package controller

import (
	"context"
	"net/http"
	"testing"
	"time"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type mockPool struct {
	invalidatedNamespaces []string
	getClientCalls        int
	getClientErr          error
}

func (m *mockPool) GetClient(_ context.Context, _ string, _ *finopsv1alpha1.TLSConfig, _ *metav1.Duration) (*http.Client, error) {
	m.getClientCalls++
	if m.getClientErr != nil {
		return nil, m.getClientErr
	}

	return &http.Client{Timeout: time.Second}, nil
}

func (m *mockPool) InvalidateNamespace(ns string) {
	m.invalidatedNamespaces = append(m.invalidatedNamespaces, ns)
}
func (m *mockPool) Invalidate(string, *finopsv1alpha1.TLSConfig, *metav1.Duration) {}

func newTestScaler(name string, mutate func(*finopsv1alpha1.IdleScalerSpec)) *finopsv1alpha1.IdleScaler {
	spec := &finopsv1alpha1.IdleScalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
			Kind:       "Deployment",
			Name:       testDeployment,
			APIVersion: "apps/v1",
		},
		Trigger: finopsv1alpha1.TriggerSpec{
			Type: finopsv1alpha1.TriggerTypeHTTP,
			HTTP: &finopsv1alpha1.HTTPTriggerSpec{
				URL: "http://dummy-url.local",
			},
		},
	}

	if mutate != nil {
		mutate(spec)
	}

	return &finopsv1alpha1.IdleScaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: *spec,
	}
}

func cleanupScaler(t *testing.T, key types.NamespacedName) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resource := &finopsv1alpha1.IdleScaler{}
	err := k8sClient.Get(ctx, key, resource)
	if errors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Logf("cleanup: get failed: %v", err)
		return
	}

	if controllerutil.RemoveFinalizer(resource, idleScalerFinalizer) {
		if err := k8sClient.Update(ctx, resource); client.IgnoreNotFound(err) != nil {
			t.Logf("cleanup: failed to detach finalizer")
		}
	}

	if err := k8sClient.Delete(ctx, resource); client.IgnoreNotFound(err) != nil {
		t.Logf("cleanup: delete failed: %v", err)
	}
}

func waitForPhase(t *testing.T, key types.NamespacedName, phase finopsv1alpha1.IdleScalerPhase) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := &finopsv1alpha1.IdleScaler{}
		if err := k8sClient.Get(t.Context(), key, got); err == nil && got.Status.Phase == phase {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("timeout waiting for Phase=%q", phase)
}
