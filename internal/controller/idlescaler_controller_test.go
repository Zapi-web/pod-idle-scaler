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
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
)

const (
	namespace      = "default"
	targetName     = "target"
	testDeployment = "test-deployment"
)

func TestIdleScaler_Reconcile(t *testing.T) {
	t.Run("existing resource - adds finalizer", func(t *testing.T) {
		const (
			name = "test-resource"
		)

		key := types.NamespacedName{Name: name, Namespace: namespace}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		resource := newTestScaler(name, func(s *finopsv1alpha1.IdleScalerSpec) {
			s.Trigger.HTTP.URL = srv.URL
		})
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

func TestIdleScaler_ReconcileFullPath(t *testing.T) {
	const mapKey = "app"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testDeployment,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(3)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{mapKey: testDeployment},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{mapKey: testDeployment},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx"},
					},
				},
			},
		},
	}

	if err := k8sClient.Create(t.Context(), deployment); err != nil {
		t.Fatalf("Reconcile(): create deployment: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), deployment)
	})

	resource := newTestScaler("test", func(s *finopsv1alpha1.IdleScalerSpec) {
		s.Trigger.HTTP.URL = srv.URL
		s.IdleTimeout = &metav1.Duration{Duration: time.Millisecond}
		s.ScaleMinimum = new(int32(1))
	})

	key := types.NamespacedName{Name: "test", Namespace: namespace}

	if err := k8sClient.Create(t.Context(), resource); err != nil {
		t.Fatalf("Reconcile(): create scaler: %v", err)
	}
	t.Cleanup(func() {
		cleanupScaler(t, key)
	})

	r := &IdleScalerReconciler{
		Client:     k8sClient,
		Scheme:     k8sClient.Scheme(),
		ClientPool: &mockPool{},
	}

	if _, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile(): first reconcile: %v", err)
	}

	waitForPhase(t, key, finopsv1alpha1.PhaseIdling)

	if _, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile(): second reconcile: %v", err)
	}

	got := &appsv1.Deployment{}
	if err := k8sClient.Get(t.Context(), types.NamespacedName{Name: testDeployment, Namespace: namespace}, got); err != nil {
		t.Fatalf("Reconcile(): failed to get deployment: %v", err)
	}
	if *got.Spec.Replicas != 1 {
		t.Errorf("Reconcile(): replicas: got %d, want 1", *got.Spec.Replicas)
	}
	if got.Annotations[originalReplicasAnnotation] != "3" {
		t.Errorf("annotation: got %q, want 3", got.Annotations[originalReplicasAnnotation])
	}
}

