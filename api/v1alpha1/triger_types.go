package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TriggerType provides trigger type
// +kubebuilder:validation:Enum=HTTP;PromQL
type TriggerType string

const (
	TriggerTypeHTTP   TriggerType = "HTTP"
	TriggerTypePromQL TriggerType = "PromQL"
)

// +kubebuilder:validation:XValidation:rule="(self.type == 'HTTP' && has(self.http) && !has(self.promql)) || (self.type == 'PromQL' && has(self.promql) && !has(self.http))",message="trigger configuration must match the specified type and only one config can be provided"
type TriggerSpec struct {
	// Type defines the trigger mechanism
	// +required
	Type TriggerType `json:"type"`

	// HTTP trigger configuration
	// +optional
	HTTP *HTTPTriggerSpec `json:"http,omitempty"`

	// PromQL trigger configuration
	// +optional
	PromQL *PromQLTriggerSpec `json:"promql,omitempty"`
}

type HTTPTriggerSpec struct {
	// URL endpoint to probe
	// +kubebuilder:validation:Pattern=`^https?://.*`
	// +required
	URL string `json:"url"`

	// Method for HTTP request
	// +kubebuilder:default="GET"
	// +kubebuilder:validation:Enum=GET;POST;HEAD
	// +optional
	Method string `json:"method,omitempty"`

	// Timeout for HTTP request
	// +kubebuilder:default="5s"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// ExpectedCode is the expected HTTP answer code
	// +kubebuilder:default=200
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:validation:Maximum=599
	// +optional
	ExpectedCode *int32 `json:"expectedCode,omitempty"`

	// Headers include in the request
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// BodyPattern is a regex pattern that response body must match to consider service active
	// +optional
	BodyPattern string `json:"bodyPattern,omitempty"`

	// TokenSecretRef references a Secret key containing an auth token for headers
	// +optional
	TokenSecretRef *corev1.SecretKeySelector `json:"tokenSecretRef,omitempty"`

	// TLSconfig provides TLS connection settings
	// +optional
	TLSConfig *TLSConfig `json:"tlsConfig,omitempty"`
}

type PromQLTriggerSpec struct {
	// URL of the Prometheus/VictoriaMetrics etc.
	// +kubebuilder:validation:Pattern=`^https?://.*`
	// +required
	URL string `json:"url"`

	// Query is the PromQL code to execute
	// +required
	Query string `json:"query"`

	// Threshold defines the value above which the workload equals active state
	// Default to 0 (metric value > 0 resets the timer)
	// +kubebuilder:default="0"
	// +optional
	Threshold *resource.Quantity `json:"threshold,omitempty"`

	// Timeout for the query execution
	// +kubebuilder:default="5s"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// TokenSecretRef references a Secret key for Prometheus Bearer auth
	// +optional
	TokenSecretRef *corev1.SecretKeySelector `json:"tokenSecretRef,omitempty"`

	// TLSconfig provides TLS connection settings
	// +optional
	TLSConfig *TLSConfig `json:"tlsConfig,omitempty"`
}
