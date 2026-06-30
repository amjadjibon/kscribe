package web_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amjadjibon/kscribe/internal/store"
	"github.com/amjadjibon/kscribe/internal/web"
)

// fakeStore implements web.StoreReader in memory.
type fakeStore struct {
	incidents map[string]*store.IncidentDetail // key: namespace/name
}

func (f *fakeStore) ListIncidents(_ context.Context, limit int) ([]store.Incident, error) {
	out := make([]store.Incident, 0, len(f.incidents))
	for _, d := range f.incidents {
		out = append(out, d.Incident)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) GetIncident(_ context.Context, namespace, name string) (*store.IncidentDetail, error) {
	d, ok := f.incidents[namespace+"/"+name]
	if !ok {
		return nil, &notFoundErr{namespace + "/" + name}
	}
	return d, nil
}

type notFoundErr struct{ id string }

func (e *notFoundErr) Error() string { return "not found: " + e.id }

func seedStore() *fakeStore {
	now := time.Now().UTC()
	completed := now.Add(-time.Minute)
	return &fakeStore{incidents: map[string]*store.IncidentDetail{
		"default/done-incident": {
			Incident: store.Incident{
				Namespace: "default", Name: "done-incident",
				InvolvedObjectKind: "Pod", InvolvedObjectName: "my-pod", InvolvedObjectNamespace: "default",
				Reason: "BackOff", Message: "back-off restarting failed container",
				Phase: "Done", StartedAt: &now, CompletedAt: &completed,
				LLMProvider: "openai", LLMModel: "gpt-4o-mini", TokensUsed: 512,
				CreatedAt: now, UpdatedAt: now,
			},
			Diagnoses: []store.Diagnosis{{
				ID: 1, Namespace: "default", Name: "done-incident",
				Summary: "Container OOM", RootCause: "memory limit too low",
				Remediation: "Increase memory limit to 512Mi", Confidence: 0.92,
				CreatedAt: now,
			}},
		},
		"default/partial-incident": {
			Incident: store.Incident{
				Namespace: "default", Name: "partial-incident",
				InvolvedObjectKind: "Deployment", InvolvedObjectName: "api", InvolvedObjectNamespace: "default",
				Reason: "Failed", Message: "partial diagnosis",
				Phase: "Partial", CreatedAt: now, UpdatedAt: now,
			},
			Diagnoses: nil,
		},
		"default/failed-incident": {
			Incident: store.Incident{
				Namespace: "default", Name: "failed-incident",
				InvolvedObjectKind: "Job", InvolvedObjectName: "batch", InvolvedObjectNamespace: "default",
				Reason: "FailedScheduling", Message: "no nodes available",
				Phase: "Failed", CreatedAt: now, UpdatedAt: now,
			},
			Diagnoses: nil,
		},
	}}
}

func newTestServer(t *testing.T) (*httptest.Server, *web.Broker) {
	t.Helper()
	broker := web.NewBroker()
	srv := web.New(seedStore(), broker)
	return httptest.NewServer(srv.Handler()), broker
}

func TestHealthz(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestList(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("want text/html, got %q", ct)
	}
}

func TestDetail(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	cases := []struct {
		path       string
		wantPhase  string
		wantInBody string
	}{
		{"/incidents/default/done-incident", "Done", "Container OOM"},
		{"/incidents/default/partial-incident", "Partial", "partial-incident"},
		{"/incidents/default/failed-incident", "Failed", "failed-incident"},
	}

	for _, tc := range cases {
		t.Run(tc.wantPhase, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: want 200, got %d", tc.path, resp.StatusCode)
			}
			var sb strings.Builder
			_, _ = io.Copy(&sb, resp.Body)
			body := sb.String()
			if !strings.Contains(body, tc.wantPhase) {
				t.Errorf("want phase %q in body", tc.wantPhase)
			}
			if !strings.Contains(body, tc.wantInBody) {
				t.Errorf("want %q in body", tc.wantInBody)
			}
		})
	}
}

func TestDetailNotFound(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/incidents/default/noexist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestStaticAssets(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	cases := []struct {
		path    string
		wantCT  string
	}{
		{"/static/css/app.css", "text/css"},
		{"/static/js/alpine.min.js", "text/javascript"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("want 200, got %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, tc.wantCT) {
				t.Fatalf("want Content-Type %q, got %q", tc.wantCT, ct)
			}
		})
	}
}

func TestListStatCards(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	if !strings.Contains(body, "stat-cards") {
		t.Error("want stat-cards section in list page")
	}
	// seedStore has 1 Done, 1 Partial, 1 Failed, 0 Diagnosing, 0 Pending.
	// Each stat card renders a stat-card-count span with the integer value.
	// Five phase cards total.
	if got := strings.Count(body, "stat-card-count"); got != 5 {
		t.Errorf("want 5 stat-card-count spans, got %d", got)
	}
	// Phases that should show count 1 (Done, Partial, Failed).
	for _, phase := range []string{"Done", "Partial", "Failed"} {
		if !strings.Contains(body, "stat-card-label\">"+phase+"<") {
			t.Errorf("want stat-card label %q in body", phase)
		}
	}
}

// TestDetailSanitization verifies that XSS payloads in LLM RCA fields are
// stripped from the rendered HTML before it reaches the browser. SEC-001.
func TestDetailSanitization(t *testing.T) {
	now := time.Now().UTC()
	xssStore := &fakeStore{incidents: map[string]*store.IncidentDetail{
		"default/xss-incident": {
			Incident: store.Incident{
				Namespace: "default", Name: "xss-incident",
				InvolvedObjectKind: "Pod", InvolvedObjectName: "pod", InvolvedObjectNamespace: "default",
				Reason: "Test", Message: "xss test",
				Phase: "Done", CreatedAt: now, UpdatedAt: now,
			},
			Diagnoses: []store.Diagnosis{{
				ID:          2,
				Namespace:   "default",
				Name:        "xss-incident",
				Summary:     `<script>alert(1)</script>`,
				RootCause:   `<img src=x onerror=alert(1)>`,
				Remediation: "clean fix",
				Confidence:  0.5,
				CreatedAt:   now,
			}},
		},
	}}
	broker := web.NewBroker()
	srv := web.New(xssStore, broker)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/incidents/default/xss-incident")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// Layout includes legitimate <script> tags; check for the injected payload pattern.
	if strings.Contains(body, "<script>alert") {
		t.Error("SEC-001: injected <script>alert payload found in rendered detail page")
	}
	if strings.Contains(body, "onerror=") {
		t.Error("SEC-001: onerror= attribute found in rendered detail page")
	}
}

// TestDetailSSEAttribute verifies the SSE live-status block is present in the
// rendered detail page with sse-connect and the incident phase. REQ-006.
func TestDetailSSEAttribute(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/incidents/default/done-incident")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	if !strings.Contains(body, "sse-connect") {
		t.Error("REQ-006: sse-connect attribute missing from detail page")
	}
	if !strings.Contains(body, "Done") {
		t.Error("REQ-006: phase 'Done' not found in detail page")
	}
}

func TestSSE(t *testing.T) {
	ts, broker := newTestServer(t)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/incidents/default/done-incident/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("want text/event-stream, got %q", ct)
	}

	// publish an event after connection is established
	go func() {
		time.Sleep(50 * time.Millisecond)
		broker.Publish("default/done-incident", web.Event{HTML: "<span>Done</span>"})
	}()

	// read until we see a data: line or context expires
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			found = true
			cancel() // stop the stream
			break
		}
	}
	if !found {
		t.Fatal("no SSE data: frame received")
	}
}
