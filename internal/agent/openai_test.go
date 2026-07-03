package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

// TestOpenAIClient_Complete_SDKRoundTrip is the Phase 1 gate: it drives the
// SDK-backed Complete against a real httptest server and asserts the request is
// POSTed to <baseURL>/chat/completions with our messages/tools, and that the
// response (content, tool_calls, usage) is mapped back into our schema.
func TestOpenAIClient_Complete_SDKRoundTrip(t *testing.T) {
	var gotPath, gotAuth string
	var reqBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = sonic.Unmarshal(b, &reqBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id":"c1","object":"chat.completion","created":1,"model":"m",
			"choices":[{"index":0,"finish_reason":"tool_calls","message":{
				"role":"assistant","content":"hi",
				"tool_calls":[{"id":"tc1","type":"function","function":{"name":"get_pod_logs","arguments":"{\"pod\":\"p\"}"}}]
			}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`)
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL+"/v1beta/openai", "secret", "gpt-4o-mini")
	resp, err := c.Complete(context.Background(), Request{
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "why did the pod crash?"},
			// REQ-006: an assistant message carrying tool_calls must serialize via
			// the explicit param struct, followed by its tool-result message.
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID:       "tc1",
				Type:     "function",
				Function: FunctionCall{Name: "get_pod_logs", Arguments: `{"pod":"p"}`},
			}}},
			{Role: "tool", ToolCallID: "tc1", Name: "get_pod_logs", Content: "log line"},
		},
		Tools: []ToolDefinition{{
			Type: "function",
			Function: FunctionDef{
				Name:        "get_pod_logs",
				Description: "fetch pod logs",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Base-URL path must be preserved (no /v1 collapse) and auth forwarded.
	if gotPath != "/v1beta/openai/chat/completions" {
		t.Errorf("path = %q, want /v1beta/openai/chat/completions", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("auth = %q, want Bearer secret", gotAuth)
	}

	// Request body carries model, messages, and tools.
	if reqBody["model"] != "gpt-4o-mini" {
		t.Errorf("model = %v, want gpt-4o-mini", reqBody["model"])
	}
	msgs, ok := reqBody["messages"].([]any)
	if !ok || len(msgs) != 4 {
		t.Fatalf("messages = %v, want 4", reqBody["messages"])
	}
	// REQ-006: the assistant message (index 2) carries tool_calls with the
	// function name/arguments; the tool result (index 3) carries tool_call_id.
	asst, _ := msgs[2].(map[string]any)
	tcs, _ := asst["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("assistant tool_calls = %v, want 1", asst["tool_calls"])
	}
	tc0, _ := tcs[0].(map[string]any)
	fn, _ := tc0["function"].(map[string]any)
	if tc0["id"] != "tc1" || fn["name"] != "get_pod_logs" {
		t.Errorf("outgoing tool_call mapped wrong: %v", tc0)
	}
	if tool, _ := msgs[3].(map[string]any); tool["tool_call_id"] != "tc1" {
		t.Errorf("tool message tool_call_id = %v, want tc1", msgs[3])
	}
	if tools, ok := reqBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Errorf("tools = %v, want 1", reqBody["tools"])
	}

	// Response mapped back into our schema.
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	ch := resp.Choices[0]
	if ch.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", ch.FinishReason)
	}
	if ch.Message.Content != "hi" {
		t.Errorf("content = %q, want hi", ch.Message.Content)
	}
	if len(ch.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(ch.Message.ToolCalls))
	}
	tc := ch.Message.ToolCalls[0]
	if tc.ID != "tc1" || tc.Function.Name != "get_pod_logs" || !strings.Contains(tc.Function.Arguments, `"pod":"p"`) {
		t.Errorf("tool call mapped wrong: %+v", tc)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens = %d, want 15", resp.Usage.TotalTokens)
	}
}

// TestOpenAIClient_Complete_ErrorTruncation verifies provider errors are capped
// at 512 bytes so raw provider detail is not embedded in durable status (SEC-001).
func TestOpenAIClient_Complete_ErrorTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"`+strings.Repeat("x", 4000)+`"}}`)
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL+"/v1", "bad", "m")
	// No retries here would be ideal, but the SDK retries auth errors are not
	// retried; a 401 returns immediately.
	_, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("want error on 401")
	}
	if len(err.Error()) > 600 { // "provider error: " prefix + 512 cap
		t.Errorf("error not truncated: %d bytes", len(err.Error()))
	}
}
