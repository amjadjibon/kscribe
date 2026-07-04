package templates

import (
	"encoding/json"
	"github.com/amjadjibon/kscribe/internal/enricher"
	"github.com/amjadjibon/kscribe/internal/store"
)

// TraceStep is one tool-call in a diagnosis trace.
// Decoded with stdlib encoding/json.
type TraceStep struct {
	Tool   string `json:"tool"`
	Args   any    `json:"args"`
	Result any    `json:"result"`
}

// DiagnosisView wraps a store.Diagnosis with decoded context and trace.
type DiagnosisView struct {
	store.Diagnosis
	Snapshot   *enricher.Snapshot // nil when ContextJSON is empty/invalid
	TraceSteps []TraceStep        // empty when TraceJSON is empty/invalid
}

// IncidentDetailView wraps IncidentDetail with decoded diagnosis views and chat history.
type IncidentDetailView struct {
	*store.IncidentDetail
	DiagnosisViews []DiagnosisView
	ChatMessages   []store.ChatMessage
}

// BuildDetailView decodes context_json and trace_json for each diagnosis.
// JSON via stdlib encoding/json.
func BuildDetailView(d *store.IncidentDetail, msgs []store.ChatMessage) *IncidentDetailView {
	v := &IncidentDetailView{IncidentDetail: d, ChatMessages: msgs}
	for _, diag := range d.Diagnoses {
		dv := DiagnosisView{Diagnosis: diag}
		if len(diag.ContextJSON) > 0 {
			dv.Snapshot, _ = enricher.DecodeSnapshot(diag.ContextJSON)
		}
		if len(diag.TraceJSON) > 0 {
			_ = json.Unmarshal(diag.TraceJSON, &dv.TraceSteps)
		}
		v.DiagnosisViews = append(v.DiagnosisViews, dv)
	}
	return v
}

// marshalJSON returns a compact JSON string for display; falls back to "?" on error.
// JSON via stdlib encoding/json.
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}
