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
	page1, err := s.ListIncidentsPage(ctx, 25, 0)
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
	page2, err := s.ListIncidentsPage(ctx, 25, 25)
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
	empty, err := s.ListIncidentsPage(ctx, 25, 100)
	if err != nil {
		t.Fatalf("past-end: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("past-end len = %d, want 0", len(empty))
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

	got, err := s.CountIncidentsByPhase(ctx)
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
