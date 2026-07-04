package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCollectorsRegistered exercises each collector once and asserts it emits
// a series — fails if a collector is renamed, unregistered, or mislabelled.
func TestCollectorsRegistered(t *testing.T) {
	DiagnosesTotal.WithLabelValues("done").Inc()
	LLMTokensTotal.WithLabelValues("openai", "gpt-4o-mini").Add(42)
	LLMRequestSeconds.WithLabelValues("openai").Observe(1.5)
	NotificationsTotal.WithLabelValues("sent").Inc()

	if n := testutil.CollectAndCount(DiagnosesTotal, "kscribe_diagnoses_total"); n != 1 {
		t.Errorf("kscribe_diagnoses_total series = %d, want 1", n)
	}
	if n := testutil.CollectAndCount(LLMTokensTotal, "kscribe_llm_tokens_total"); n != 1 {
		t.Errorf("kscribe_llm_tokens_total series = %d, want 1", n)
	}
	if n := testutil.CollectAndCount(LLMRequestSeconds, "kscribe_llm_request_seconds"); n != 1 {
		t.Errorf("kscribe_llm_request_seconds series = %d, want 1", n)
	}
	if n := testutil.CollectAndCount(NotificationsTotal, "kscribe_notifications_total"); n != 1 {
		t.Errorf("kscribe_notifications_total series = %d, want 1", n)
	}
	if v := testutil.ToFloat64(LLMTokensTotal.WithLabelValues("openai", "gpt-4o-mini")); v != 42 {
		t.Errorf("token counter = %v, want 42", v)
	}
}
