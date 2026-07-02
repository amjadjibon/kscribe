package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	_ "modernc.org/sqlite"
)

// Incident mirrors a KscribeDiagnosis CR's current state.
type Incident struct {
	Namespace               string
	Name                    string
	EventUID                string
	InvolvedObjectKind      string
	InvolvedObjectName      string
	InvolvedObjectNamespace string
	Reason                  string
	Message                 string
	Phase                   string
	StartedAt               *time.Time
	CompletedAt             *time.Time
	LLMProvider             string
	LLMModel                string
	TokensUsed              int64
	PromptRedacted          bool
	Persisted               bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// Diagnosis is a final RCA record linked to an incident.
type Diagnosis struct {
	ID          int64
	Namespace   string
	Name        string
	EventUID    string
	RCAJson     []byte // raw JSON — decode with sonic
	Summary     string
	RootCause   string
	Remediation string
	Confidence  float64
	CreatedAt   time.Time
	ContextJSON []byte // redacted snapshot sent to the LLM — decode with sonic
	Reasoning   string // narrative explanation of how the conclusion was reached
	TraceJSON   []byte // tool-call trace — decode with sonic
}

// IncidentFilter holds optional filter criteria for incident list queries. SEC-002: all
// fields are passed as bound parameters — never interpolated into SQL.
type IncidentFilter struct {
	Phase     string
	Namespace string
	Reason    string
	Query     string // free-text: LIKE match against name, message, reason
}

// buildWhere builds a parameterized WHERE clause from f.
// If includePhase is false, f.Phase is ignored (TASK-023: per-phase counts ignore phase filter).
func buildWhere(f IncidentFilter, includePhase bool) (string, []any) {
	var conds []string
	var args []any
	if includePhase && f.Phase != "" {
		conds = append(conds, "phase = ?")
		args = append(args, f.Phase)
	}
	if f.Namespace != "" {
		conds = append(conds, "namespace = ?")
		args = append(args, f.Namespace)
	}
	if f.Reason != "" {
		conds = append(conds, "reason = ?")
		args = append(args, f.Reason)
	}
	if f.Query != "" {
		conds = append(conds, "(name LIKE ? OR message LIKE ? OR reason LIKE ?)")
		w := "%" + f.Query + "%"
		args = append(args, w, w, w)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// IncidentDetail bundles an Incident with its Diagnoses.
type IncidentDetail struct {
	Incident
	Diagnoses []Diagnosis
}

// Store wraps a SQLite database with typed access methods.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at dbPath, applies pending
// migrations, and returns a ready Store. Returns an error if any migration
// fails — the store is not usable in that case (ADR-004: fail closed).
func Open(dbPath string) (*Store, error) {
	return openWithFS(dbPath, migrationsFS)
}

// openWithFS is the testable entry point; callers may supply a custom fs.FS.
func openWithFS(dbPath string, fsys fs.FS) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dbPath, err)
	}
	// SQLite is single-writer; one connection avoids "database is locked" races.
	db.SetMaxOpenConns(1)

	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma journal_mode: %w", err)
	}
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma busy_timeout: %w", err)
	}

	if err = runMigrations(db, fsys); err != nil {
		db.Close()
		return nil, err // already wrapped by runMigrations
	}

	return &Store{db: db}, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// UpsertIncident inserts or updates the incident row for a given CR namespace/name.
func (s *Store) UpsertIncident(ctx context.Context, inc Incident) error {
	const q = `
INSERT INTO incidents (
    namespace, name, event_uid,
    involved_object_kind, involved_object_name, involved_object_namespace,
    reason, message, phase,
    started_at, completed_at,
    llm_provider, llm_model, tokens_used, prompt_redacted, persisted,
    updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(namespace, name) DO UPDATE SET
    event_uid                 = excluded.event_uid,
    involved_object_kind      = excluded.involved_object_kind,
    involved_object_name      = excluded.involved_object_name,
    involved_object_namespace = excluded.involved_object_namespace,
    reason                    = excluded.reason,
    message                   = excluded.message,
    phase                     = excluded.phase,
    started_at                = excluded.started_at,
    completed_at              = excluded.completed_at,
    llm_provider              = excluded.llm_provider,
    llm_model                 = excluded.llm_model,
    tokens_used               = excluded.tokens_used,
    prompt_redacted           = excluded.prompt_redacted,
    persisted                 = excluded.persisted,
    updated_at                = excluded.updated_at`

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, q,
		inc.Namespace, inc.Name, inc.EventUID,
		inc.InvolvedObjectKind, inc.InvolvedObjectName, inc.InvolvedObjectNamespace,
		inc.Reason, inc.Message, inc.Phase,
		fmtTimePtr(inc.StartedAt), fmtTimePtr(inc.CompletedAt),
		inc.LLMProvider, inc.LLMModel, inc.TokensUsed, inc.PromptRedacted, inc.Persisted,
		now,
	)
	return err
}

