/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
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

func newTestScaler(name, namespace string, mutate func(*finopsv1alpha1.IdleScalerSpec)) *finopsv1alpha1.IdleScaler {
	spec := &finopsv1alpha1.IdleScalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
			Kind:       "Deployment",
			Name:       "test-deployment",
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

func TestIdleScaler_Reconcile(t *testing.T) {
	t.Run("existing resource - adds finalizier", func(t *testing.T) {
		const (
			name      = "test-resource"
			namespace = "default"
		)
		key := types.NamespacedName{Name: name, Namespace: namespace}

		resource := newTestScaler(name, namespace, nil)
		if err := k8sClient.Create(t.Context(), resource); err != nil {
			t.Fatalf("failed to create resource: %v", err)
		}
		t.Cleanup(func() { cleanupScaler(t, key) })

		pool := &mockPool{}
		r := &IdleScalerReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			ClientPool: pool,
		}

		_, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: key})
		if err != nil {
			t.Fatalf("failed to reconcile resource: %v", err)
		}

		if pool.getClientCalls != 1 {
			t.Errorf("expected exactly 1 GetClient call, got %d", pool.getClientCalls)
		}

		got := &finopsv1alpha1.IdleScaler{}
		if err := k8sClient.Get(t.Context(), key, got); err != nil {
			t.Fatalf("failed to get resource: %v", err)
		}
		if !controllerutil.ContainsFinalizer(got, idleScalerFinalizer) {
			t.Errorf("expected finalizer %q to be set", idleScalerFinalizer)
		}
	})
	t.Run("resource not found - invalidates namespace", func(t *testing.T) {
		pool := &mockPool{}
		r := &IdleScalerReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			ClientPool: pool,
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "does-not-exist",
				Namespace: "default",
			},
		}

		_, err := r.Reconcile(t.Context(), req)
		if err != nil {
			t.Fatalf("Reconcile(): unexpected error: %v", err)
		}

		if pool.getClientCalls != 0 {
			t.Errorf("Reconcile(): GetClient should not be called, got %d calls", pool.getClientCalls)
		}
		if !slices.Contains(pool.invalidatedNamespaces, "default") {
			t.Errorf("Reconcile(): expected InvalidateNamespace(default), got %v", pool.invalidatedNamespaces)
		}
	})
}
