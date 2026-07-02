-- 0002_context_reasoning.sql: add context snapshot, narrative reasoning, and tool-call trace
-- to the diagnoses table. SQLite allows only one ADD COLUMN per ALTER TABLE statement.
ALTER TABLE diagnoses ADD COLUMN context_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE diagnoses ADD COLUMN reasoning    TEXT NOT NULL DEFAULT '';
ALTER TABLE diagnoses ADD COLUMN trace_json   TEXT NOT NULL DEFAULT '[]';
