package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// openTestStore opens a Store against a temp-file DB using the real migrations.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestMigrationSuccess verifies that all three tables exist and version 1 is recorded.
func TestMigrationSuccess(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, table := range []string{"schema_migrations", "incidents", "diagnoses"} {
		var name string
		err := s.db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	var version int
	if err := s.db.QueryRowContext(ctx,
		"SELECT version FROM schema_migrations WHERE version = 1",
	).Scan(&version); err != nil {
		t.Errorf("migration version 1 not recorded: %v", err)
	}
}

// TestMigrationFailurePreventsStartup injects a bad migration and asserts that
// Open returns an error. It also verifies that version 1 was NOT recorded
// (transaction rolled back — no partial state).
func TestMigrationFailurePreventsStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bad.db")

	badFS := fstest.MapFS{
		"migrations/0001_bad.sql": &fstest.MapFile{
			Data: []byte("THIS IS NOT VALID SQL AND SHOULD FAIL;"),
		},
	}

	_, err := openWithFS(dbPath, badFS)
	if err == nil {
		t.Fatal("expected error from bad migration, got nil")
	}

	// Verify no partial state: the failed version must not appear in schema_migrations.
	// schema_migrations is bootstrapped outside the tx so it may exist, but the
	// version row must not be present (the tx was rolled back).
	db, openErr := sql.Open("sqlite", dbPath)
	if openErr != nil {
		t.Fatalf("re-open db to check state: %v", openErr)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 1",
	).Scan(&count); err != nil {
		// schema_migrations may not even exist if the file was never created; that is fine.
		t.Logf("schema_migrations not present (acceptable): %v", err)
		return
	}
	if count != 0 {
		t.Errorf("bad migration version recorded in schema_migrations — partial state leaked")
	}
}

// TestUpsertIncident verifies insert-then-update semantics on (namespace, name).
func TestUpsertIncident(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inc := Incident{
		Namespace:          "default",
		Name:               "test-incident",
		EventUID:           "uid-001",
		InvolvedObjectKind: "Pod",
		InvolvedObjectName: "myapp-abc",
		Reason:             "BackOff",
		Message:            "Back-off restarting failed container",
		Phase:              "Pending",
	}

	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Update phase and verify the row is updated (not duplicated).
	inc.Phase = "Done"
	inc.LLMProvider = "openai"
	inc.LLMModel = "gpt-4o-mini"
	inc.TokensUsed = 1234
	inc.Persisted = true

	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var rowCount int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM incidents WHERE namespace='default' AND name='test-incident'",
	).Scan(&rowCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("expected 1 row after two upserts, got %d", rowCount)
	}

	var phase string
	var tokens int64
	if err := s.db.QueryRow(
		"SELECT phase, tokens_used FROM incidents WHERE namespace='default' AND name='test-incident'",
	).Scan(&phase, &tokens); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if phase != "Done" {
		t.Errorf("phase = %q, want Done", phase)
	}
	if tokens != 1234 {
		t.Errorf("tokens_used = %d, want 1234", tokens)
	}
}

// TestInsertDiagnosisAndReadBack writes a final RCA and verifies it round-trips correctly.
func TestInsertDiagnosisAndReadBack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Need a parent incident first (FK).
	if err := s.UpsertIncident(ctx, Incident{
		Namespace: "kube-system",
		Name:      "oom-diag",
		Phase:     "Done",
	}); err != nil {
		t.Fatalf("upsert incident: %v", err)
	}

	type rcaPayload struct {
		Analysis string `json:"analysis"`
	}
	payload := rcaPayload{Analysis: "OOM caused by memory leak in sidecar"}

	d := Diagnosis{
		Namespace:   "kube-system",
		Name:        "oom-diag",
		EventUID:    "uid-oom",
		Summary:     "OOM kill",
		RootCause:   "memory_leak",
		Remediation: "Increase memory limit or fix leak",
		Confidence:  0.92,
	}
	if err := s.InsertDiagnosis(ctx, d, payload); err != nil {
		t.Fatalf("InsertDiagnosis: %v", err)
	}

	detail, err := s.GetIncident(ctx, "kube-system", "oom-diag")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if len(detail.Diagnoses) != 1 {
		t.Fatalf("expected 1 diagnosis, got %d", len(detail.Diagnoses))
	}

	got := detail.Diagnoses[0]
	if got.Summary != "OOM kill" {
		t.Errorf("Summary = %q, want OOM kill", got.Summary)
	}
	if got.Confidence != 0.92 {
		t.Errorf("Confidence = %f, want 0.92", got.Confidence)
	}
	if len(got.RCAJson) == 0 {
		t.Error("RCAJson empty")
	}
}

