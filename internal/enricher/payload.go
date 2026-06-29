package enricher

import "github.com/bytedance/sonic"

// RedactedPlaceholder is the stable string substituted for redacted values.
const RedactedPlaceholder = "***REDACTED***"

// Snapshot is the enriched Kubernetes context collected for a diagnosis event.
// All free-text fields are redacted before serialization (SEC-001).
type Snapshot struct {
	EventUID   string `json:"eventUID"`
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	Namespace  string `json:"namespace"`
	ObjectKind string `json:"objectKind"`
	ObjectName string `json:"objectName"`

	PodContexts      []PodContext      `json:"podContexts,omitempty"`
	RelatedEvents    []EventSummary    `json:"relatedEvents,omitempty"`
	NodeConditions   []NodeCondition   `json:"nodeConditions,omitempty"`
	DeploymentStatus *DeploymentStatus `json:"deploymentStatus,omitempty"`
	ReplicaSetStatus *ReplicaSetStatus `json:"replicaSetStatus,omitempty"`

	// Partial records collection failures so callers can surface degraded context (REQ-004).
	Partial []string `json:"partial,omitempty"`
}

// PodContext holds per-pod log lines, env vars, and annotations.
type PodContext struct {
	PodName     string            `json:"podName"`
	NodeName    string            `json:"nodeName"`
	Phase       string            `json:"phase"`
	Annotations map[string]string `json:"annotations,omitempty"`
	EnvVars     []EnvVar          `json:"envVars,omitempty"`
	Logs        []PodLog          `json:"logs,omitempty"`
}

// EnvVar is a container env var; ValueFrom entries appear as RedactedPlaceholder.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PodLog holds tail-N log lines for one container.
type PodLog struct {
	ContainerName string `json:"containerName"`
	Lines         string `json:"lines"`
}

// EventSummary is a condensed view of a Kubernetes Event.
type EventSummary struct {
	Name    string `json:"name"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int32  `json:"count"`
}

// NodeCondition is one condition entry from a Node's status.
type NodeCondition struct {
	NodeName string `json:"nodeName"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

// DeploymentStatus captures replica counts and condition messages.
type DeploymentStatus struct {
	Name              string   `json:"name"`
	Replicas          int32    `json:"replicas"`
	ReadyReplicas     int32    `json:"readyReplicas"`
	AvailableReplicas int32    `json:"availableReplicas"`
	Conditions        []string `json:"conditions,omitempty"`
}

// ReplicaSetStatus captures replica counts and condition messages.
type ReplicaSetStatus struct {
	Name          string   `json:"name"`
	Replicas      int32    `json:"replicas"`
	ReadyReplicas int32    `json:"readyReplicas"`
	Conditions    []string `json:"conditions,omitempty"`
}

// EncodeSnapshot redacts then serializes. Redaction is enforced here so it cannot be bypassed (SEC-001).
// CON-003: uses sonic, not encoding/json.
func EncodeSnapshot(s *Snapshot) ([]byte, error) {
	RedactSnapshot(s)
	return sonic.Marshal(s)
}

// DecodeSnapshot deserializes a snapshot.
// CON-003: uses sonic, not encoding/json.
func DecodeSnapshot(b []byte) (*Snapshot, error) {
	var s Snapshot
	if err := sonic.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
