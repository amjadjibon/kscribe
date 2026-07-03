---
date: 2026-07-03
plan: docs/production-ready/PLAN.md
plan_version: 1.1
reviewer: Claude
verdict: Needs Revision
---

# Plan Review: production-ready

## Verdict

**Needs Revision** — the plan is well-structured and testable, but it names CR phase constants and a DB column that do not exist in the codebase; agents following the prompts literally will diverge or stall on both.

## Findings

### [REVISE-001] Terminal phases "Completed/Failed" don't exist — actual enum is Done/Partial/Failed
**Phase**: 1, 2, 4 (and RISK-001)
**Issue**: `api/v1alpha1/kscribediagnosis_types.go` defines `Pending | Diagnosing | Done | Partial | Failed`. The plan repeatedly says "terminal phase (Completed/Failed)": TASK-003's CR pruner would miss every `Done` and `Partial` diagnosis (i.e. most of them — defeating the phase's purpose), TASK-006's metrics label spec says `outcome (completed|failed)`, and RISK-001's mitigation names the same nonexistent constant. `DiagnosisPhaseCompleted` won't compile, so an agent will improvise — and may guess that `Partial` is not terminal.
**Fix**: Replace every "Completed/Failed" with "Done, Partial, or Failed" in TASK-003, the Phase 1 agent prompt, TASK-007, the Phase 2 agent prompt (outcome label values `done|partial|failed`), and RISK-001. State explicitly that `Partial` is terminal for pruning purposes.

---

### [REVISE-002] Retention cutoff column `last_seen` doesn't exist — use `updated_at`
**Phase**: 1
**Issue**: TASK-002 and the Phase 1 agent prompt say "delete incidents with `last_seen < olderThan`". `migrations/0001_init.sql` has `created_at` / `updated_at` (both TEXT via `datetime('now')`), no `last_seen`. ASSUMPTION-002 hedges ("the agent adapts"), but the schema was verifiable at planning time — a self-contained agent prompt shouldn't ship a known-wrong column name.
**Fix**: Change TASK-002 and the prompt to `updated_at < olderThan` (indexed via `idx_incidents_updated_at`). Note that timestamps are stored as SQLite `datetime('now')` strings, so the comparison value must be formatted the same way (see existing `fmtTimePtr`/`parseTime` helpers in `internal/store/sqlite.go`). Drop the hedge from ASSUMPTION-002.

---

### [SUGGEST-001] Intro says "four independent phases" — there are five
**Phase**: Frontmatter/intro
**Issue**: The summary paragraph predates Phase 5 (sonic removal) and now undercounts the plan.
**Fix**: Update to five phases and mention the dependency cleanup.

---

### [SUGGEST-002] Phase 3 prompt says "the mux" — the router is chi
**Phase**: 3
**Issue**: `internal/web/server.go` imports `github.com/go-chi/chi/v5`. Chi has first-class middleware (`r.Use(...)`), which is the natural place for the auth wrapper — "wrap the mux" undersells the idiomatic option and may lead the agent to hand-roll a wrapper.
**Fix**: In TASK-010 and the Phase 3 agent prompt, say "add chi middleware via `r.Use` in `Handler()`, mounted so `/healthz` (and `/login`) bypass it".

---

### [SUGGEST-003] `chat_messages` has no FK to incidents — prune must delete it explicitly
**Phase**: 1
**Issue**: `diagnoses` has a (non-cascading) FK; `chat_messages` (0003_chat.sql) has none at all. The plan's "respect existing FK/cascade behaviour" phrasing implies there might be cascades to lean on — there are none.
**Fix**: State plainly in TASK-002: one transaction, three explicit DELETEs (chat_messages, diagnoses, incidents), keyed on (namespace, name). No migration needed.

---

## What's Good

- Every phase's completion criteria are runnable commands (`go test`, `helm template`, `grep`), not vibes.
- Phase 5's placement is justified (it touches files phases 1–4 edit; last avoids rebase churn), and RISK-004 correctly anticipates the sonic↔stdlib behavioural edges (HTML escaping, SSE malformed-chunk skipping).
- CON-004 protects the generated `deploy/kscribe.yaml` from hand edits, and every knob defaults sanely (auth off, retention on — with the rationale stated).

## Machine-Readable Verdict

```yaml
verdict: Needs Revision
block: 0
revise: 2
suggest: 3
blocking_ids: []
```
