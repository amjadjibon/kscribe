package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer returns an httptest.Server that writes canned SSE lines then [DONE].
func sseServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		fmt.Fprintln(w, "data: [DONE]")
	}))
}

func TestCompleteStream_ParsesDeltas(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
	})
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "m")
	var deltas []string
	resp, err := c.CompleteStream(context.Background(), Request{}, func(d string) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(deltas) != 2 || deltas[0] != "Hel" || deltas[1] != "lo" {
		t.Errorf("deltas = %v, want [Hel lo]", deltas)
	}
	if resp.Choices[0].Message.Content != "Hello" {
		t.Errorf("content = %q, want Hello", resp.Choices[0].Message.Content)
	}
}

func TestCompleteStream_SendsTokenLimit(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &reqBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "m")
	_, err := c.CompleteStream(context.Background(), Request{MaxTokens: 123}, func(string) error {
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if reqBody["max_tokens"] != float64(123) {
		t.Errorf("max_tokens = %v, want 123", reqBody["max_tokens"])
	}
}

// fakeNonStreamingProvider satisfies Provider but not StreamingProvider.
type fakeNonStreamingProvider struct{ content string }

func (f *fakeNonStreamingProvider) Complete(_ context.Context, _ Request) (Response, error) {
	return Response{Choices: []Choice{{Message: Message{Role: "assistant", Content: f.content}}}}, nil
}

func TestStreamOrComplete_FallbackCallsDeltaOnce(t *testing.T) {
	p := &fakeNonStreamingProvider{content: "full answer"}
	var calls []string
	resp, err := StreamOrComplete(context.Background(), p, Request{}, func(d string) error {
		calls = append(calls, d)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamOrComplete: %v", err)
	}
	if len(calls) != 1 || calls[0] != "full answer" {
		t.Errorf("calls = %v, want [full answer]", calls)
	}
	if resp.Choices[0].Message.Content != "full answer" {
		t.Errorf("content = %q, want full answer", resp.Choices[0].Message.Content)
	}
}

func TestStreamOrComplete_UsesStreamingPath(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
	})
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "m")
	var deltas []string
	resp, err := StreamOrComplete(context.Background(), c, Request{}, func(d string) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamOrComplete: %v", err)
	}
	if len(deltas) != 1 || deltas[0] != "Hi" {
		t.Errorf("deltas = %v, want [Hi]", deltas)
	}
	if resp.Choices[0].Message.Content != "Hi" {
		t.Errorf("content = %q, want Hi", resp.Choices[0].Message.Content)
	}
}

// TestCompleteStream_NonSuccess asserts a non-2xx HTTP response returns a provider error.
func TestCompleteStream_NonSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "m")
	_, err := c.CompleteStream(context.Background(), Request{}, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if !strings.Contains(err.Error(), "provider error 429") {
		t.Errorf("err = %v, want provider error 429", err)
	}
}

// TestCompleteStream_MalformedChunkSkips asserts a malformed JSON data-line is
// skipped and the stream continues accumulating valid deltas. (bug: was returning error)
func TestCompleteStream_MalformedChunkSkips(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"content":"ok"}}]}`,
		`data: NOT VALID JSON`,
		`data: {"choices":[{"delta":{"content":"!"}}]}`,
	})
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "m")
	var deltas []string
	resp, err := c.CompleteStream(context.Background(), Request{}, func(d string) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(deltas) != 2 || deltas[0] != "ok" || deltas[1] != "!" {
		t.Errorf("deltas = %v, want [ok !]", deltas)
	}
	if resp.Choices[0].Message.Content != "ok!" {
		t.Errorf("content = %q, want ok!", resp.Choices[0].Message.Content)
	}
}

// TestCompleteStream_OnDeltaError asserts onDelta returning an error aborts the stream.
func TestCompleteStream_OnDeltaError(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"content":"token"}}]}`,
	})
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "m")
	wantErr := errors.New("sink full")
	_, err := c.CompleteStream(context.Background(), Request{}, func(string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

// TestStreamOrComplete_OnDeltaError_NonStreaming asserts onDelta error propagates
// through the non-streaming fallback path in StreamOrComplete.
func TestStreamOrComplete_OnDeltaError_NonStreaming(t *testing.T) {
	p := &fakeNonStreamingProvider{content: "some content"}
	wantErr := errors.New("write error")
	_, err := StreamOrComplete(context.Background(), p, Request{}, func(string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

// TestCompleteStream_RetriesTransientThenSucceeds asserts a 500 on the first
// connection attempt is retried and the stream succeeds on the second.
func TestCompleteStream_RetriesTransientThenSucceeds(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "", "m")
	resp, err := c.CompleteStream(context.Background(), Request{}, func(string) error { return nil })
	if err != nil {
		t.Fatalf("CompleteStream after retry: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("content = %q, want ok", resp.Choices[0].Message.Content)
	}
}
