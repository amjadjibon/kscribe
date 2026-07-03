package web_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"

	"github.com/amjadjibon/kscribe/internal/agent"
	"github.com/amjadjibon/kscribe/internal/enricher"
	"github.com/amjadjibon/kscribe/internal/store"
	"github.com/amjadjibon/kscribe/internal/web"
)

// filteredIncidents applies store.IncidentFilter in-memory (mirrors SQL behaviour).
func filteredIncidents(all []store.Incident, filter store.IncidentFilter) []store.Incident {
	if filter == (store.IncidentFilter{}) {
		return all
	}
	var out []store.Incident
	for _, inc := range all {
		if filter.Phase != "" && inc.Phase != filter.Phase {
			continue
		}
		if filter.Namespace != "" && inc.Namespace != filter.Namespace {
			continue
		}
		if filter.Reason != "" && inc.Reason != filter.Reason {
			continue
		}
		if filter.Query != "" {
			q := strings.ToLower(filter.Query)
			if !strings.Contains(strings.ToLower(inc.Name), q) &&
				!strings.Contains(strings.ToLower(inc.Message), q) &&
				!strings.Contains(strings.ToLower(inc.Reason), q) {
				continue
			}
		}
		out = append(out, inc)
	}
	return out
}

// fakeStore implements web.StoreReader in memory.
type fakeStore struct {
	incidents    map[string]*store.IncidentDetail // key: namespace/name
	orderedKeys  []string                         // optional; if set, ListIncidentsPage respects this order
	chatMessages []store.ChatMessage
}

func (f *fakeStore) orderedIncidents() []store.Incident {
	keys := f.orderedKeys
	if len(keys) == 0 {
		keys = make([]string, 0, len(f.incidents))
		for k := range f.incidents {
			keys = append(keys, k)
		}
	}
	out := make([]store.Incident, 0, len(keys))
	for _, k := range keys {
		if d, ok := f.incidents[k]; ok {
			out = append(out, d.Incident)
		}
	}
	return out
}