func TestIdleScaler_Deletion(t *testing.T) {
	const name = "test-deletion"
	key := types.NamespacedName{Name: name, Namespace: namespace}

	resource := newTestScaler(name, nil)
	resource.Finalizers = []string{idleScalerFinalizer}
	if err := k8sClient.Create(t.Context(), resource); err != nil {
		t.Fatalf("Reconcile(): create: %v", err)
	}
	t.Cleanup(func() {
		cleanupScaler(t, key)
	})

	if err := k8sClient.Delete(t.Context(), resource); err != nil {
		t.Fatalf("Reconcile(): delete: %v", err)
	}

	r := &IdleScalerReconciler{
		Client:     k8sClient,
		Scheme:     k8sClient.Scheme(),
		ClientPool: &mockPool{},
	}
	if _, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := k8sClient.Get(t.Context(), key, &finopsv1alpha1.IdleScaler{})
		if errors.IsNotFound(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("resource still exists — finalizer not removed")
}

func TestGetTriggerSettings(t *testing.T) {
	tests := []struct {
		name        string
		spec        finopsv1alpha1.IdleScalerSpec
		expectError bool
	}{
		{
			name: "good http spec",
			spec: finopsv1alpha1.IdleScalerSpec{
				Trigger: finopsv1alpha1.TriggerSpec{
					Type: finopsv1alpha1.TriggerTypeHTTP,
					HTTP: &finopsv1alpha1.HTTPTriggerSpec{URL: "http://some-url"},
				},
			},
			expectError: false,
		},
		{
			name: "http nil",
			spec: finopsv1alpha1.IdleScalerSpec{
				Trigger: finopsv1alpha1.TriggerSpec{
					Type: finopsv1alpha1.TriggerTypeHTTP,
				},
			},
			expectError: true,
		},
		{
			name: "promql ok",
			spec: finopsv1alpha1.IdleScalerSpec{
				Trigger: finopsv1alpha1.TriggerSpec{
					Type:   finopsv1alpha1.TriggerTypePromQL,
					PromQL: &finopsv1alpha1.PromQLTriggerSpec{},
				},
			},
			expectError: false,
		},
		{
			name: "promql config nil",
			spec: finopsv1alpha1.IdleScalerSpec{
				Trigger: finopsv1alpha1.TriggerSpec{Type: finopsv1alpha1.TriggerTypePromQL},
			},
			expectError: true,
		},
		{
			name: "unknown type",
			spec: finopsv1alpha1.IdleScalerSpec{
				Trigger: finopsv1alpha1.TriggerSpec{Type: "wat"},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getTriggerSettings(tt.spec)
			if tt.expectError && err == nil {
				t.Fatal("getTriggerSettings(): expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Fatalf("getTriggerSettings(): unexpected error: %v", err)
			}
		})
	}
}

func TestScaleDown(t *testing.T) {
	tests := []struct {
		name                string
		replicas            int32
		scaleMin            int32
		existingAnnotations map[string]string
		wantReplicas        int32
		wantAnnotation      string
	}{
		{
			name:           "Scale Down, save original",
			replicas:       3,
			scaleMin:       1,
			wantReplicas:   1,
			wantAnnotation: "3",
		},
		{
			name:           "Already at min - nothing to do",
			replicas:       1,
			scaleMin:       1,
			wantReplicas:   1,
			wantAnnotation: "",
		},
		{
			name:           "Below min - nothing to do",
			replicas:       1,
			scaleMin:       2,
			wantReplicas:   1,
			wantAnnotation: "",
		},
		{
			name:                "annotation already present — not overwritten",
			replicas:            4,
			scaleMin:            1,
			existingAnnotations: map[string]string{originalReplicasAnnotation: "7"},
			wantReplicas:        1,
			wantAnnotation:      "7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scaler := newTestScaler("scaler", func(s *finopsv1alpha1.IdleScalerSpec) {
				s.ScaleTargetRef.Name = targetName
				s.ScaleMinimum = new(tt.scaleMin)
			})

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:        targetName,
					Namespace:   namespace,
					Annotations: tt.existingAnnotations,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(tt.replicas),
				},
			}

			cl := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(deployment).Build()

			r := &IdleScalerReconciler{Client: cl, Scheme: k8sClient.Scheme()}

			if err := r.ScaleDown(t.Context(), scaler); err != nil {
				t.Fatalf("ScaleDown error: %v", err)
			}

			got := &appsv1.Deployment{}
			if err := cl.Get(t.Context(), types.NamespacedName{Name: targetName, Namespace: namespace}, got); err != nil {
				t.Fatalf("ScaleDown(): failed to get result: %v", err)
			}

			if *got.Spec.Replicas != tt.wantReplicas {
				t.Errorf("ScaleDown(): replicas got: %d, want %d", *got.Spec.Replicas, tt.wantReplicas)
			}

			gotAnn := got.Annotations[originalReplicasAnnotation]
			if gotAnn != tt.wantAnnotation {
				t.Errorf("ScaleDown(): annotation got %q, want %q", gotAnn, tt.wantAnnotation)
			}
		})
	}
}

func TestScaleUp(t *testing.T) {
	tests := []struct {
		name           string
		replicas       int32
		scaleMin       int32
		existingAnnot  string
		wantReplicas   int32
		wantAnnotation string
	}{
		{
			name:           "restores from annotation",
			replicas:       0,
			scaleMin:       1,
			existingAnnot:  "3",
			wantReplicas:   3,
			wantAnnotation: "",
		},
		{
			name:           "invalid annotation → falls back to max(scaleMin, 1)",
			replicas:       0,
			scaleMin:       2,
			existingAnnot:  "abc",
			wantReplicas:   2,
			wantAnnotation: "",
		},
		{
			name:           "no annotation, replicas below min → set to max(scaleMin, 1)",
			replicas:       0,
			scaleMin:       2,
			wantReplicas:   2,
			wantAnnotation: "",
		},
		{
			name:           "no annotation, replicas above min → no change",
			replicas:       5,
			scaleMin:       1,
			wantReplicas:   5,
			wantAnnotation: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scaler := newTestScaler("scaler", func(s *finopsv1alpha1.IdleScalerSpec) {
				s.ScaleTargetRef.Name = targetName
				s.ScaleMinimum = new(tt.scaleMin)
			})

			var annot map[string]string
			if tt.existingAnnot != "" {
				annot = make(map[string]string)
				annot[originalReplicasAnnotation] = tt.existingAnnot
			}

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:        targetName,
					Namespace:   namespace,
					Annotations: annot,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: new(tt.replicas),
				},
			}

			cl := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(deployment).Build()

			r := &IdleScalerReconciler{Client: cl, Scheme: k8sClient.Scheme()}

			if err := r.ScaleUp(t.Context(), scaler); err != nil {
				t.Fatalf("ScaleUp error: %v", err)
			}

			got := &appsv1.Deployment{}
			if err := cl.Get(t.Context(), types.NamespacedName{Name: targetName, Namespace: namespace}, got); err != nil {
				t.Fatalf("ScaleUp(): failed to get result: %v", err)
			}

			if *got.Spec.Replicas != tt.wantReplicas {
				t.Errorf("ScaleUp(): replicas got: %d, want %d", *got.Spec.Replicas, tt.wantReplicas)
			}

			gotAnn := got.Annotations[originalReplicasAnnotation]
			if gotAnn != tt.wantAnnotation {
				t.Errorf("ScaleUp(): annotation got %q, want %q", gotAnn, tt.wantAnnotation)
			}

		})
	}
}
