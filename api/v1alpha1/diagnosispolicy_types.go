package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DiagnosisPolicySpec defines the desired behaviour for event-triggered diagnosis (REQ-003).
type DiagnosisPolicySpec struct {
	// Enabled toggles this policy on or off. Defaults to true when omitted.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// EventReasons is the allowlist of Kubernetes event reasons this policy matches.
	// When empty the operator falls back to its global allowlist.
	// +optional
	EventReasons []string `json:"eventReasons,omitempty"`

	// LLMProvider overrides the operator-level LLM provider for diagnoses matched by this policy.
	// +optional
	LLMProvider string `json:"llmProvider,omitempty"`

	// LLMModel overrides the operator-level LLM model.
	// +optional
	LLMModel string `json:"llmModel,omitempty"`

	// LLMBaseURL overrides the operator-level LLM base URL.
	// +optional
	LLMBaseURL string `json:"llmBaseURL,omitempty"`

	// MaxIterations caps the diagnosis loop depth for diagnoses under this policy.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	MaxIterations *int32 `json:"maxIterations,omitempty"`

	// Redact controls whether sensitive data is scrubbed from prompts for this policy.
	// +optional
	Redact *bool `json:"redact,omitempty"`
}

// DiagnosisPolicyStatus holds the observed state of a DiagnosisPolicy.
type DiagnosisPolicyStatus struct {
	// Conditions holds standard Kubernetes condition records.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// DiagnosisPolicy is the Schema for the diagnosispolicies API.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dp,scope=Namespaced
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.llmProvider`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DiagnosisPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DiagnosisPolicySpec   `json:"spec,omitempty"`
	Status DiagnosisPolicyStatus `json:"status,omitempty"`
}

// DiagnosisPolicyList contains a list of DiagnosisPolicy.
//
// +kubebuilder:object:root=true
type DiagnosisPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []DiagnosisPolicy `json:"items"`
}
