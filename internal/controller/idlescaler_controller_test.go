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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
)

type mockPool struct{}

func (m *mockPool) GetClient(_ context.Context, _ string, _ *finopsv1alpha1.TLSConfig, _ *metav1.Duration) (*http.Client, error) {
	return &http.Client{Timeout: time.Second}, nil
}

func (m *mockPool) InvalidateNamespace(_ string)                                         {}
func (m *mockPool) Invalidate(_ string, _ *finopsv1alpha1.TLSConfig, _ *metav1.Duration) {}

var _ = Describe("IdleScaler Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		idlescaler := &finopsv1alpha1.IdleScaler{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind IdleScaler")
			err := k8sClient.Get(ctx, typeNamespacedName, idlescaler)
			if err != nil && errors.IsNotFound(err) {
				resource := &finopsv1alpha1.IdleScaler{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: finopsv1alpha1.IdleScalerSpec{
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
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &finopsv1alpha1.IdleScaler{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance IdleScaler")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &IdleScalerReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				ClientPool: &mockPool{},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
