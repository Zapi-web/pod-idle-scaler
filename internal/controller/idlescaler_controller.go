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
	"fmt"
	"net/http"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
)

// IdleScalerReconciler reconciles a IdleScaler object
type IdleScalerReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	ClientPool ConnectionPool
}

type TriggerSettings struct {
	TLSConfig *finopsv1alpha1.TLSConfig
	Timeout   *metav1.Duration
}

type ConnectionPool interface {
	GetClient(ctx context.Context, ns string, tlsConfig *finopsv1alpha1.TLSConfig, timeout *metav1.Duration) (*http.Client, error)
	InvalidateNamespace(ns string)
	Invalidate(ns string, tlsConfig *finopsv1alpha1.TLSConfig, timeout *metav1.Duration)
}

const idleScalerFinalizer = "finops.zapi-web.github.io/finalizer"
const originalReplicasAnnotation = "finops.zapi-web.github.io/original-replicas"

// +kubebuilder:rbac:groups=finops.zapi-web.github.io,resources=idlescalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.zapi-web.github.io,resources=idlescalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.zapi-web.github.io,resources=idlescalers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the IdleScaler object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/reconcile
func (r *IdleScalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klogger := logf.FromContext(ctx)

	scaler := finopsv1alpha1.IdleScaler{}
	if err := r.Get(ctx, req.NamespacedName, &scaler); err != nil {
		if errors.IsNotFound(err) {
			klogger.Info("IdleScaler is not found")
			r.ClientPool.InvalidateNamespace(req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !scaler.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&scaler, idleScalerFinalizer) {
			klogger.Info("perform cleanup before deleting IdleScaler")

			if err := r.finalizeIdleScaler(ctx, &scaler); err != nil {
				klogger.Error(err, "failed to finalize target deployment")
				return ctrl.Result{}, err
			}
		}

		controllerutil.RemoveFinalizer(&scaler, idleScalerFinalizer)
		if err := r.Update(ctx, &scaler); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&scaler, idleScalerFinalizer) {
		controllerutil.AddFinalizer(&scaler, idleScalerFinalizer)
		if err := r.Update(ctx, &scaler); err != nil {
			return ctrl.Result{}, err
		}
	}

	checkTimeout := 1 * time.Minute
	if scaler.Spec.CheckTimeout != nil && scaler.Spec.CheckTimeout.Duration > 0 {
		checkTimeout = scaler.Spec.CheckTimeout.Duration
	}

	idleTimeout := 15 * time.Minute
	if scaler.Spec.IdleTimeout != nil && scaler.Spec.IdleTimeout.Duration > 0 {
		idleTimeout = scaler.Spec.IdleTimeout.Duration
	}

	triggerSettings, err := getTriggerSettings(scaler.Spec)
	if err != nil {
		klogger.Error(err, "invalid trigger configuration")
		r.setErrorPhase(ctx, &scaler)
		return ctrl.Result{RequeueAfter: checkTimeout}, nil
	}

	httpClient, err := r.ClientPool.GetClient(ctx, req.Namespace, triggerSettings.TLSConfig, triggerSettings.Timeout)
	if err != nil {
		klogger.Error(err, "failed to get HTTP client")
		r.setErrorPhase(ctx, &scaler)
		return ctrl.Result{}, err
	}

	trigger, err := NewTrigger(r.Client, scaler.Spec.Trigger.Type, httpClient)
	if err != nil {
		klogger.Error(err, "failed to build trigger")
		r.setErrorPhase(ctx, &scaler)
		return ctrl.Result{RequeueAfter: checkTimeout}, nil
	}

	active, err := trigger.IsActive(ctx, &scaler)

	if err != nil {
		klogger.Error(err, "failed to check activity status")
		r.setErrorPhase(ctx, &scaler)
		return ctrl.Result{RequeueAfter: checkTimeout}, nil
	}

	now := metav1.Now()
	if active {
		if scaler.Status.Phase == finopsv1alpha1.PhaseSleeping {
			klogger.Info("Activity detected, scaling up deployment")
			if err := r.ScaleUp(ctx, &scaler); err != nil {
				return ctrl.Result{}, err
			}
		}

		scaler.Status.LastActivityTime = &now
		scaler.Status.Phase = finopsv1alpha1.PhaseActive
		if err := r.Status().Update(ctx, &scaler); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: checkTimeout}, nil
	}

	if scaler.Status.Phase == finopsv1alpha1.PhaseSleeping {
		return ctrl.Result{RequeueAfter: checkTimeout}, nil
	}

	if scaler.Status.LastActivityTime == nil {
		scaler.Status.LastActivityTime = &now
		scaler.Status.Phase = finopsv1alpha1.PhaseIdling
		if err := r.Status().Update(ctx, &scaler); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: checkTimeout}, nil
	}

	elapsed := time.Since(scaler.Status.LastActivityTime.Time)
	if elapsed < idleTimeout {
		remaining := idleTimeout - elapsed
		if scaler.Status.Phase != finopsv1alpha1.PhaseIdling {
			scaler.Status.Phase = finopsv1alpha1.PhaseIdling
			if err := r.Status().Update(ctx, &scaler); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: min(remaining, checkTimeout)}, nil
	}
	scaleMin := ptr.Deref(scaler.Spec.ScaleMinimum, 0)
	klogger.Info("Idle timeout exceeded, scaling down deployment", "scaleMinimum", scaleMin)
	if err := r.ScaleDown(ctx, &scaler); err != nil {
		return ctrl.Result{}, err
	}

	scaler.Status.Phase = finopsv1alpha1.PhaseSleeping
	if err := r.Status().Update(ctx, &scaler); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: checkTimeout}, nil
}