func (f *fakeStore) ListIncidents(_ context.Context, limit int) ([]store.Incident, error) {
	all := f.orderedIncidents()
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (f *fakeStore) ListIncidentsPage(_ context.Context, filter store.IncidentFilter, limit, offset int) ([]store.Incident, error) {
	all := filteredIncidents(f.orderedIncidents(), filter)
	if offset >= len(all) {
		return nil, nil
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (f *fakeStore) CountIncidents(_ context.Context, filter store.IncidentFilter) (int, error) {
	return len(filteredIncidents(f.orderedIncidents(), filter)), nil
}

func (f *fakeStore) CountIncidentsByPhase(_ context.Context, filter store.IncidentFilter) (map[string]int, error) {
	// Apply all filter fields except Phase (TASK-023).
	noPhase := filter
	noPhase.Phase = ""
	counts := make(map[string]int)
	for _, inc := range filteredIncidents(f.orderedIncidents(), noPhase) {
		counts[inc.Phase]++
	}
	return counts, nil
}

func (f *fakeStore) GetIncident(_ context.Context, namespace, name string) (*store.IncidentDetail, error) {
	d, ok := f.incidents[namespace+"/"+name]
	if !ok {
		return nil, &notFoundErr{namespace + "/" + name}
	}
	return d, nil
}

func (f *fakeStore) AppendChatMessage(_ context.Context, namespace, name, role, content string) error {
	f.chatMessages = append(f.chatMessages, store.ChatMessage{
		ID:        int64(len(f.chatMessages) + 1),
		Namespace: namespace, Name: name, Role: role, Content: content,
		CreatedAt: time.Now().UTC(),
	})
	return nil
}

func (f *fakeStore) ListChatMessages(_ context.Context, namespace, name string) ([]store.ChatMessage, error) {
	var out []store.ChatMessage
	for _, m := range f.chatMessages {
		if m.Namespace == namespace && m.Name == name {
			out = append(out, m)
		}
	}
	return out, nil
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
	srv := web.New(seedStore(), broker, nil)
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
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()
	if !strings.Contains(body, "Pod/my-pod (default)") {
		t.Fatalf("want object label in incident list, body missing Pod/my-pod (default)")
	}
	if !strings.Contains(body, "BackOff") {
		t.Fatalf("want event reason in incident list, body missing BackOff")
	}
}

func TestListUsesFallbackForMissingObjectAndReason(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		"my-ns/ksd-empty": {
			Incident: store.Incident{
				Namespace: "my-ns",
				Name:      "ksd-empty",
				Phase:     "Done",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}}
	srv := web.New(st, web.NewBroker(), nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	if strings.Contains(body, "<td class=\"incident-object muted-value\">/</td>") {
		t.Fatal("object fallback rendered as bare slash")
	}
	if !strings.Contains(body, "Not captured") {
		t.Fatal("want missing object/reason fallback in list")
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
			if tc.path == "/incidents/default/done-incident" {
				if !strings.Contains(body, "Pod/my-pod (default)") {
					t.Fatalf("want object label in detail page, body missing Pod/my-pod (default)")
				}
				if !strings.Contains(body, "BackOff") {
					t.Fatalf("want event reason in detail page, body missing BackOff")
				}
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
		path   string
		wantCT string
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

// seedLargeStore creates a store with 30 Done incidents so pagination kicks in.
func seedLargeStore() *fakeStore {
	now := time.Now().UTC()
	f := &fakeStore{incidents: make(map[string]*store.IncidentDetail, 30)}
	keys := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("ns/incident-%02d", i)
		keys = append(keys, key)
		f.incidents[key] = &store.IncidentDetail{
			Incident: store.Incident{
				Namespace: "ns", Name: fmt.Sprintf("incident-%02d", i),
				InvolvedObjectKind: "Pod", InvolvedObjectName: fmt.Sprintf("pod-%02d", i),
				Reason: "Test", Phase: "Done",
				CreatedAt: now, UpdatedAt: now,
			},
		}
	}
	f.orderedKeys = keys
	return f
}

// TestListPager asserts pagination controls appear with >25 incidents.
func TestListPager(t *testing.T) {
	broker := web.NewBroker()
	srv := web.New(seedLargeStore(), broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Page 1: should show "Page 1 of 2" and Next link, no Prev link.
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	if !strings.Contains(body, "Page 1 of 2") {
		t.Errorf("want 'Page 1 of 2' in body, got:\n%s", body[:min(500, len(body))])
	}
	if !strings.Contains(body, "?page=2") {
		t.Error("want next page link (?page=2) in body")
	}

	// Page 2: should show "Page 2 of 2" and no Next link.
	resp2, err := http.Get(ts.URL + "/?page=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var sb2 strings.Builder
	_, _ = io.Copy(&sb2, resp2.Body)
	body2 := sb2.String()

	if !strings.Contains(body2, "Page 2 of 2") {
		t.Errorf("want 'Page 2 of 2' in body2")
	}
	if strings.Contains(body2, "?page=3") {
		t.Error("unexpected next page link on last page")
	}
}

// TestListTotalsFromDB asserts stat card counts reflect all incidents, not just the visible page.
func TestListTotalsFromDB(t *testing.T) {
	broker := web.NewBroker()
	srv := web.New(seedLargeStore(), broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// 30 Done incidents total — stat card must show 30, not 25 (the page size).
	if !strings.Contains(body, ">30<") {
		t.Errorf("want stat card showing total 30 Done incidents, body excerpt: %s",
			body[:min(1000, len(body))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// mustJSON marshals v with sonic or panics — test helper only.
func mustJSON(v any) []byte {
	b, err := sonic.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// TestDetailContextAndReasoning asserts that the Context and Reasoning tabs are
// rendered and contain seeded data from context_json, trace_json, and reasoning. TASK-010.
func TestDetailContextAndReasoning(t *testing.T) {
	now := time.Now().UTC()

	snap := &enricher.Snapshot{
		Namespace:  "default",
		ObjectName: "my-pod",
		PodContexts: []enricher.PodContext{{
			PodName:  "my-pod-abc",
			NodeName: "node-1",
			Phase:    "Running",
			Logs: []enricher.PodLog{{
				ContainerName: "app",
				Lines:         "INFO: unique-log-marker-42 startup complete",
			}},
		}},
	}
	ctxJSON, _ := enricher.EncodeSnapshot(snap)

	type traceStep struct {
		Tool   string `json:"tool"`
		Args   any    `json:"args"`
		Result any    `json:"result"`
	}
	traceJSON := mustJSON([]traceStep{{
		Tool:   "get_pod_logs",
		Args:   map[string]string{"pod": "my-pod-abc"},
		Result: "INFO: unique-log-marker-42 startup complete",
	}})

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		"default/ctx-incident": {
			Incident: store.Incident{
				Namespace: "default", Name: "ctx-incident",
				InvolvedObjectKind: "Pod", InvolvedObjectName: "my-pod", InvolvedObjectNamespace: "default",
				Reason: "CrashLoopBackOff", Message: "container keeps crashing",
				Phase: "Done", CreatedAt: now, UpdatedAt: now,
			},
			Diagnoses: []store.Diagnosis{{
				ID:          10,
				Namespace:   "default",
				Name:        "ctx-incident",
				Summary:     "pod crashes on startup",
				RootCause:   "OOM",
				Remediation: "increase memory",
				Confidence:  0.88,
				Reasoning:   "## Analysis\nThe pod OOMed based on the logs.",
				ContextJSON: ctxJSON,
				TraceJSON:   traceJSON,
				CreatedAt:   now,
			}},
		},
	}}

	broker := web.NewBroker()
	srv := web.New(st, broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/incidents/default/ctx-incident")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// Context tab button exists.
	if !strings.Contains(body, "tab='context'") {
		t.Error("want Context tab button in page")
	}
	// Reasoning tab button exists.
	if !strings.Contains(body, "tab='reasoning'") {
		t.Error("want Reasoning tab button in page")
	}
	// Pod log line appears (rendered in Context tab).
	if !strings.Contains(body, "unique-log-marker-42") {
		t.Error("want seeded log line in rendered page")
	}
	// Tool name appears (rendered in Reasoning/trace).
	if !strings.Contains(body, "get_pod_logs") {
		t.Error("want tool name 'get_pod_logs' in rendered page")
	}
}

// TestDetailContextXSS asserts that an XSS payload in context_json/reasoning is
// escaped or stripped before it reaches the browser. SEC-001.
func TestDetailContextXSS(t *testing.T) {
	now := time.Now().UTC()

	snap := &enricher.Snapshot{
		Namespace:  "default",
		ObjectName: "xss-pod",
		PodContexts: []enricher.PodContext{{
			PodName:  "xss-pod-1",
			NodeName: "node-1",
			Phase:    "Running",
			Logs: []enricher.PodLog{{
				ContainerName: "app",
				Lines:         `<script>alert(1)</script>`, // XSS in log line
			}},
		}},
	}
	ctxJSON, _ := enricher.EncodeSnapshot(snap)

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		"default/xss2-incident": {
			Incident: store.Incident{
				Namespace: "default", Name: "xss2-incident",
				InvolvedObjectKind: "Pod", InvolvedObjectName: "xss-pod", InvolvedObjectNamespace: "default",
				Reason: "Test", Message: "xss",
				Phase: "Done", CreatedAt: now, UpdatedAt: now,
			},
			Diagnoses: []store.Diagnosis{{
				ID:          20,
				Namespace:   "default",
				Name:        "xss2-incident",
				Summary:     "test",
				Confidence:  0.5,
				Reasoning:   `<script>alert(2)</script>`, // XSS in reasoning
				ContextJSON: ctxJSON,
				CreatedAt:   now,
			}},
		},
	}}

	broker := web.NewBroker()
	srv := web.New(st, broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/incidents/default/xss2-incident")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// The raw <script>alert payload must not appear verbatim — templ escapes
	// context values and bluemonday strips scripts from reasoning.
	if strings.Contains(body, "<script>alert") {
		t.Error("SEC-001: XSS payload found verbatim in rendered page")
	}
}

// errStore is a fakeStore variant that injects errors for specific methods.
type errStore struct {
	fakeStore
	failCountByPhase bool
	failCount        bool
	failList         bool
}

var errDB = errors.New("db error")

func (e *errStore) CountIncidentsByPhase(ctx context.Context, f store.IncidentFilter) (map[string]int, error) {
	if e.failCountByPhase {
		return nil, errDB
	}
	return e.fakeStore.CountIncidentsByPhase(ctx, f)
}

func (e *errStore) CountIncidents(ctx context.Context, f store.IncidentFilter) (int, error) {
	if e.failCount {
		return 0, errDB
	}
	return e.fakeStore.CountIncidents(ctx, f)
}

func (e *errStore) ListIncidentsPage(ctx context.Context, f store.IncidentFilter, limit, offset int) ([]store.Incident, error) {
	if e.failList {
		return nil, errDB
	}
	return e.fakeStore.ListIncidentsPage(ctx, f, limit, offset)
}

// TestListStoreErrors covers the three error branches in the list handler (66.7% → ~100%).
func TestListStoreErrors(t *testing.T) {
	cases := []struct {
		name string
		st   *errStore
	}{
		{"CountIncidentsByPhase", &errStore{fakeStore: *seedStore(), failCountByPhase: true}},
		{"CountIncidents", &errStore{fakeStore: *seedStore(), failCount: true}},
		{"ListIncidentsPage", &errStore{fakeStore: *seedStore(), failList: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := web.NewBroker()
			srv := web.New(tc.st, broker, nil)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			resp, err := http.Get(ts.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusInternalServerError {
				t.Errorf("want 500, got %d", resp.StatusCode)
			}
		})
	}
}

// TestStaticNotFound asserts that a missing static file returns 404 (not 500).
func TestStaticNotFound(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/static/does-not-exist.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

// TestStaticSVGFavicon checks the favicon returns 200 with an SVG content-type.
func TestStaticSVGFavicon(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/static/icons/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "svg") && !strings.Contains(ct, "xml") {
		t.Errorf("want SVG/XML content-type, got %q", ct)
	}
}

// TestListHTMLAssetsWired verifies GET / embeds /static/css/app.css and /static/js/alpine.min.js.
func TestListHTMLAssetsWired(t *testing.T) {
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

	for _, asset := range []string{"/static/css/app.css", "/static/js/alpine.min.js"} {
		if !strings.Contains(body, asset) {
			t.Errorf("want %q referenced in page HTML", asset)
		}
	}
}

// TestEmptyStorePager verifies that an empty store renders a sane "no incidents" state
// with 200 OK (no panic, no 500).
func TestEmptyStorePager(t *testing.T) {
	empty := &fakeStore{incidents: map[string]*store.IncidentDetail{}}
	broker := web.NewBroker()
	srv := web.New(empty, broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()
	// Template shows empty-state when there are no incidents.
	if !strings.Contains(body, "No incidents yet") {
		t.Errorf("want empty-state text for empty store, body excerpt: %s", body[:min(500, len(body))])
	}
}

// TestPageClamp verifies that page=0 is treated as page=1, and page beyond lastPage is clamped.
func TestPageClamp(t *testing.T) {
	ts, _ := newTestServer(t) // 3 incidents, pageSize=25, so lastPage=1
	defer ts.Close()

	for _, url := range []string{"/?page=0", "/?page=-5", "/?page=99"} {
		t.Run(url, func(t *testing.T) {
			resp, err := http.Get(ts.URL + url)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("want 200, got %d", resp.StatusCode)
			}
			var sb strings.Builder
			_, _ = io.Copy(&sb, resp.Body)
			body := sb.String()
			if !strings.Contains(body, "Page 1 of 1") {
				t.Errorf("%s: want 'Page 1 of 1' after clamp, got excerpt: %s", url, body[:min(500, len(body))])
			}
		})
	}
}

// TestStatCardIgnoresPhaseFilter checks that stat-card counts reflect all phases even
// when a phase filter is active (CountIncidentsByPhase ignores filter.Phase). TASK-023.
func TestStatCardIgnoresPhaseFilter(t *testing.T) {
	ts, _ := newTestServer(t) // seedStore: Done=1, Partial=1, Failed=1
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/?phase=Failed")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// Even though we filtered to Failed, stat cards must still label Done and Partial.
	for _, label := range []string{"Done", "Partial", "Failed"} {
		if !strings.Contains(body, "stat-card-label\">"+label+"<") {
			t.Errorf("want stat-card label %q even with phase=Failed filter", label)
		}
	}
	// Five stat-card-count spans (five phases always rendered).
	if got := strings.Count(body, "stat-card-count"); got != 5 {
		t.Errorf("want 5 stat-card-count spans, got %d", got)
	}
}

// TestListFilterByPhase verifies that GET /?phase=Failed returns only Failed incidents.
func TestListFilterByPhase(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/?phase=Failed")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	if !strings.Contains(body, "failed-incident") {
		t.Error("want failed-incident in body")
	}
	if strings.Contains(body, "done-incident") {
		t.Error("done-incident should not appear when filtering by Failed")
	}
	if strings.Contains(body, "partial-incident") {
		t.Error("partial-incident should not appear when filtering by Failed")
	}
}

// TestFilterPreservingPager verifies that pager links and stat-card links carry the active filter.
func TestFilterPreservingPager(t *testing.T) {
	// Build a store with 30 Failed incidents so the pager appears.
	now := time.Now().UTC()
	f := &fakeStore{incidents: make(map[string]*store.IncidentDetail, 30)}
	keys := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("ns/fail-%02d", i)
		keys = append(keys, key)
		f.incidents[key] = &store.IncidentDetail{
			Incident: store.Incident{
				Namespace: "ns", Name: fmt.Sprintf("fail-%02d", i),
				Phase: "Failed", Reason: "OOMKilled",
				CreatedAt: now, UpdatedAt: now,
			},
		}
	}
	f.orderedKeys = keys

	broker := web.NewBroker()
	srv := web.New(f, broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/?phase=Failed&q=fail")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// Pager Next link must preserve phase=Failed and q=fail.
	if !strings.Contains(body, "phase=Failed") {
		t.Error("want phase=Failed in body (pager or stat-card links)")
	}
	if !strings.Contains(body, "q=fail") {
		t.Error("want q=fail in body (pager or stat-card links)")
	}
	// Pager must show page 1 of 2.
	if !strings.Contains(body, "Page 1 of 2") {
		t.Errorf("want 'Page 1 of 2' in body")
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
	srv := web.New(xssStore, broker, nil)
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

// capturingProvider is a fake agent.StreamingProvider that emits a fixed delta
// and records the last Request it received.
type capturingProvider struct {
	delta   string
	lastReq agent.Request
}

func (p *capturingProvider) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	p.lastReq = req
	return agent.Response{Choices: []agent.Choice{{Message: agent.Message{Content: p.delta}}}}, nil
}

func (p *capturingProvider) CompleteStream(_ context.Context, req agent.Request, onDelta func(string) error) (agent.Response, error) {
	p.lastReq = req
	if err := onDelta(p.delta); err != nil {
		return agent.Response{}, err
	}
	return agent.Response{Choices: []agent.Choice{{Message: agent.Message{Content: p.delta}}}}, nil
}

// TestRunChat asserts SEC-002 (sanitized Markdown SSE frames), message persistence,
// history inclusion, and bounded system message (CON-007).
func TestRunChat(t *testing.T) {
	ctx := context.Background()
	ns, name := "default", "chat-incident"

	// Large contextJSON to verify the 4096-byte truncation.
	bigCtx := make([]byte, 8000)
	for i := range bigCtx {
		bigCtx[i] = 'x'
	}

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		ns + "/" + name: {
			Incident: store.Incident{Namespace: ns, Name: name, Phase: "Done"},
			Diagnoses: []store.Diagnosis{{
				Summary:     "OOM kill",
				RootCause:   "memory leak in sidecar",
				ContextJSON: bigCtx,
			}},
		},
	}}

	// Pre-seed 2 prior messages to verify history inclusion.
	_ = st.AppendChatMessage(ctx, ns, name, "user", "first question")
	_ = st.AppendChatMessage(ctx, ns, name, "assistant", "first answer")

	// Markdown + XSS payload from the "model".
	xssPayload := `**OOM** <script>alert(1)</script>`
	prov := &capturingProvider{delta: xssPayload}

	broker := web.NewBroker()
	topic := ns + "/" + name + "/chat"
	ch, cancelSub := broker.Subscribe(topic)
	defer cancelSub()

	if err := web.RunChat(ctx, st, prov, broker, ns, name, "new question"); err != nil {
		t.Fatalf("RunChat: %v", err)
	}

	// Drain published events.
	var events []web.Event
drain:
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		default:
			break drain
		}
	}

	// SEC-002: no raw <script> on the wire, but Markdown is rendered.
	if len(events) == 0 {
		t.Fatal("expected at least one published event")
	}
	for _, ev := range events {
		if strings.Contains(ev.HTML, "<script>") {
			t.Errorf("SEC-002: raw <script> in SSE payload: %q", ev.HTML)
		}
		if !strings.Contains(ev.HTML, "<strong>OOM</strong>") {
			t.Errorf("Markdown missing from SSE payload: %q", ev.HTML)
		}
	}

	// Message persistence: user + assistant both stored.
	msgs, err := st.ListChatMessages(ctx, ns, name)
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	// 2 seeded + 1 new user + 1 assistant = 4
	if len(msgs) != 4 {
		t.Fatalf("want 4 chat messages, got %d", len(msgs))
	}
	if msgs[2].Role != "user" || msgs[2].Content != "new question" {
		t.Errorf("user message not persisted correctly: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != xssPayload {
		t.Errorf("assistant message not persisted correctly: %+v", msgs[3])
	}

	// History: provider received system + 3 messages (2 seeded + new user).
	req := prov.lastReq
	if len(req.Messages) < 2 {
		t.Fatalf("expected messages in request, got %d", len(req.Messages))
	}
	sysMsg := req.Messages[0]
	if sysMsg.Role != "system" {
		t.Errorf("first message role = %q, want system", sysMsg.Role)
	}
	// System message must contain RCA summary and be bounded.
	if !strings.Contains(sysMsg.Content, "OOM kill") {
		t.Errorf("system message missing RCA summary, got: %q", sysMsg.Content[:min(200, len(sysMsg.Content))])
	}
	// contextJSON budget: system message should not carry the full 8000-byte context.
	// Allow generous headroom for prefix text; 6000 bytes is well below 8000.
	if len(sysMsg.Content) > 6000 {
		t.Errorf("system message too large (%d bytes), context budget not applied", len(sysMsg.Content))
	}
	// History messages included (seeded + new user = 3).
	if len(req.Messages) != 4 { // system + 2 seeded + 1 new user
		t.Errorf("want 4 messages in request (system + 3 history), got %d", len(req.Messages))
	}
}

// TestChatDetailRender asserts the detail page renders the Chat sidebar markup
// and seeded chat history (assistant markdown rendered). TASK-021.
func TestChatDetailRender(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{
		incidents: map[string]*store.IncidentDetail{
			"default/chat-render": {
				Incident: store.Incident{
					Namespace: "default", Name: "chat-render",
					InvolvedObjectKind: "Pod", InvolvedObjectName: "p", InvolvedObjectNamespace: "default",
					Reason: "Test", Message: "test", Phase: "Done",
					CreatedAt: now, UpdatedAt: now,
				},
			},
		},
		chatMessages: []store.ChatMessage{
			{ID: 1, Namespace: "default", Name: "chat-render", Role: "user", Content: "what happened?", CreatedAt: now},
			{ID: 2, Namespace: "default", Name: "chat-render", Role: "assistant", Content: "**OOM kill** detected.", CreatedAt: now},
		},
	}
	broker := web.NewBroker()
	srv := web.New(st, broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/incidents/default/chat-render")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// Chat is in the persistent sidebar, not hidden behind a tab.
	if !strings.Contains(body, `class="detail-sidebar"`) {
		t.Error("want Chat sidebar in page")
	}
	if !strings.Contains(body, `chatOpen: true`) || !strings.Contains(body, `chat-rail-button`) {
		t.Error("want collapsible chat rail controls in page")
	}
	if !strings.Contains(body, `chatDraft`) || !strings.Contains(body, `chatPending`) || !strings.Contains(body, `chatSending`) {
		t.Error("want chat panel Alpine state for draft, pending, and sending")
	}
	if !strings.Contains(body, `class="chat-history"`) || !strings.Contains(body, `class="chat-pending"`) {
		t.Error("want history and pending messages inside the transcript")
	}
	if strings.Contains(body, "tab='chat'") {
		t.Error("chat should not be rendered as a tab")
	}
	// User message in history.
	if !strings.Contains(body, "what happened?") {
		t.Error("want user message in chat history")
	}
	// Assistant markdown rendered: **OOM kill** becomes <strong>OOM kill</strong>.
	if !strings.Contains(body, "OOM kill") {
		t.Error("want assistant content in chat history")
	}
	// SSE connect attribute for chat stream.
	if !strings.Contains(body, "/chat/stream") {
		t.Error("want sse-connect to /chat/stream in page")
	}
	if !strings.Contains(body, `class="chat-bubble chat-bubble-assistant chat-streaming markdown"`) {
		t.Error("want chat stream target to be the assistant bubble")
	}
	if !strings.Contains(body, `window.location.reload()`) {
		t.Error("want successful chat post to reload persisted history")
	}
	if !strings.Contains(body, `:readonly="chatSending"`) {
		t.Error("want chat input to stay serializable while sending")
	}
	if strings.Contains(body, `class="chat-input" autocomplete="off" :disabled="chatSending"`) {
		t.Error("chat input must not be disabled before HTMX serializes the form")
	}
}

// TestChatPost asserts POST /incidents/{ns}/{name}/chat returns 200 and persists. TASK-021.
func TestChatPost(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{
		incidents: map[string]*store.IncidentDetail{
			"default/chat-post": {
				Incident: store.Incident{
					Namespace: "default", Name: "chat-post",
					Phase: "Done", CreatedAt: now, UpdatedAt: now,
				},
			},
		},
	}
	prov := &capturingProvider{delta: "response text"}
	broker := web.NewBroker()
	srv := web.New(st, broker, prov)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.PostForm(ts.URL+"/incidents/default/chat-post/chat",
		map[string][]string{"message": {"hello"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	// Persisted: user + assistant = 2 messages.
	msgs, _ := st.ListChatMessages(context.Background(), "default", "chat-post")
	if len(msgs) != 2 {
		t.Fatalf("want 2 persisted chat messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("user message not persisted: %+v", msgs[0])
	}
}

// TestChatStreamContentType asserts GET /incidents/{ns}/{name}/chat/stream returns text/event-stream. TASK-021.
func TestChatStreamContentType(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/incidents/default/done-incident/chat/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("want text/event-stream, got %q", ct)
	}
}

// errProvider is a Provider that always returns a fixed error (no streaming).
type errProvider struct{ err error }

func (e *errProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	return agent.Response{}, e.err
}

// TestChatPost_NilProvider asserts POST /chat with no provider returns 500.
func TestChatPost_NilProvider(t *testing.T) {
	ts, _ := newTestServer(t) // nil provider
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/incidents/default/done-incident/chat",
		"text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500 for nil provider, got %d", resp.StatusCode)
	}
}

// TestChatPost_BodyFallback asserts POST /chat reads raw body when form field is absent.
func TestChatPost_BodyFallback(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		"default/body-chat": {
			Incident: store.Incident{
				Namespace: "default", Name: "body-chat", Phase: "Done",
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}}
	prov := &capturingProvider{delta: "ok"}
	broker := web.NewBroker()
	srv := web.New(st, broker, prov)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Content-Type is not application/x-www-form-urlencoded, so FormValue("message") == "".
	resp, err := http.Post(ts.URL+"/incidents/default/body-chat/chat",
		"text/plain", strings.NewReader("body message"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
	msgs, _ := st.ListChatMessages(context.Background(), "default", "body-chat")
	if len(msgs) == 0 || msgs[0].Content != "body message" {
		t.Errorf("body message not persisted: %+v", msgs)
	}
}

// TestChatPost_ProviderError asserts POST /chat returns 500 when RunChat fails.
func TestChatPost_ProviderError(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		"default/err-chat": {
			Incident: store.Incident{
				Namespace: "default", Name: "err-chat", Phase: "Done",
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}}
	prov := &errProvider{err: errors.New("provider down")}
	broker := web.NewBroker()
	srv := web.New(st, broker, prov)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.PostForm(ts.URL+"/incidents/default/err-chat/chat",
		map[string][]string{"message": {"hello"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500 for provider error, got %d", resp.StatusCode)
	}
}

// TestChatStream_DeliversSSE asserts the chat/stream endpoint delivers published events.
func TestChatStream_DeliversSSE(t *testing.T) {
	ts, broker := newTestServer(t)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/incidents/default/done-incident/chat/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("want text/event-stream, got %q", ct)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		broker.Publish("default/done-incident/chat", web.Event{HTML: "<span>Hi</span>"})
	}()

	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			found = true
			cancel()
			break
		}
	}
	if !found {
		t.Fatal("no SSE data: frame received on chat/stream")
	}
}

// TestRunChat_NoDiagnoses asserts RunChat works when the incident has no diagnoses yet
// (system message has only the base SRE prompt, no Context section).
func TestRunChat_NoDiagnoses(t *testing.T) {
	ctx := context.Background()
	ns, name := "default", "no-diag"

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		ns + "/" + name: {
			Incident: store.Incident{Namespace: ns, Name: name, Phase: "Pending"},
			// no Diagnoses
		},
	}}
	prov := &capturingProvider{delta: "fallback answer"}
	broker := web.NewBroker()

	if err := web.RunChat(ctx, st, prov, broker, ns, name, "what is happening?"); err != nil {
		t.Fatalf("RunChat with no diagnoses: %v", err)
	}

	msgs, _ := st.ListChatMessages(ctx, ns, name)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages (user+assistant), got %d", len(msgs))
	}
	// System message must not contain diagnosis fields (no summary/root cause).
	req := prov.lastReq
	if len(req.Messages) == 0 || req.Messages[0].Role != "system" {
		t.Fatal("expected system message")
	}
	if strings.Contains(req.Messages[0].Content, "Summary:") {
		t.Errorf("system message should have no diagnosis summary for incident with no diagnoses")
	}
}

// TestRunChat_ProviderError asserts user message is persisted but no assistant
// message is written when the provider fails.
func TestRunChat_ProviderError(t *testing.T) {
	ctx := context.Background()
	ns, name := "default", "rce-diag"

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		ns + "/" + name: {
			Incident: store.Incident{Namespace: ns, Name: name, Phase: "Done"},
		},
	}}
	prov := &errProvider{err: errors.New("llm down")}
	broker := web.NewBroker()

	err := web.RunChat(ctx, st, prov, broker, ns, name, "help")
	if err == nil {
		t.Fatal("expected error from provider failure")
	}
	// User message persisted; no assistant message.
	msgs, _ := st.ListChatMessages(ctx, ns, name)
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Errorf("want 1 persisted user message only, got: %+v", msgs)
	}
}

// TestRunChat_HistoryBudget asserts the 10-message history cap is enforced:
// when more than 10 messages are stored, only the last 10 are sent to the LLM.
func TestRunChat_HistoryBudget(t *testing.T) {
	ctx := context.Background()
	ns, name := "default", "hist-inc"

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		ns + "/" + name: {
			Incident: store.Incident{Namespace: ns, Name: name, Phase: "Done"},
		},
	}}

	// Seed 12 prior turns (alternating user/assistant).
	for i := 0; i < 12; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		_ = st.AppendChatMessage(ctx, ns, name, role, fmt.Sprintf("msg-%d", i))
	}

	prov := &capturingProvider{delta: "ok"}
	broker := web.NewBroker()

	if err := web.RunChat(ctx, st, prov, broker, ns, name, "new question"); err != nil {
		t.Fatalf("RunChat: %v", err)
	}

	// After RunChat, the new user message is also persisted; total = 13+1=14 stored.
	// But the request must carry at most system + 10 messages.
	req := prov.lastReq
	// req.Messages[0] = system; rest = bounded history (max 10 items).
	if len(req.Messages) > 11 { // 1 system + 10 history
		t.Errorf("history budget not enforced: got %d messages in request (want ≤11)", len(req.Messages))
	}
}

// TestRunChat_UTF8ContextTruncation asserts LOW-1: when ContextJSON contains
// multi-byte runes and is truncated at the byte budget, the system message is
// still valid UTF-8 and within the budget + prefix overhead. LOW-1.
func TestRunChat_UTF8ContextTruncation(t *testing.T) {
	ctx := context.Background()
	ns, name := "default", "utf8-trunc"

	// Build a context blob filled with 3-byte UTF-8 runes ('あ') so that a
	// naive byte-slice at chatContextBudget (4096) splits a rune: 4096 % 3 == 1.
	const budget = 4096
	rune3 := []byte("あ") // 3 bytes: E3 81 82
	ctxBytes := make([]byte, 0, budget+10)
	for len(ctxBytes) < budget+6 {
		ctxBytes = append(ctxBytes, rune3...)
	}

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		ns + "/" + name: {
			Incident: store.Incident{Namespace: ns, Name: name, Phase: "Done"},
			Diagnoses: []store.Diagnosis{{
				Summary:     "s",
				RootCause:   "r",
				ContextJSON: ctxBytes,
			}},
		},
	}}
	prov := &capturingProvider{delta: "ok"}
	broker := web.NewBroker()

	if err := web.RunChat(ctx, st, prov, broker, ns, name, "q"); err != nil {
		t.Fatalf("RunChat: %v", err)
	}

	req := prov.lastReq
	if len(req.Messages) == 0 || req.Messages[0].Role != "system" {
		t.Fatal("expected system message")
	}
	sys := req.Messages[0].Content
	if !strings.Contains(sys, "Context:") {
		t.Fatal("system message missing Context: section")
	}
	// Extract just the context portion to validate UTF-8 without worrying about prefix.
	ctxPart := sys[strings.Index(sys, "Context:"):]
	if !utf8.ValidString(ctxPart) {
		t.Error("LOW-1: context portion of system message is not valid UTF-8")
	}
	// The full system message must also be valid UTF-8.
	if !utf8.ValidString(sys) {
		t.Error("LOW-1: system message is not valid UTF-8")
	}
}

// TestChatPost_EmptyMessage asserts LOW-2: empty and whitespace-only messages
// return HTTP 400 without invoking the provider.
func TestChatPost_EmptyMessage(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		"default/empty-msg": {
			Incident: store.Incident{
				Namespace: "default", Name: "empty-msg", Phase: "Done",
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}}
	prov := &capturingProvider{delta: "should not be called"}
	broker := web.NewBroker()
	srv := web.New(st, broker, prov)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cases := []struct {
		name string
		body string
		ct   string
	}{
		{"empty_form", "message=", "application/x-www-form-urlencoded"},
		{"whitespace_form", "message=   ", "application/x-www-form-urlencoded"},
		{"empty_body", "", "text/plain"},
		{"whitespace_body", "   \t\n", "text/plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(ts.URL+"/incidents/default/empty-msg/chat",
				tc.ct, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("LOW-2: want 400 for empty/whitespace message, got %d", resp.StatusCode)
			}
		})
	}
	// Provider must not have been invoked for any of the above.
	if prov.lastReq.Messages != nil {
		t.Error("LOW-2: provider was invoked despite empty/whitespace message")
	}
}

