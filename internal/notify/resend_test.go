package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	var got emailRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := &Resend{APIKey: "re_test", From: "kscribe@x.dev", To: []string{"oncall@x.dev"}, BaseURL: srv.URL}
	if err := r.Send(context.Background(), "subj", "<b>hi</b>"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if auth != "Bearer re_test" {
		t.Errorf("auth = %q, want Bearer re_test", auth)
	}
	if got.From != "kscribe@x.dev" || len(got.To) != 1 || got.To[0] != "oncall@x.dev" ||
		got.Subject != "subj" || got.HTML != "<b>hi</b>" {
		t.Errorf("body = %+v", got)
	}
}

func TestSendErrorTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer srv.Close()

	r := &Resend{BaseURL: srv.URL}
	err := r.Send(context.Background(), "s", "h")
	if err == nil {
		t.Fatal("want error on 422")
	}
	if !strings.Contains(err.Error(), "resend error 422") {
		t.Errorf("err = %v, want status in message", err)
	}
	if len(err.Error()) > 600 {
		t.Errorf("error not truncated: %d bytes", len(err.Error()))
	}
}

func TestHTMLEscapes(t *testing.T) {
	out := HTML("Done", "BackOff", "prod", "api-7f", `<script>alert(1)</script>`, "bad & wrong", []string{"fix <it>"})
	if strings.Contains(out, "<script>") || strings.Contains(out, "<it>") {
		t.Fatalf("unescaped input in output: %s", out)
	}
	for _, want := range []string{"&lt;script&gt;", "bad &amp; wrong", "fix &lt;it&gt;", "prod/api-7f"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output", want)
		}
	}
}

func TestSubject(t *testing.T) {
	got := Subject("Failed", "OOMKilling", "prod", "worker-1")
	if got != "[kscribe] Failed: OOMKilling prod/worker-1" {
		t.Errorf("Subject = %q", got)
	}
}

func TestSlackNotify(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Slack{WebhookURL: srv.URL}
	err := s.Notify(context.Background(), Notification{
		Phase: "Failed", Reason: "OOMKilling", Namespace: "prod", Object: "worker-1",
		Summary: "a <b> & c", RootCause: "limits", Remediation: []string{"raise memory"},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	text := got["text"]
	for _, want := range []string{"*[kscribe] Failed: OOMKilling prod/worker-1*", "a &lt;b&gt; &amp; c", "1. raise memory", "*Root cause:* limits"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<b>") {
		t.Error("unescaped angle brackets in slack text")
	}
}

func TestSlackNotifyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(strings.Repeat("y", 2048)))
	}))
	defer srv.Close()

	s := &Slack{WebhookURL: srv.URL}
	err := s.Notify(context.Background(), Notification{})
	if err == nil || !strings.Contains(err.Error(), "slack error 404") {
		t.Fatalf("err = %v, want slack error 404", err)
	}
	if len(err.Error()) > 600 {
		t.Errorf("error not truncated: %d bytes", len(err.Error()))
	}
}

// failNotifier always errors; used to prove Multi keeps going.
type failNotifier struct{ called bool }

func (f *failNotifier) Notify(context.Context, Notification) error {
	f.called = true
	return errors.New("boom")
}

type okNotifier struct{ called bool }

func (o *okNotifier) Notify(context.Context, Notification) error {
	o.called = true
	return nil
}

func TestMultiCallsAllDespiteFailure(t *testing.T) {
	f, o := &failNotifier{}, &okNotifier{}
	err := Multi(f, o).Notify(context.Background(), Notification{})
	if !f.called || !o.called {
		t.Fatalf("all notifiers must be called: fail=%v ok=%v", f.called, o.called)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("joined error missing failure: %v", err)
	}
}
