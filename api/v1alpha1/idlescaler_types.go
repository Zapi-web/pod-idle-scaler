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

package v1alpha1

import (
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// IdleScalerPhase describe current lifecycle phase of the scaler
// +kubebuilder:validation:Enum=Active;Idling;Sleeping;Error
type IdleScalerPhase string

const (
	PhaseActive   IdleScalerPhase = "Active"
	PhaseIdling   IdleScalerPhase = "Idling"
	PhaseSleeping IdleScalerPhase = "Sleeping"
	PhaseError    IdleScalerPhase = "Error"
)

// IdleScalerSpec defines the desired state of IdleScaler
type IdleScalerSpec struct {
	// Replicas to scale up to when waking up
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Time before reduce
	// +kubebuilder:default="15m"
	// +optional
	IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`

	// Link on Deployment
	// +required
	ScaleTargetRef autoscalingv2.CrossVersionObjectReference `json:"scaleTargetRef"`

	// Minimal scaling number
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	ScaleMinimum *int32 `json:"scaleMinimum,omitempty"`

	// Gets Activity metric from Custom/External Metrics API
	// +required
	Trigger TriggerSpec `json:"trigger"`
}

// IdleScalerStatus defines the observed state of IdleScaler.
type IdleScalerStatus struct {
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Number of ready replicas
	// +optional
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`

	// Phase provides a summary of the scaller state
	// +optional
	Phase IdleScalerPhase `json:"phase,omitempty"`

	// Last Activity Time
	// +optional
	LastActivityTime *metav1.Time `json:"lastActivityTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// IdleScaler is the Schema for the idlescalers API
type IdleScaler struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of IdleScaler
	// +required
	Spec IdleScalerSpec `json:"spec"`

	// status defines the observed state of IdleScaler
	// +optional
	Status IdleScalerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// IdleScalerList contains a list of IdleScaler
type IdleScalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []IdleScaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &IdleScaler{}, &IdleScalerList{})
		return nil
	})
}
