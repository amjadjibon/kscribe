package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DiagnosisPhase is the lifecycle phase of a KscribeDiagnosis.
// +kubebuilder:validation:Enum=Pending;Diagnosing;Done;Partial;Failed
type DiagnosisPhase string

const (
	DiagnosisPhasePending    DiagnosisPhase = "Pending"
	DiagnosisPhaseDiagnosing DiagnosisPhase = "Diagnosing"
	DiagnosisPhaseDone       DiagnosisPhase = "Done"
	DiagnosisPhasePartial    DiagnosisPhase = "Partial"
	DiagnosisPhaseFailed     DiagnosisPhase = "Failed"
)

// Condition type constants for KscribeDiagnosis.
const (
	ConditionDiagnosing = "Diagnosing"
	ConditionDiagnosed  = "Diagnosed"
	ConditionPersisted  = "Persisted"
)

// KscribeDiagnosisSpec captures the source Kubernetes event and any
// policy/LLM overrides that were active when the diagnosis was triggered.
type KscribeDiagnosisSpec struct {
	// InvolvedObjectName is the name of the Kubernetes object the event referenced.
	InvolvedObjectName string `json:"involvedObjectName"`

	// InvolvedObjectNamespace is the namespace of the involved object.
	InvolvedObjectNamespace string `json:"involvedObjectNamespace"`

	// InvolvedObjectKind is the Kind of the involved object (e.g. Pod, Deployment).
	InvolvedObjectKind string `json:"involvedObjectKind"`

	// Reason is the Kubernetes event reason (e.g. BackOff, OOMKilling).
	Reason string `json:"reason"`

	// Message is the human-readable event message.
	Message string `json:"message"`

	// EventUID is the UID of the source Kubernetes Event.
	EventUID string `json:"eventUID"`

	// Count is the number of times the event occurred.
	// +optional
	Count int32 `json:"count,omitempty"`

	// FirstTimestamp is when the event was first recorded.
	// +optional
	FirstTimestamp *metav1.Time `json:"firstTimestamp,omitempty"`

	// LastTimestamp is when the event was last seen.
	// +optional
	LastTimestamp *metav1.Time `json:"lastTimestamp,omitempty"`

	// PolicyRef is the name of the DiagnosisPolicy that triggered this diagnosis.
	// +optional
	PolicyRef string `json:"policyRef,omitempty"`

	// LLMProvider overrides the operator-level LLM provider for this diagnosis.
	// +optional
	LLMProvider string `json:"llmProvider,omitempty"`

	// LLMModel overrides the operator-level LLM model for this diagnosis.
	// +optional
	LLMModel string `json:"llmModel,omitempty"`

	// LLMBaseURL overrides the operator-level LLM base URL for this diagnosis.
	// +optional
	LLMBaseURL string `json:"llmBaseURL,omitempty"`

	// MaxIterations overrides the maximum diagnosis loop depth.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	MaxIterations *int32 `json:"maxIterations,omitempty"`

	// Redact overrides whether sensitive data is scrubbed from prompts.
	// +optional
	Redact *bool `json:"redact,omitempty"`
}

// KscribeDiagnosisStatus holds the observed state of a KscribeDiagnosis.
type KscribeDiagnosisStatus struct {
	// Phase is the current lifecycle phase of this diagnosis.
	// +optional
	Phase DiagnosisPhase `json:"phase,omitempty"`

	// Conditions holds standard Kubernetes condition records.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// StartedAt is when the diagnosis loop began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the diagnosis loop finished (success or failure).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Summary is the human-readable RCA conclusion.
	// +optional
	Summary string `json:"summary,omitempty"`

	// RootCause is the structured root-cause classification.
	// +optional
	RootCause string `json:"rootCause,omitempty"`

	// LLMProvider records which LLM provider was used (SEC-002 audit).
	// +optional
	LLMProvider string `json:"llmProvider,omitempty"`

	// LLMModel records which LLM model was used (SEC-002 audit).
	// +optional
	LLMModel string `json:"llmModel,omitempty"`

	// TokensUsed records total tokens consumed by the diagnosis run (SEC-002 audit).
	// +optional
	TokensUsed int64 `json:"tokensUsed,omitempty"`

	// PromptRedacted indicates whether the prompt was redacted before sending (SEC-002 audit).
	// +optional
	PromptRedacted bool `json:"promptRedacted,omitempty"`

	// Persisted indicates whether the RCA result was written to the state DB.
	// +optional
	Persisted bool `json:"persisted,omitempty"`
}

// KscribeDiagnosis is the Schema for the kscribediagnoses API.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ksd
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.spec.reason`
// +kubebuilder:printcolumn:name="Object",type=string,JSONPath=`.spec.involvedObjectName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type KscribeDiagnosis struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KscribeDiagnosisSpec   `json:"spec,omitempty"`
	Status KscribeDiagnosisStatus `json:"status,omitempty"`
}

// KscribeDiagnosisList contains a list of KscribeDiagnosis.
//
// +kubebuilder:object:root=true
type KscribeDiagnosisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []KscribeDiagnosis `json:"items"`
}
