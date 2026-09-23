package v1alpha1

import corev1 "k8s.io/api/core/v1"

type TLSConfig struct {
	// InsecureSkipVerify disable TLS certificate verification
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// CASecretRef references a Secret containing the CA bundle
	// +optional
	CASecretRef *corev1.SecretKeySelector `json:"caSecretRef,omitempty"`
}