// TestListIncidentsPage verifies paging, offset past end, and ordering.
func TestListIncidentsPage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Insert 30 incidents with distinct updated_at times so ordering is deterministic.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		if err := s.UpsertIncident(ctx, Incident{
			Namespace: "pg", Name: fmt.Sprintf("inc-%02d", i),
			Phase: "Done", UpdatedAt: ts,
		}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	// Page 1 (25 items): most recent first, so inc-29..inc-05.
	page1, err := s.ListIncidentsPage(ctx, IncidentFilter{}, 25, 0)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 25 {
		t.Fatalf("page1 len = %d, want 25", len(page1))
	}
	if page1[0].Name != "inc-29" {
		t.Errorf("page1[0] = %q, want inc-29", page1[0].Name)
	}

	// Page 2 (5 items): inc-04..inc-00.
	page2, err := s.ListIncidentsPage(ctx, IncidentFilter{}, 25, 25)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2 len = %d, want 5", len(page2))
	}
	if page2[0].Name != "inc-04" {
		t.Errorf("page2[0] = %q, want inc-04", page2[0].Name)
	}

	// Offset past end returns empty.
	empty, err := s.ListIncidentsPage(ctx, IncidentFilter{}, 25, 100)
	if err != nil {
		t.Fatalf("past-end: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("past-end len = %d, want 0", len(empty))
	}
}

// TestFilterByPhase verifies that ListIncidentsPage and CountIncidents respect filter.Phase.
func TestFilterByPhase(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, inc := range []Incident{
		{Namespace: "ns", Name: "a", Phase: "Done"},
		{Namespace: "ns", Name: "b", Phase: "Failed"},
		{Namespace: "ns", Name: "c", Phase: "Failed"},
	} {
		if err := s.UpsertIncident(ctx, inc); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	f := IncidentFilter{Phase: "Failed"}
	rows, err := s.ListIncidentsPage(ctx, f, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 Failed, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Phase != "Failed" {
			t.Errorf("unexpected phase %q", r.Phase)
		}
	}

	count, err := s.CountIncidents(ctx, f)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("CountIncidents want 2, got %d", count)
	}
}

