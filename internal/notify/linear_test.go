package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinearNotify(t *testing.T) {
	var got linearRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true}}}`))
	}))
	defer srv.Close()

	l := &Linear{APIKey: "lin_test", TeamID: "team-1", BaseURL: srv.URL}
	err := l.Notify(context.Background(), Notification{
		Phase: "Failed", Reason: "OOMKilling", Namespace: "prod", Object: "worker-1",
		Summary: "a & b", RootCause: "limits", Remediation: []string{"raise memory"},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if auth != "lin_test" {
		t.Errorf("auth = %q, want raw key (no Bearer)", auth)
	}
	if got.Variables.Input.TeamID != "team-1" {
		t.Errorf("teamId = %q", got.Variables.Input.TeamID)
	}
	if got.Variables.Input.Title != "[kscribe] Failed: OOMKilling prod/worker-1" {
		t.Errorf("title = %q", got.Variables.Input.Title)
	}
	if !strings.Contains(got.Variables.Input.Description, "raise memory") {
		t.Errorf("description = %q", got.Variables.Input.Description)
	}
}

func TestLinearNotifyGraphQLErrorsIn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"invalid teamId"}]}`))
	}))
	defer srv.Close()

	l := &Linear{APIKey: "k", TeamID: "bad", BaseURL: srv.URL}
	err := l.Notify(context.Background(), Notification{})
	if err == nil || !strings.Contains(err.Error(), "invalid teamId") {
		t.Fatalf("err = %v, want graphql error surfaced", err)
	}
}

func TestLinearNotifyGraphQLErrorsOver512Bytes(t *testing.T) {
	// A validation error that echoes back a long input can push the error
	// envelope past the 512-byte cap used for the truncated message; the
	// parser must still see it as a failure (regression for MED-001).
	long := strings.Repeat("x", 700)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		body, _ := json.Marshal(map[string]any{
			"errors": []map[string]string{{"message": "invalid input: " + long}},
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	l := &Linear{APIKey: "k", TeamID: "bad", BaseURL: srv.URL}
	err := l.Notify(context.Background(), Notification{})
	if err == nil || !strings.Contains(err.Error(), "linear graphql error") {
		t.Fatalf("err = %v, want graphql error surfaced despite long payload", err)
	}
}

func TestLinearNotifyErrorTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("z", 2048)))
	}))
	defer srv.Close()

	l := &Linear{BaseURL: srv.URL}
	err := l.Notify(context.Background(), Notification{})
	if err == nil || !strings.Contains(err.Error(), "linear error 500") {
		t.Fatalf("err = %v, want linear error 500", err)
	}
	if len(err.Error()) > 600 {
		t.Errorf("error not truncated: %d bytes", len(err.Error()))
	}
}
