package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveBaseURL(t *testing.T) {
	cases := []struct{ provider, configured, want string }{
		{"openai", "", ""},
		{"google", "", GeminiBaseURL},
		{"gemini", "", GeminiBaseURL},
		{"GEMINI", "", GeminiBaseURL},
		{"zai", "", ZAIBaseURL},
		{"z.ai", "", ZAIBaseURL},
		{"glm", "", ZAIBaseURL},
		{"google", "https://custom/v1", "https://custom/v1"}, // explicit wins
		{"", "", ""},
	}
	for _, c := range cases {
		if got := ResolveBaseURL(c.provider, c.configured); got != c.want {
			t.Errorf("ResolveBaseURL(%q,%q)=%q want %q", c.provider, c.configured, got, c.want)
		}
	}
}

// The client must POST to <baseURL>/chat/completions (no extra /v1), so a
// Gemini-style baseURL with a /v1beta/openai path segment works unchanged.
func TestOpenAIClient_Path(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL+"/v1beta/openai", "k", "gemini-2.0-flash")
	if _, err := c.Complete(context.Background(), Request{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1beta/openai/chat/completions" {
		t.Errorf("path = %q, want /v1beta/openai/chat/completions", gotPath)
	}
}
