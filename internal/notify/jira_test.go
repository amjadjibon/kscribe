package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJiraNotify(t *testing.T) {
	var got jiraIssueRequest
	var user, pass string
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok = r.BasicAuth()
		if r.URL.Path != "/rest/api/3/issue" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, Email: "bot@x.dev", APIToken: "tok", ProjectKey: "OPS"}
	err := j.Notify(context.Background(), Notification{
		Phase: "Failed", Reason: "OOMKilling", Namespace: "prod", Object: "worker-1",
		Summary: "a & b", RootCause: "limits", Remediation: []string{"raise memory"},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !ok || user != "bot@x.dev" || pass != "tok" {
		t.Errorf("basic auth = %q/%q ok=%v", user, pass, ok)
	}
	if got.Fields.Project.Key != "OPS" {
		t.Errorf("project key = %q", got.Fields.Project.Key)
	}
	if got.Fields.Summary != "[kscribe] Failed: OOMKilling prod/worker-1" {
		t.Errorf("summary = %q", got.Fields.Summary)
	}
	if got.Fields.IssueType.Name != "Task" {
		t.Errorf("issuetype = %q", got.Fields.IssueType.Name)
	}
	text := got.Fields.Description.Content[0].Content[0].Text
	for _, want := range []string{"Summary: a & b", "Root cause: limits", "1. raise memory"} {
		if !strings.Contains(text, want) {
			t.Errorf("description missing %q:\n%s", want, text)
		}
	}
}

func TestJiraNotifyErrorTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("z", 2048)))
	}))
	defer srv.Close()

	j := &Jira{BaseURL: srv.URL, ProjectKey: "OPS"}
	err := j.Notify(context.Background(), Notification{})
	if err == nil || !strings.Contains(err.Error(), "jira error 400") {
		t.Fatalf("err = %v, want jira error 400", err)
	}
	if len(err.Error()) > 600 {
		t.Errorf("error not truncated: %d bytes", len(err.Error()))
	}
}