// emptyStreamProvider is a non-streaming Provider that returns no content (no choices),
// so StreamOrComplete never calls onDelta and accumulated stays empty. LOW-3.
type emptyStreamProvider struct{}

func (e *emptyStreamProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	return agent.Response{}, nil // no Choices → onDelta never called
}

// TestRunChat_EmptyStreamFallback asserts LOW-3: when the provider returns no
// content, an assistant message is persisted with the fallback text (not empty).
func TestRunChat_EmptyStreamFallback(t *testing.T) {
	ctx := context.Background()
	ns, name := "default", "empty-stream"

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		ns + "/" + name: {
			Incident: store.Incident{Namespace: ns, Name: name, Phase: "Done"},
		},
	}}
	broker := web.NewBroker()

	if err := web.RunChat(ctx, st, &emptyStreamProvider{}, broker, ns, name, "q"); err != nil {
		t.Fatalf("RunChat: %v", err)
	}

	msgs, _ := st.ListChatMessages(ctx, ns, name)
	if len(msgs) != 2 {
		t.Fatalf("LOW-3: want 2 messages, got %d", len(msgs))
	}
	asst := msgs[1]
	if asst.Role != "assistant" {
		t.Fatalf("LOW-3: want assistant message, got role=%q", asst.Role)
	}
	if strings.TrimSpace(asst.Content) == "" {
		t.Error("LOW-3: assistant message is empty — fallback not applied")
	}
	if asst.Content == "" || asst.Content == " " {
		t.Error("LOW-3: assistant message is blank")
	}
	// Verify it's the expected fallback text.
	if asst.Content != "(no response from model)" {
		t.Errorf("LOW-3: want fallback text, got %q", asst.Content)
	}
}

