---
goal: Fix dashboard object and reason display
version: 1.0
date_created: 2026-07-03
last_updated: 2026-07-03
owner: Codex
status: 'Planned'
tags: [bug]
---

# Fix Dashboard Object And Reason Display

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

The dashboard should show the Kubernetes involved object and event reason anywhere an incident appears. This plan adds regression coverage for the real reconcile/store/web path, then fixes the smallest broken link in that path.

## 1. Requirements & Constraints

- **REQ-001**: The incident list must render each incident's involved object and reason when they are present in persisted incident data.
- **REQ-002**: The incident detail overview and raw field sections must render the same involved object and reason without falling back to "Not captured" when the data exists.
- **CON-001**: Keep the fix in the existing Go/templ/store architecture; do not add dependencies.
- **CON-002**: Preserve existing fallback text for rows that genuinely lack object or reason metadata.

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` and commit. No `Co-authored-by:`. Tick `[x]` as each task completes.

### Phase 1: Restore Dashboard Metadata

**Goal**: Add focused regression coverage and fix the dashboard metadata propagation/rendering path in one narrow change.

- [ ] TASK-001: Add an integration assertion in `internal/controller/reconciler_store_integration_test.go` that a reconciled incident returned by `ListIncidents` includes `InvolvedObjectKind`, `InvolvedObjectName`, `InvolvedObjectNamespace`, and `Reason`.
- [ ] TASK-002: Add web-render assertions in `internal/web/server_test.go` proving the incident list and detail page include `Pod/my-pod` and `BackOff` for seeded incident metadata.
- [ ] TASK-003: Fix the minimal code path in `internal/controller/kscribediagnosis_controller.go`, `internal/store/sqlite.go`, or `internal/web/templates/incidents.templ` that prevents present object/reason metadata from appearing.
- [ ] TASK-004: Regenerate `internal/web/templates/incidents_templ.go` with `templ generate` if the templ source changes.

**Completion criteria**: `go test ./internal/controller ./internal/store ./internal/web`

**git commit**: `git add -u && git commit -m "fix: show dashboard object and reason"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of fix-dashboard-object-reason.

Context: The kscribe dashboard lists persisted incidents from SQLite and should show the Kubernetes involved object plus event reason. The templates contain object/reason UI, but the user reports those values are not showing.

Branch: fix-dashboard-object-reason/phase-1  |  Base: main

Tasks:
- TASK-001: Add an integration assertion in internal/controller/reconciler_store_integration_test.go that a reconciled incident returned by ListIncidents includes InvolvedObjectKind, InvolvedObjectName, InvolvedObjectNamespace, and Reason.
- TASK-002: Add web-render assertions in internal/web/server_test.go proving the incident list and detail page include Pod/my-pod and BackOff for seeded incident metadata.
- TASK-003: Fix the minimal code path in internal/controller/kscribediagnosis_controller.go, internal/store/sqlite.go, or internal/web/templates/incidents.templ that prevents present object/reason metadata from appearing.
- TASK-004: Regenerate internal/web/templates/incidents_templ.go with templ generate if the templ source changes.

Key files:
- internal/controller/kscribediagnosis_controller.go — mirrors KscribeDiagnosis spec/status into store.Incident.
- internal/store/sqlite.go — persists and reads incident metadata for the dashboard.
- internal/web/server.go — serves incident list/detail views.
- internal/web/templates/incidents.templ — renders object/reason fields.
- internal/controller/reconciler_store_integration_test.go — end-to-end reconcile to SQLite coverage.
- internal/web/server_test.go — rendered dashboard HTML coverage.

Completion criteria: go test ./internal/controller ./internal/store ./internal/web

When done: git add -u && git commit -m "fix: show dashboard object and reason" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

## 3. Testing

- [ ] TEST-001: `go test ./internal/controller ./internal/store ./internal/web`

## 4. Risks & Assumptions

- **ASSUMPTION-001**: The dashboard's source of truth is SQLite incident rows, not live Kubernetes API reads.
- **ASSUMPTION-002**: The desired object label format is the existing `Kind/name` display from `incidentObjectLabel`.
- **RISK-001**: The issue may involve stale rows already persisted without metadata; mitigation: preserve existing fallback behavior and ensure all future reconcile mirrors keep metadata.