func (r *IdleScalerReconciler) ScaleUp(ctx context.Context, scaler *finopsv1alpha1.IdleScaler) error {
	deployment, err := getDeployment(ctx, scaler, r.Client)
	if err != nil {
		return fmt.Errorf("failed to get target deployment: %w", err)
	}

	if deployment.Annotations == nil {
		deployment.Annotations = make(map[string]string)
	}

	base := deployment.DeepCopy()
	scaleMin := ptr.Deref(scaler.Spec.ScaleMinimum, 0)

	originalReplicasStr, ok := deployment.Annotations[originalReplicasAnnotation]
	if !ok {
		if ptr.Deref(deployment.Spec.Replicas, 0) <= scaleMin {
			deployment.Spec.Replicas = new(max(scaleMin, 1))
			return r.Patch(ctx, deployment, client.MergeFrom(base))
		}
		return nil
	}
	originalReplicasInt, err := strconv.Atoi(originalReplicasStr)
	if err != nil {
		klogger := logf.FromContext(ctx)
		klogger.Error(err, "invalid original-replicas annotation, removing it",
			"value", originalReplicasStr)
		originalReplicasInt = max(int(scaleMin), 1)
	}

	deployment.Spec.Replicas = new(int32(originalReplicasInt))
	delete(deployment.Annotations, originalReplicasAnnotation)

	return r.Patch(ctx, deployment, client.MergeFrom(base))
}

func (r *IdleScalerReconciler) ScaleDown(ctx context.Context, scaler *finopsv1alpha1.IdleScaler) error {
	deployment, err := getDeployment(ctx, scaler, r.Client)
	if err != nil {
		return fmt.Errorf("failed to get target deployment: %w", err)
	}

	base := deployment.DeepCopy()
	if deployment.Annotations == nil {
		deployment.Annotations = make(map[string]string)
	}

	scaleMin := ptr.Deref(scaler.Spec.ScaleMinimum, 0)
	currentReplicas := ptr.Deref(deployment.Spec.Replicas, 1)
	if currentReplicas <= scaleMin {
		return nil
	}

	if _, ok := deployment.Annotations[originalReplicasAnnotation]; !ok {
		deployment.Annotations[originalReplicasAnnotation] = strconv.Itoa(int(currentReplicas))
	}
	deployment.Spec.Replicas = new(scaleMin)

	return r.Patch(ctx, deployment, client.MergeFrom(base))
}

func getDeployment(ctx context.Context, scaler *finopsv1alpha1.IdleScaler, c client.Reader) (*appsv1.Deployment, error) {
	targetName := scaler.Spec.ScaleTargetRef.Name
	if targetName == "" {
		return nil, fmt.Errorf("scaleTargetRef.Name is not specified")
	}

	kind := scaler.Spec.ScaleTargetRef.Kind
	if kind != "" && kind != "Deployment" {
		return nil, fmt.Errorf("unsupported scaleTargetRef.Kind %q: only Deployment is supported", kind)
	}

	key := types.NamespacedName{
		Name:      targetName,
		Namespace: scaler.Namespace,
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(ctx, key, deployment); err != nil {
		return nil, fmt.Errorf("failed to find target deployment %s: %w", targetName, err)
	}

	return deployment, nil
}

func (r *IdleScalerReconciler) setErrorPhase(ctx context.Context, scaler *finopsv1alpha1.IdleScaler) {
	if scaler.Status.Phase == finopsv1alpha1.PhaseError {
		return
	}
	klogger := logf.FromContext(ctx)
	scaler.Status.Phase = finopsv1alpha1.PhaseError
	if err := r.Status().Update(ctx, scaler); err != nil {
		klogger.Error(err, "failed to update status to Error phase")
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *IdleScalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1alpha1.IdleScaler{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("idlescaler").
		Complete(r)
}

func getTriggerSettings(spec finopsv1alpha1.IdleScalerSpec) (TriggerSettings, error) {
	switch spec.Trigger.Type {
	case finopsv1alpha1.TriggerTypeHTTP:
		if spec.Trigger.HTTP == nil {
			return TriggerSettings{}, fmt.Errorf("http trigger config missing")
		}
		return TriggerSettings{
			TLSConfig: spec.Trigger.HTTP.TLSConfig,
			Timeout:   spec.Trigger.HTTP.Timeout,
		}, nil
	case finopsv1alpha1.TriggerTypePromQL:
		if spec.Trigger.PromQL == nil {
			return TriggerSettings{}, fmt.Errorf("promql trigger config missing")
		}
		return TriggerSettings{
			TLSConfig: spec.Trigger.PromQL.TLSConfig,
			Timeout:   spec.Trigger.PromQL.Timeout,
		}, nil
	default:
		return TriggerSettings{}, fmt.Errorf("unsupported trigger type: %s", spec.Trigger.Type)
	}
}

func NewTrigger(c client.Reader, triggerType finopsv1alpha1.TriggerType, httpClient *http.Client) (ActivityChecker, error) {
	switch triggerType {
	case finopsv1alpha1.TriggerTypeHTTP:
		return NewHTTPChecker(c, httpClient), nil
	case finopsv1alpha1.TriggerTypePromQL:
		return NewPromQLChecker(c, httpClient), nil
	default:
		return nil, fmt.Errorf("unsupported trigger type: %s", triggerType)
	}
}