// InsertDiagnosis writes a final RCA record. rcaPayload is encoded to JSON
// with sonic (CON-003: no encoding/json).
func (s *Store) InsertDiagnosis(ctx context.Context, d Diagnosis, rcaPayload any) error {
	rcaJSON, err := sonic.Marshal(rcaPayload)
	if err != nil {
		return fmt.Errorf("marshal rca payload: %w", err)
	}
	ctxJSON := d.ContextJSON
	if len(ctxJSON) == 0 {
		ctxJSON = []byte("{}")
	}
	traceJSON := d.TraceJSON
	if len(traceJSON) == 0 {
		traceJSON = []byte("[]")
	}
	const q = `
INSERT INTO diagnoses (namespace, name, event_uid, rca_json, summary, root_cause, remediation, confidence, created_at, context_json, reasoning, trace_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, q,
		d.Namespace, d.Name, d.EventUID,
		string(rcaJSON), d.Summary, d.RootCause, d.Remediation, d.Confidence,
		time.Now().UTC().Format(time.RFC3339Nano),
		string(ctxJSON), d.Reasoning, string(traceJSON),
	)
	return err
}

// ListIncidents returns incidents ordered by updated_at DESC, capped at limit rows.
func (s *Store) ListIncidents(ctx context.Context, limit int) ([]Incident, error) {
	return s.ListIncidentsPage(ctx, IncidentFilter{}, limit, 0)
}

// ListIncidentsPage returns a filtered page of incidents ordered by updated_at DESC.
func (s *Store) ListIncidentsPage(ctx context.Context, filter IncidentFilter, limit, offset int) ([]Incident, error) {
	where, args := buildWhere(filter, true)
	q := `SELECT namespace, name, event_uid,
       involved_object_kind, involved_object_name, involved_object_namespace,
       reason, message, phase,
       started_at, completed_at,
       llm_provider, llm_model, tokens_used, prompt_redacted, persisted,
       created_at, updated_at
FROM incidents` + where + ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// CountIncidents returns the total number of incidents matching filter.
func (s *Store) CountIncidents(ctx context.Context, filter IncidentFilter) (int, error) {
	where, args := buildWhere(filter, true)
	q := `SELECT COUNT(*) FROM incidents` + where
	var count int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&count)
	return count, err
}

// CountIncidentsByPhase returns a count per phase, applying all filter fields EXCEPT Phase.
// TASK-023: ignoring filter.Phase keeps stat cards populated as phase toggles.
func (s *Store) CountIncidentsByPhase(ctx context.Context, filter IncidentFilter) (map[string]int, error) {
	where, args := buildWhere(filter, false) // false = skip filter.Phase
	q := `SELECT phase, COUNT(*) FROM incidents` + where + ` GROUP BY phase`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var phase string
		var count int
		if err := rows.Scan(&phase, &count); err != nil {
			return nil, err
		}
		out[phase] = count
	}
	return out, rows.Err()
}

// GetIncident returns a single incident and all its diagnoses.
func (s *Store) GetIncident(ctx context.Context, namespace, name string) (*IncidentDetail, error) {
	const iq = `
SELECT namespace, name, event_uid,
       involved_object_kind, involved_object_name, involved_object_namespace,
       reason, message, phase,
       started_at, completed_at,
       llm_provider, llm_model, tokens_used, prompt_redacted, persisted,
       created_at, updated_at
FROM incidents WHERE namespace = ? AND name = ?`

	row := s.db.QueryRowContext(ctx, iq, namespace, name)
	inc, err := scanIncident(row)
	if err != nil {
		return nil, err
	}

	const dq = `
SELECT id, namespace, name, event_uid, rca_json, summary, root_cause, remediation, confidence, created_at, context_json, reasoning, trace_json
FROM diagnoses WHERE namespace = ? AND name = ?
ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, dq, namespace, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var diags []Diagnosis
	for rows.Next() {
		var d Diagnosis
		var createdAt string
		if err := rows.Scan(
			&d.ID, &d.Namespace, &d.Name, &d.EventUID,
			&d.RCAJson, &d.Summary, &d.RootCause, &d.Remediation, &d.Confidence,
			&createdAt, &d.ContextJSON, &d.Reasoning, &d.TraceJSON,
		); err != nil {
			return nil, err
		}
		d.CreatedAt = parseTime(createdAt)
		diags = append(diags, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &IncidentDetail{Incident: inc, Diagnoses: diags}, nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanIncident(s scanner) (Incident, error) {
	var inc Incident
	var startedAt, completedAt, createdAt, updatedAt *string
	err := s.Scan(
		&inc.Namespace, &inc.Name, &inc.EventUID,
		&inc.InvolvedObjectKind, &inc.InvolvedObjectName, &inc.InvolvedObjectNamespace,
		&inc.Reason, &inc.Message, &inc.Phase,
		&startedAt, &completedAt,
		&inc.LLMProvider, &inc.LLMModel, &inc.TokensUsed, &inc.PromptRedacted, &inc.Persisted,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return Incident{}, err
	}
	inc.StartedAt = parseTimePtr(startedAt)
	inc.CompletedAt = parseTimePtr(completedAt)
	if createdAt != nil {
		inc.CreatedAt = parseTime(*createdAt)
	}
	if updatedAt != nil {
		inc.UpdatedAt = parseTime(*updatedAt)
	}
	return inc, nil
}

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func parseTimePtr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t := parseTime(*s)
	if t.IsZero() {
		return nil
	}
	return &t
}

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
