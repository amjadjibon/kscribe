package notify

import (
	"context"
	"encoding/json"
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