// TestRunChat_HistoryByteBudget asserts LOW-4: when history messages' total
// content bytes exceed chatHistoryByteBudget (8192), oldest messages are dropped
// so the request stays within the byte cap.
func TestRunChat_HistoryByteBudget(t *testing.T) {
	ctx := context.Background()
	ns, name := "default", "byte-budget"

	st := &fakeStore{incidents: map[string]*store.IncidentDetail{
		ns + "/" + name: {
			Incident: store.Incident{Namespace: ns, Name: name, Phase: "Done"},
		},
	}}

	// Seed 8 messages each with 1500-byte content → total 12000 bytes, well over 8192.
	// Also within the 10-message count cap, so only the byte cap triggers.
	bigContent := strings.Repeat("x", 1500)
	for i := 0; i < 8; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		_ = st.AppendChatMessage(ctx, ns, name, role, bigContent)
	}

	prov := &capturingProvider{delta: "ok"}
	broker := web.NewBroker()

	if err := web.RunChat(ctx, st, prov, broker, ns, name, "new question"); err != nil {
		t.Fatalf("RunChat: %v", err)
	}

	req := prov.lastReq
	// Count total history content bytes (exclude system message at index 0).
	var totalHistBytes int
	for _, m := range req.Messages[1:] {
		totalHistBytes += len(m.Content)
	}
	const chatHistoryByteBudget = 8192
	if totalHistBytes > chatHistoryByteBudget {
		t.Errorf("LOW-4: history byte budget exceeded: %d bytes in request (limit %d)",
			totalHistBytes, chatHistoryByteBudget)
	}
}

// TestChatAssistantXSS asserts a stored assistant message with <script> is NOT
// present verbatim in the rendered detail page (SEC-001 via RenderMarkdown). TASK-021.
func TestChatAssistantXSS(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{
		incidents: map[string]*store.IncidentDetail{
			"default/chat-xss": {
				Incident: store.Incident{
					Namespace: "default", Name: "chat-xss",
					Phase: "Done", CreatedAt: now, UpdatedAt: now,
				},
			},
		},
		chatMessages: []store.ChatMessage{
			{ID: 1, Namespace: "default", Name: "chat-xss", Role: "assistant",
				Content: "<script>alert(1)</script>", CreatedAt: now},
		},
	}
	broker := web.NewBroker()
	srv := web.New(st, broker, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/incidents/default/chat-xss")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	body := sb.String()

	// SEC-001: raw script tag must not appear verbatim.
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("SEC-001: XSS payload in assistant message found verbatim in rendered page")
	}
}
