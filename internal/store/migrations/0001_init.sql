-- 0001_init.sql: bootstrap schema for the kscribe SQLite history mirror.
-- ADR-003: CR status is source of truth; this DB is a queryable history mirror.
-- ADR-004: migrations fail closed (runner wraps each file in a transaction).

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS incidents (
    id                        INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace                 TEXT    NOT NULL,
    name                      TEXT    NOT NULL,
    event_uid                 TEXT    NOT NULL DEFAULT '',
    involved_object_kind      TEXT    NOT NULL DEFAULT '',
    involved_object_name      TEXT    NOT NULL DEFAULT '',
    involved_object_namespace TEXT    NOT NULL DEFAULT '',
    reason                    TEXT    NOT NULL DEFAULT '',
    message                   TEXT    NOT NULL DEFAULT '',
    phase                     TEXT    NOT NULL DEFAULT 'Pending',
    started_at                TEXT,
    completed_at              TEXT,
    llm_provider              TEXT    NOT NULL DEFAULT '',
    llm_model                 TEXT    NOT NULL DEFAULT '',
    tokens_used               INTEGER NOT NULL DEFAULT 0,
    prompt_redacted           INTEGER NOT NULL DEFAULT 0,
    persisted                 INTEGER NOT NULL DEFAULT 0,
    created_at                TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at                TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (namespace, name)
);

CREATE INDEX IF NOT EXISTS idx_incidents_phase      ON incidents (phase);
CREATE INDEX IF NOT EXISTS idx_incidents_updated_at ON incidents (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_incidents_created_at ON incidents (created_at DESC);

CREATE TABLE IF NOT EXISTS diagnoses (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace   TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    event_uid   TEXT    NOT NULL DEFAULT '',
    rca_json    TEXT    NOT NULL DEFAULT '{}',
    summary     TEXT    NOT NULL DEFAULT '',
    root_cause  TEXT    NOT NULL DEFAULT '',
    remediation TEXT    NOT NULL DEFAULT '',
    confidence  REAL    NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (namespace, name) REFERENCES incidents (namespace, name)
);

CREATE INDEX IF NOT EXISTS idx_diagnoses_incident ON diagnoses (namespace, name);
