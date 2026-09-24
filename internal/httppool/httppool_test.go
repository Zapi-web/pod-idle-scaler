package httppool

import (
	"crypto/sha256"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetClient_Concurrency(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	pool := NewClientPool(reader)

	const goroutines = 50

	results := make([]*http.Client, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			c, err := pool.GetClient(t.Context(), "ns", nil, nil)
			if err != nil {
				t.Errorf("goroutine %d; got an error: %v", i, err)
			}
			results[i] = c
		}(i)
	}
	wg.Wait()

	for i := range goroutines {
		if results[i] != results[0] {
			t.Fatalf("Got another result with similar configs: expected %v; got %v", results[0], results[i])
		}
	}

	if len(pool.clients) != 1 {
		t.Fatalf("expected only 1 client, got %d", len(pool.clients))
	}
}

func TestGetClient_Cache(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	pool := NewClientPool(reader)

	t.Run("Cache hit", func(t *testing.T) {
		cacheRes1, err := pool.GetClient(t.Context(), "ns", nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cacheRes2, err := pool.GetClient(t.Context(), "ns", nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cacheRes1 != cacheRes2 {
			t.Fatal("expected same client from cache")
		}
	})

	t.Run("different namespaces", func(t *testing.T) {
		cacheRes1, err := pool.GetClient(t.Context(), "ns1", nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cacheRes2, err := pool.GetClient(t.Context(), "ns2", nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cacheRes1 == cacheRes2 {
			t.Fatal("Different namespace should give different clients")
		}
	})

	t.Run("invalidate forces rebuild", func(t *testing.T) {
		c1, err := pool.GetClient(t.Context(), "ns-inv", nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		pool.Invalidate("ns-inv", nil, nil)
		c2, err := pool.GetClient(t.Context(), "ns-inv", nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c1 == c2 {
			t.Fatal("expected rebuild after invalidate")
		}
	})
}

func TestGetClient_DifferentTLSConfigs(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	pool := NewClientPool(reader)

	c1, err := pool.GetClient(t.Context(), "ns", &v1alpha1.TLSConfig{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c2, err := pool.GetClient(t.Context(), "ns", &v1alpha1.TLSConfig{InsecureSkipVerify: false}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c1 == c2 {
		t.Fatal("different TLS configs should give different clients")
	}
	if len(pool.clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(pool.clients))
	}
}

func TestInvalidate(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	pool := NewClientPool(reader)

	cfg := &v1alpha1.TLSConfig{InsecureSkipVerify: true}
	dur := &metav1.Duration{Duration: 15 * time.Second}

	c1, err := pool.GetClient(t.Context(), "ns", cfg, dur)
	if err != nil {
		t.Fatalf("first GetClient: %v", err)
	}
	if len(pool.clients) != 1 {
		t.Fatalf("expected 1 client before invalidate, got %d", len(pool.clients))
	}

	pool.Invalidate("ns", cfg, dur)
	if len(pool.clients) != 0 {
		t.Fatalf("expected pool to be empty after invalidate, got %d", len(pool.clients))
	}

	c2, err := pool.GetClient(t.Context(), "ns", cfg, dur)
	if err != nil {
		t.Fatalf("second GetClient: %v", err)
	}
	if c1 == c2 {
		t.Fatal("expected rebuild after invalidate")
	}
}

func TestInvalidateNamespace(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	pool := NewClientPool(reader)

	if _, err := pool.GetClient(t.Context(), "ns1", nil, nil); err != nil {
		t.Fatalf("GetClient ns1: %v", err)
	}
	if _, err := pool.GetClient(t.Context(), "ns1", &v1alpha1.TLSConfig{InsecureSkipVerify: true}, nil); err != nil {
		t.Fatalf("GetClient ns1 tls: %v", err)
	}
	if _, err := pool.GetClient(t.Context(), "ns2", nil, nil); err != nil {
		t.Fatalf("GetClient ns2: %v", err)
	}

	pool.InvalidateNamespace("ns1")

	if len(pool.clients) != 1 {
		t.Fatalf("expected 1 client left (ns2), got %d", len(pool.clients))
	}
	for key := range pool.clients {
		if key.Namespace != "ns2" {
			t.Fatalf("expected only ns2 left, got %q", key.Namespace)
		}
	}
}

func TestNormalizeTimeout(t *testing.T) {
	tests := []struct {
		Name string
		In   *metav1.Duration
		Want time.Duration
	}{
		{
			Name: "Positive",
			In:   &metav1.Duration{Duration: 10 * time.Second},
			Want: 10 * time.Second,
		},
		{
			Name: "Negative duration",
			In:   &metav1.Duration{Duration: -10 * time.Second},
			Want: 5 * time.Second,
		},
		{
			Name: "Zero stays zero",
			In:   &metav1.Duration{Duration: 0 * time.Second},
			Want: 0 * time.Second,
		},
		{
			Name: "Nil pointer",
			In:   nil,
			Want: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			res := normalizeTimeout(tt.In)

			if res != tt.Want {
				t.Fatalf("normalizeTimeout() expected result: %v; got %v", tt.Want, res)
			}
		})
	}
}

func TestHashTLS(t *testing.T) {
	newStruct := func() *v1alpha1.TLSConfig {
		return &v1alpha1.TLSConfig{
			InsecureSkipVerify: true,
			CASecretRef: &v1.SecretKeySelector{
				LocalObjectReference: v1.LocalObjectReference{
					Name: "some-name",
				},
				Key: "some-key",
			},
		}
	}

	t.Run("Determinism", func(t *testing.T) {
		a := hashTLS(newStruct())
		b := hashTLS(newStruct())
		if a != b {
			t.Fatal("hashTLS() same configs should give same hashes")
		}
	})

	t.Run("nil", func(t *testing.T) {
		if hashTLS(nil) != sha256.Sum256(nil) {
			t.Fatal("nil config should give sha256.Sum256(nil) back")
		}
	})

	t.Run("Different Configs", func(t *testing.T) {
		variants := []struct {
			name string
			cfg  *v1alpha1.TLSConfig
		}{
			{
				name: "base",
				cfg:  newStruct(),
			},
			{
				name: "insecure off",
				cfg: func() *v1alpha1.TLSConfig {
					base := newStruct()
					base.InsecureSkipVerify = false
					return base
				}(),
			},
			{
				name: "other name",
				cfg: func() *v1alpha1.TLSConfig {
					base := newStruct()
					base.CASecretRef.Name = "another-name"
					return base
				}(),
			},
			{
				name: "other key",
				cfg: func() *v1alpha1.TLSConfig {
					base := newStruct()
					base.CASecretRef.Key = "another-key"
					return base
				}(),
			},
			{
				name: "no ref",
				cfg: func() *v1alpha1.TLSConfig {
					base := newStruct()
					base.CASecretRef = nil
					return base
				}(),
			},
			{
				name: "empty config",
				cfg:  &v1alpha1.TLSConfig{},
			},
			{
				name: "nil",
				cfg:  nil,
			},
		}

		seen := make(map[[32]byte]string, len(variants))

		for _, v := range variants {
			hash := hashTLS(v.cfg)
			if prev, ok := seen[hash]; ok {
				t.Fatalf("configs %q and %q produce the same hash", prev, v.name)
			}
			seen[hash] = v.name
		}
	})
}
