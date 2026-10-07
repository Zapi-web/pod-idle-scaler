package controller

import (
	"context"
	"fmt"
	"strconv"

	finopsv1alpha1 "github.com/zapi-web/pod-idle-scaler/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *IdleScalerReconciler) finalizeIdleScaler(ctx context.Context, scaler *finopsv1alpha1.IdleScaler) error {
	deployment, err := getDeployment(ctx, scaler, r.Client)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get target deployment during cleanup: %w", err)
	}

	scaleMin := ptr.Deref(scaler.Spec.ScaleMinimum, 0)
	base := deployment.DeepCopy()

	if deployment.Annotations != nil {
		originalReplicasStr, ok := deployment.Annotations[originalReplicasAnnotation]
		if ok {
			val, err := strconv.Atoi(originalReplicasStr)
			if err != nil {
				deployment.Spec.Replicas = new(max(scaleMin, 1))
			} else {
				deployment.Spec.Replicas = new(int32(val))
			}
			delete(deployment.Annotations, originalReplicasAnnotation)
			return r.Patch(ctx, deployment, client.MergeFrom(base))
		}
	}

	return nil
}
