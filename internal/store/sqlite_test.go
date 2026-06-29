package store

import (
	"context"
	"database/sql"
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

// TestListIncidentsAndGetIncident verifies ordering and field correctness.
func TestListIncidentsAndGetIncident(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	names := []string{"alpha", "beta", "gamma"}
	for i, n := range names {
		started := now.Add(time.Duration(i) * time.Second)
		if err := s.UpsertIncident(ctx, Incident{
			Namespace:  "ns1",
			Name:       n,
			Phase:      "Done",
			StartedAt:  &started,
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
