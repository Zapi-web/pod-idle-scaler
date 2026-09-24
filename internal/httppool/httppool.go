package httppool

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClientPool struct {
	k8sClient client.Reader
	mu        sync.RWMutex
	clients   map[clientKey]*http.Client
}

type clientKey struct {
	Namespace string
	tlsHash   [32]byte
	timeout   time.Duration
}

func NewClientPool(reader client.Reader) *ClientPool {
	return &ClientPool{
		k8sClient: reader,
		clients:   make(map[clientKey]*http.Client),
	}
}

func (c *ClientPool) GetClient(ctx context.Context, ns string, tlsConfig *v1alpha1.TLSConfig, timeout *metav1.Duration) (*http.Client, error) {
	fullKey := clientKey{
		Namespace: ns,
		tlsHash:   hashTLS(tlsConfig),
		timeout:   normalizeTimeout(timeout),
	}

	c.mu.RLock()
	cl, ok := c.clients[fullKey]
	c.mu.RUnlock()

	if ok {
		return cl, nil
	}

	newClient, err := c.buildClient(ctx, ns, tlsConfig, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to build client: %w", err)
	}

	c.mu.Lock()
	if existing, ok := c.clients[fullKey]; !ok {
		c.clients[fullKey] = newClient
	} else {
		if tr, ok := newClient.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
		newClient = existing
	}
	c.mu.Unlock()

	return newClient, nil
}

func (c *ClientPool) InvalidateNamespace(ns string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, cl := range c.clients {
		if key.Namespace != ns {
			continue
		}
		if tr, ok := cl.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
		delete(c.clients, key)
	}
}

func (c *ClientPool) Invalidate(ns string, tlsConfig *v1alpha1.TLSConfig, timeout *metav1.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := clientKey{
		Namespace: ns,
		tlsHash:   hashTLS(tlsConfig),
		timeout:   normalizeTimeout(timeout),
	}

	if cl, ok := c.clients[key]; ok {
		if tr, ok := cl.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
		delete(c.clients, key)
	}
}

func (c *ClientPool) buildClient(ctx context.Context, ns string, tlsConfig *v1alpha1.TLSConfig, timeout *metav1.Duration) (*http.Client, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()

	if tlsConfig != nil {
		tlsClientConf := &tls.Config{
			InsecureSkipVerify: tlsConfig.InsecureSkipVerify,
		}

		if tlsConfig.CASecretRef != nil {
			rootCAs, err := c.loadCACertPool(ctx, ns, tlsConfig.CASecretRef)
			if err != nil {
				return nil, fmt.Errorf("failed to load root CAs: %w", err)
			}
			tlsClientConf.RootCAs = rootCAs
		}

		transport.TLSClientConfig = tlsClientConf
	}

	return &http.Client{
		Transport: transport,
		Timeout:   normalizeTimeout(timeout),
	}, nil
}

func (c *ClientPool) loadCACertPool(ctx context.Context, ns string, ref *corev1.SecretKeySelector) (*x509.CertPool, error) {
	var secret corev1.Secret
	err := c.k8sClient.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      ref.Name,
	}, &secret)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch secret %s/%s: %w", ns, ref.Name, err)
	}

	key := ref.Key
	if key == "" {
		key = "ca.crt"
	}

	caBytes, ok := secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s/%s", key, ns, ref.Name)
	}

	rootCAs, err := x509.SystemCertPool()
	if err != nil || rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}

	if ok := rootCAs.AppendCertsFromPEM(caBytes); !ok {
		return nil, fmt.Errorf("failed to parse CA certificate from secret %s (invalid PEM)", ref.Name)
	}

	return rootCAs, nil
}

func normalizeTimeout(timeout *metav1.Duration) time.Duration {
	if timeout != nil && timeout.Duration >= 0 {
		return timeout.Duration
	}

	return 5 * time.Second
}

func hashTLS(t *v1alpha1.TLSConfig) [32]byte {
	if t == nil {
		return sha256.Sum256(nil)
	}
	var refName, refKey string
	if t.CASecretRef != nil {
		refName = t.CASecretRef.Name
		refKey = t.CASecretRef.Key
	}

	s := fmt.Sprintf("%t|%s|%s", t.InsecureSkipVerify, refName, refKey)
	return sha256.Sum256([]byte(s))
}