// TestFilterByNamespace verifies namespace filtering.
func TestFilterByNamespace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, inc := range []Incident{
		{Namespace: "prod", Name: "x", Phase: "Done"},
		{Namespace: "staging", Name: "y", Phase: "Done"},
	} {
		if err := s.UpsertIncident(ctx, inc); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	rows, err := s.ListIncidentsPage(ctx, IncidentFilter{Namespace: "prod"}, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Namespace != "prod" {
		t.Errorf("want 1 prod incident, got %+v", rows)
	}
}

// TestFilterByQuery verifies free-text search against name, message, and reason.
func TestFilterByQuery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, inc := range []Incident{
		{Namespace: "ns", Name: "oom-killer", Phase: "Done", Message: "container killed", Reason: "OOMKilled"},
		{Namespace: "ns", Name: "crash-loop", Phase: "Done", Message: "back-off restarting", Reason: "BackOff"},
		{Namespace: "ns", Name: "image-pull", Phase: "Failed", Message: "failed to pull image", Reason: "ImagePullBackOff"},
	} {
		if err := s.UpsertIncident(ctx, inc); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// Matches name "oom-killer" and reason "OOMKilled".
	rows, err := s.ListIncidentsPage(ctx, IncidentFilter{Query: "oom"}, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("query 'oom': want 1, got %d: %+v", len(rows), rows)
	}

	// Matches message "failed to pull image" and name "image-pull".
	rows2, err := s.ListIncidentsPage(ctx, IncidentFilter{Query: "image"}, 10, 0)
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	if len(rows2) != 1 {
		t.Errorf("query 'image': want 1, got %d", len(rows2))
	}
}

// TestFilterCombinedAndPaging verifies that phase + query filters compose correctly with paging.
func TestFilterCombinedAndPaging(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		if err := s.UpsertIncident(ctx, Incident{
			Namespace: "ns", Name: fmt.Sprintf("pod-%02d", i),
			Phase: "Failed", Message: "image pull failed", UpdatedAt: ts,
		}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	// Add a non-matching row.
	if err := s.UpsertIncident(ctx, Incident{
		Namespace: "ns", Name: "other", Phase: "Done", Message: "all good",
	}); err != nil {
		t.Fatalf("upsert other: %v", err)
	}

	f := IncidentFilter{Phase: "Failed", Query: "image"}
	total, err := s.CountIncidents(ctx, f)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 10 {
		t.Errorf("want 10 total, got %d", total)
	}

	// Page 1 of 3 with pageSize=4.
	p1, err := s.ListIncidentsPage(ctx, f, 4, 0)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(p1) != 4 {
		t.Errorf("page1 len = %d, want 4", len(p1))
	}

	// Page 3 (remainder=2).
	p3, err := s.ListIncidentsPage(ctx, f, 4, 8)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(p3) != 2 {
		t.Errorf("page3 len = %d, want 2", len(p3))
	}
}

// TestFilteredCounts verifies CountIncidentsByPhase ignores filter.Phase (TASK-023).
func TestFilteredCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, inc := range []Incident{
		{Namespace: "prod", Name: "a", Phase: "Done"},
		{Namespace: "prod", Name: "b", Phase: "Failed"},
		{Namespace: "staging", Name: "c", Phase: "Done"},
	} {
		if err := s.UpsertIncident(ctx, inc); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// Filter by phase=Failed + namespace=prod: CountIncidentsByPhase must ignore phase.
	f := IncidentFilter{Phase: "Failed", Namespace: "prod"}
	counts, err := s.CountIncidentsByPhase(ctx, f)
	if err != nil {
		t.Fatalf("CountIncidentsByPhase: %v", err)
	}
	// Only prod namespace: Done=1, Failed=1 (phase filter excluded).
	if counts["Done"] != 1 {
		t.Errorf("Done count = %d, want 1", counts["Done"])
	}
	if counts["Failed"] != 1 {
		t.Errorf("Failed count = %d, want 1", counts["Failed"])
	}
	// staging is excluded by namespace filter.
	total := 0
	for _, n := range counts {
		total += n
	}
	if total != 2 {
		t.Errorf("total from counts = %d, want 2", total)
	}
}

// TestInjectionLiteral verifies that a SQL injection string is treated as a literal
// (returns 0 rows, not all rows). SEC-002: parameterized queries only.
func TestInjectionLiteral(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.UpsertIncident(ctx, Incident{
		Namespace: "ns", Name: "real-incident", Phase: "Done",
		Message: "normal message",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// If the query is parameterized, this returns 0 rows (no incident has
	// name/message/reason containing the literal string "' OR 1=1 --").
	rows, err := s.ListIncidentsPage(ctx, IncidentFilter{Query: "' OR 1=1 --"}, 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("SEC-002: injection returned %d rows, want 0 — query is not parameterized", len(rows))
	}
}

// TestMigration0002Columns verifies that the 0002 migration applied and the new columns exist.
func TestMigration0002Columns(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Verify version 2 is recorded.
	var version int
	if err := s.db.QueryRowContext(ctx,
		"SELECT version FROM schema_migrations WHERE version = 2",
	).Scan(&version); err != nil {
		t.Fatalf("migration version 2 not recorded: %v", err)
	}

	// Verify the three new columns exist by querying them directly.
	if err := s.db.QueryRowContext(ctx,
		"SELECT context_json, reasoning, trace_json FROM diagnoses LIMIT 0",
	).Err(); err != nil {
		t.Fatalf("new columns missing from diagnoses: %v", err)
	}
}

// TestInsertDiagnosis_ContextReasoningTrace verifies that the three new fields
// round-trip through InsertDiagnosis → GetIncident correctly.
func TestInsertDiagnosis_ContextReasoningTrace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.UpsertIncident(ctx, Incident{
		Namespace: "ns", Name: "crt-diag", Phase: "Done",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	d := Diagnosis{
		Namespace:   "ns",
		Name:        "crt-diag",
		EventUID:    "uid-crt",
		Summary:     "OOM",
		RootCause:   "memory leak",
		Remediation: "increase limit",
		Confidence:  0.85,
		ContextJSON: []byte(`{"reason":"OOMKilled"}`),
		Reasoning:   "high confidence based on OOM events in logs",
		TraceJSON:   []byte(`[{"tool":"get_pod_logs","args":"{}","result":"OOM"}]`),
	}
	type dummy struct{ X int }
	if err := s.InsertDiagnosis(ctx, d, dummy{X: 1}); err != nil {
		t.Fatalf("InsertDiagnosis: %v", err)
	}

	detail, err := s.GetIncident(ctx, "ns", "crt-diag")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if len(detail.Diagnoses) != 1 {
		t.Fatalf("want 1 diagnosis, got %d", len(detail.Diagnoses))
	}
	got := detail.Diagnoses[0]

	if string(got.ContextJSON) != `{"reason":"OOMKilled"}` {
		t.Errorf("ContextJSON = %q, want {\"reason\":\"OOMKilled\"}", got.ContextJSON)
	}
	if got.Reasoning != "high confidence based on OOM events in logs" {
		t.Errorf("Reasoning = %q", got.Reasoning)
	}
	if string(got.TraceJSON) != `[{"tool":"get_pod_logs","args":"{}","result":"OOM"}]` {
		t.Errorf("TraceJSON = %q", got.TraceJSON)
	}
}

// TestCountIncidentsByPhase verifies phase aggregation.
func TestCountIncidentsByPhase(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	phases := map[string]int{"Done": 3, "Failed": 2, "Pending": 1}
	idx := 0
	for phase, n := range phases {
		for i := 0; i < n; i++ {
			if err := s.UpsertIncident(ctx, Incident{
				Namespace: "cp", Name: fmt.Sprintf("inc-%d", idx),
				Phase: phase,
			}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			idx++
		}
	}

	got, err := s.CountIncidentsByPhase(ctx, IncidentFilter{})
	if err != nil {
		t.Fatalf("CountIncidentsByPhase: %v", err)
	}
	for phase, want := range phases {
		if got[phase] != want {
			t.Errorf("phase %q count = %d, want %d", phase, got[phase], want)
		}
	}
	total := 0
	for _, n := range got {
		total += n
	}
	if total != 6 {
		t.Errorf("total = %d, want 6", total)
	}
}

// TestListIncidentsAndGetIncident verifies ordering and field correctness.
func TestListIncidentsAndGetIncident(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	names := []string{"alpha", "beta", "gamma"}
	for i, n := range names {
		started := now.Add(time.Duration(i) * time.Second)
		if err := s.UpsertIncident(ctx, Incident{
			Namespace:   "ns1",
			Name:        n,
			Phase:       "Done",
			StartedAt:   &started,
			LLMProvider: "openai",
		}); err != nil {
			t.Fatalf("upsert %s: %v", n, err)
		}
	}

	list, err := s.ListIncidents(ctx, 10)
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 incidents, got %d", len(list))
	}

	// Most recent updated_at should be "gamma" (inserted last).
	if list[0].Name != "gamma" {
		t.Errorf("first result = %q, want gamma (most recently updated)", list[0].Name)
	}

	// GetIncident for a specific one.
	detail, err := s.GetIncident(ctx, "ns1", "alpha")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if detail.Name != "alpha" {
		t.Errorf("GetIncident returned name %q, want alpha", detail.Name)
	}
	if detail.LLMProvider != "openai" {
		t.Errorf("LLMProvider = %q, want openai", detail.LLMProvider)
	}

	// Limit is respected.
	limited, err := s.ListIncidents(ctx, 2)
	if err != nil {
		t.Fatalf("ListIncidents limit: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("expected 2 results with limit=2, got %d", len(limited))
	}
}

// TestChatMessageRoundTrip verifies AppendChatMessage + ListChatMessages order and isolation.
func TestChatMessageRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Seed a parent incident (FK not enforced on chat_messages but good practice).
	if err := s.UpsertIncident(ctx, Incident{Namespace: "ns", Name: "chat-inc", Phase: "Done"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Append three turns.
	turns := []struct{ role, content string }{
		{"user", "what happened?"},
		{"assistant", "OOM kill"},
		{"user", "how to fix?"},
	}
	for _, turn := range turns {
		if err := s.AppendChatMessage(ctx, "ns", "chat-inc", turn.role, turn.content); err != nil {
			t.Fatalf("AppendChatMessage(%s): %v", turn.role, err)
		}
	}

	msgs, err := s.ListChatMessages(ctx, "ns", "chat-inc")
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	// ORDER BY id ASC — first inserted comes first.
	for i, want := range turns {
		if msgs[i].Role != want.role {
			t.Errorf("[%d] role = %q, want %q", i, msgs[i].Role, want.role)
		}
		if msgs[i].Content != want.content {
			t.Errorf("[%d] content = %q, want %q", i, msgs[i].Content, want.content)
		}
		if msgs[i].ID <= 0 {
			t.Errorf("[%d] ID not set", i)
		}
		if msgs[i].CreatedAt.IsZero() {
			t.Errorf("[%d] CreatedAt not set", i)
		}
	}
	// IDs must be ascending.
	if msgs[0].ID >= msgs[1].ID || msgs[1].ID >= msgs[2].ID {
		t.Errorf("IDs not ascending: %d %d %d", msgs[0].ID, msgs[1].ID, msgs[2].ID)
	}

	// Isolation: different incident returns empty.
	other, err := s.ListChatMessages(ctx, "ns", "other-inc")
	if err != nil {
		t.Fatalf("ListChatMessages other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("want 0 messages for other incident, got %d", len(other))
	}
}

// TestMigration0003ChatTable verifies the 0003 migration ran and the table exists.
func TestMigration0003ChatTable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var version int
	if err := s.db.QueryRowContext(ctx,
		"SELECT version FROM schema_migrations WHERE version = 3",
	).Scan(&version); err != nil {
		t.Fatalf("migration version 3 not recorded: %v", err)
	}

	if err := s.db.QueryRowContext(ctx,
		"SELECT id, namespace, name, role, content, created_at FROM chat_messages LIMIT 0",
	).Err(); err != nil {
		t.Fatalf("chat_messages table or columns missing: %v", err)
	}
}
