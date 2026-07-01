package agent_test

import (
	"context"
	"errors"
	"testing"

	kscribev1alpha1 "github.com/amjadjibon/kscribe/api/v1alpha1"
	"github.com/amjadjibon/kscribe/internal/agent"
)

// fakeProvider is a controllable Provider that returns pre-loaded responses in order.
type fakeProvider struct {
	responses []agent.Response
	errs      []error
	callIdx   int
}

func (f *fakeProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	i := f.callIdx
	f.callIdx++
	if i < len(f.errs) && f.errs[i] != nil {
		return agent.Response{}, f.errs[i]
	}
	if i < len(f.responses) {
		return f.responses[i], nil
	}
	return agent.Response{}, errors.New("fakeProvider: no more responses")
}

const validRCAJSON = `{"summary":"pod OOMKilled","rootCause":"memory limit too low","contributingFactors":["memory leak"],"remediationSteps":["increase limit"],"confidence":0.9}`

func validResp(tokens int) agent.Response {
	return agent.Response{
		Choices: []agent.Choice{{
			Message:      agent.Message{Role: "assistant", Content: validRCAJSON},
			FinishReason: "stop",
		}},
		Usage: agent.Usage{TotalTokens: tokens},
	}
}

func toolCallResp(tokens int) agent.Response {
	return agent.Response{
		Choices: []agent.Choice{{
			Message: agent.Message{
				Role: "assistant",
				ToolCalls: []agent.ToolCall{{
					ID:       "c1",
					Type:     "function",
					Function: agent.FunctionCall{Name: "get_pod_logs", Arguments: `{"namespace":"default","pod":"pod-1"}`},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: agent.Usage{TotalTokens: tokens},
	}
}

func TestDiagnosisAgent_Success(t *testing.T) {
	prov := &fakeProvider{responses: []agent.Response{validResp(100)}}
	ag := &agent.DiagnosisAgent{Provider: prov, MaxIter: 5}

	out := ag.Run(context.Background(), []byte(`{}`))

	if out.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("want Done, got %s: %s", out.Phase, out.RawError)
	}
	if out.RCA == nil || out.RCA.RootCause != "memory limit too low" {
		t.Fatalf("unexpected RCA: %+v", out.RCA)
	}
	if out.TokensUsed != 100 {
		t.Fatalf("want 100 tokens, got %d", out.TokensUsed)
	}
}

func TestDiagnosisAgent_JSONRepair(t *testing.T) {
	// First response: bad JSON. Repair response: valid JSON.
	prov := &fakeProvider{responses: []agent.Response{
		{
			Choices: []agent.Choice{{Message: agent.Message{Role: "assistant", Content: "not json at all"}}},
			Usage:   agent.Usage{TotalTokens: 50},
		},
		validResp(60),
	}}
	ag := &agent.DiagnosisAgent{Provider: prov, MaxIter: 5}

	out := ag.Run(context.Background(), []byte(`{}`))

	if out.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("want Done after JSON repair, got %s: %s", out.Phase, out.RawError)
	}
	if out.TokensUsed != 110 {
		t.Fatalf("want 110 tokens (50+60), got %d", out.TokensUsed)
	}
}

func TestDiagnosisAgent_MaxIterations(t *testing.T) {
	// Model always calls a tool, never produces a final RCA.
	responses := make([]agent.Response, 10)
	for i := range responses {
		responses[i] = toolCallResp(20)
	}
	prov := &fakeProvider{responses: responses}
	ag := &agent.DiagnosisAgent{Provider: prov, MaxIter: 3}

	out := ag.Run(context.Background(), []byte(`{}`))

	if out.Phase != kscribev1alpha1.DiagnosisPhasePartial {
		t.Fatalf("want Partial on max iterations, got %s", out.Phase)
	}
	if out.TokensUsed != 60 { // 3 iterations × 20 tokens
		t.Fatalf("want 60 tokens, got %d", out.TokensUsed)
	}
}

func TestDiagnosisAgent_ProviderError(t *testing.T) {
	prov := &fakeProvider{errs: []error{errors.New("network failure")}}
	ag := &agent.DiagnosisAgent{Provider: prov, MaxIter: 5}

	out := ag.Run(context.Background(), []byte(`{}`))

	if out.Phase != kscribev1alpha1.DiagnosisPhaseFailed {
		t.Fatalf("want Failed on provider error, got %s", out.Phase)
	}
	if out.RawError == "" {
		t.Fatal("want non-empty RawError")
	}
}

func TestDiagnosisAgent_TraceAndContextJSON(t *testing.T) {
	// One tool call, then RCA with reasoning — verifies Trace, Reasoning, ContextJSON.
	const rcaWithReasoning = `{"summary":"pod OOMKilled","rootCause":"memory limit too low","confidence":0.9,"reasoning":"OOM events in logs confirm the root cause"}`
	rcaResp := agent.Response{
		Choices: []agent.Choice{{
			Message:      agent.Message{Role: "assistant", Content: rcaWithReasoning},
			FinishReason: "stop",
		}},
		Usage: agent.Usage{TotalTokens: 50},
	}
	prov := &fakeProvider{responses: []agent.Response{toolCallResp(30), rcaResp}}
	ag := &agent.DiagnosisAgent{Provider: prov, MaxIter: 5}

	input := []byte(`{"namespace":"default"}`)
	out := ag.Run(context.Background(), input)

	if out.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("want Done, got %s: %s", out.Phase, out.RawError)
	}
	if len(out.Trace) != 1 {
		t.Fatalf("want 1 trace step, got %d", len(out.Trace))
	}
	if out.Trace[0].Tool != "get_pod_logs" {
		t.Errorf("trace tool = %q, want get_pod_logs", out.Trace[0].Tool)
	}
	if out.Reasoning != "OOM events in logs confirm the root cause" {
		t.Errorf("Reasoning = %q", out.Reasoning)
	}
	if string(out.ContextJSON) != string(input) {
		t.Errorf("ContextJSON = %q, want %q", out.ContextJSON, input)
	}
}

func TestDiagnosisAgent_ToolCallThenRCA(t *testing.T) {
	// One tool call round, then a final RCA.
	prov := &fakeProvider{responses: []agent.Response{
		toolCallResp(30),
		validResp(50),
	}}
	ag := &agent.DiagnosisAgent{Provider: prov, MaxIter: 5}

	out := ag.Run(context.Background(), []byte(`{}`))

	if out.Phase != kscribev1alpha1.DiagnosisPhaseDone {
		t.Fatalf("want Done after tool call + RCA, got %s", out.Phase)
	}
	if out.TokensUsed != 80 {
		t.Fatalf("want 80 tokens, got %d", out.TokensUsed)
	}
}
