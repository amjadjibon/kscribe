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

// TestRunChat asserts SEC-002 (escaped SSE frames), message persistence,
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

	// XSS payload from the "model".
	xssPayload := `<script>alert(1)</script>`
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

	// SEC-002: no raw <script> on the wire.
	if len(events) == 0 {
		t.Fatal("expected at least one published event")
	}
	for _, ev := range events {
		if strings.Contains(ev.HTML, "<script>") {
			t.Errorf("SEC-002: raw <script> in SSE payload: %q", ev.HTML)
		}
		if !strings.Contains(ev.HTML, "&lt;script&gt;") {
			t.Errorf("SEC-002: escaped form missing from SSE payload: %q", ev.HTML)
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
