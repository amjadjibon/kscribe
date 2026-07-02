package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
