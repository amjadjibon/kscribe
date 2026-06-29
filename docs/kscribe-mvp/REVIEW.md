---
date: 2026-06-30
branch: kscribe-mvp-phase-8
reviewer: Claude
verdict: Approve
iteration: 2
---

# Code Review: kscribe-mvp (Iteration 2 — fix verification)

## Verdict

**Approve** — All iteration-1 High and Medium findings are fixed correctly and the fixes are
covered by tests that exercise the real failure paths. No regressions, races, or nil-derefs were
introduced. Three Low cleanups remain (a gofmt failure, a narrow duplicate-row edge in the new
recovery path, and an unverifiable image-USER assumption for the new securityContext) — none block merge.

## Summary

Re-reviewed `git diff 8ba9eb2 5ae2d0e` (243 insertions, 10 files) against the prior REVIEW.md.
`go build ./...`, `go vet`, and `go test ./internal/controller/... ./internal/web/...` all pass.
HIGH-001 (stranded `Diagnosing`), HIGH-002 (`AlreadyExists` storm), MED-001 (unbounded deduper),
MED-002 (no SSE publisher), MED-003 (no securityContext), and the two Low items (LOW-001 comment,
LOW-003 body truncation) are all resolved. LOW-002 remains a deliberate MVP deferral and is not
re-flagged.

## Fix Verification

- **HIGH-001 — FIXED.** The new `case DiagnosisPhaseDiagnosing` (controller:69-73) returns early when
  `Persisted`, else falls through to re-run. Primary scenario traced end-to-end: `Pending` →
  `Diagnosing` → `InsertDiagnosis` fails (sets `Persisted=false`, requeue+err, lines 191-203) →
  requeue re-enters with `Diagnosing && !Persisted` → re-runs → `InsertDiagnosis` succeeds → `Done`,
  `Persisted=true`. No duplicate `diagnoses` row in this path because the first insert failed (no row
  was written). `UpsertIncident` is `ON CONFLICT(namespace,name) DO UPDATE` (sqlite.go:110), so the
  re-run does not create a duplicate incident. No infinite tight loop: the retry is the bounded 30s
  `RequeueAfter` from ADR-003. Status writes are idempotent. Covered by
  `TestReconcile_SQLiteFailureThenRecovery`, which asserts `Diagnosing/!Persisted` then `Done/Persisted`.
  See LOW-001 below for the one narrow residual edge.
- **HIGH-002 — FIXED.** `event_watcher.go:115` now returns nil on `apierrors.IsAlreadyExists(err)` and
  still propagates every other error via `&& !apierrors.IsAlreadyExists(err)`. Correct. Covered by
  `TestAlreadyExistsIsSuccess`.
- **MED-001 — FIXED.** `dedup.go:36-43` sweeps expired entries when `len(d.seen) >= 1024`, inside the
  already-held `d.mu` lock (single critical section, no race), using the injected `d.now()` seam.
  Threshold logic is correct (sweep-then-insert). Covered by `TestDeduper_SweepEvictsExpired`.
- **MED-002 — FIXED.** `Publisher` interface lives in the controller package (controller:30-32) and
  `*web.Broker` is adapted via `brokerPublisher` in main.go — controller never imports web, so no
  import cycle. `publish()` is nil-safe (controller:38-42) and the reconciler defaults `Publisher` to
  nil. The published id `req.Namespace+"/"+req.Name` exactly matches the stream subscribe key
  `id := ns + "/" + name` (server.go:75). The adapter maps to `web.Event{HTML: html}` correctly.
  Covered by `TestReconcile_PublishesOnSuccess`.
- **MED-003 — FIXED.** Container `securityContext` (runAsNonRoot, allowPrivilegeEscalation:false,
  caps drop ALL, seccompProfile RuntimeDefault) plus pod `fsGroup: 65532` are present and identical in
  both `config/manager/deployment.yaml` and the regenerated `deploy/kscribe.yaml`. Valid YAML.
  `readOnlyRootFilesystem` is intentionally omitted with an inline upgrade note. See LOW-003 caveat.
- **LOW-001 (config comment) — FIXED.** The `RedactEnabled` comment now states redaction is always-on
  and the flag is audit metadata (config.go:38-40).
- **LOW-003 (error body) — FIXED.** `openai.go:61` truncates via `io.LimitReader(resp.Body, 512)`.
- **LOW-002** — remains a deliberate MVP deferral; not re-flagged.

## Findings

### [LOW-001] HIGH-001 recovery can write a duplicate diagnoses row + second LLM call in a narrow window *(Low)*
**File**: `internal/controller/kscribediagnosis_controller.go:191-241`; `internal/store/sqlite.go:141-155`
**Category**: Correctness
**Issue**: The recovery branch re-runs the *entire* diagnosis path. In the primary failure mode
(`InsertDiagnosis` itself fails) this is harmless — no row was written. But in the secondary mode where
`InsertDiagnosis` *succeeds* and the subsequent final `r.Status().Update(... Done)` (line 241) fails,
the CR is left `Diagnosing` with the API server still showing `Persisted=false`. The requeue then
re-enters `Diagnosing && !Persisted` and runs again: a **second LLM call** (cost) and a **second
`InsertDiagnosis`**. `diagnoses` is an append-only history table (plain INSERT, non-unique
`idx_diagnoses_incident`), and `GetIncident` reads all rows `ORDER BY created_at ASC` (sqlite.go:204-207),
so the detail view would show two diagnosis entries for one incident. This is at-least-once semantics
consistent with ADR-003 and strictly better than iteration-1's "stranded forever," but it is a new
(narrow) consequence of the fix.
**Fix**: Acceptable for MVP as-is. If undesired, either (a) re-fetch and short-circuit when a diagnoses
row already exists for `(namespace,name)` before re-inserting, or (b) make `InsertDiagnosis` an upsert
keyed on `(namespace,name)`. Low priority — only triggers on the InsertDiagnosis-succeeds-then-CR-update-fails race.

### [LOW-002] dedup.go fails gofmt (const block alignment) *(Low)*
**File**: `internal/controller/dedup.go:10-13`
**Category**: Simplicity / Hygiene
**Issue**: `gofmt -l` flags the file: the new `const (...)` block is misaligned
(`defaultDedupTTL   =` has an extra space vs `dedupSweepThresh =`). A CI `gofmt`/`gofmt -l` gate would
fail on this.
**Fix**: Run `gofmt -w internal/controller/dedup.go` (removes one space so the `=` columns align).

### [LOW-003] securityContext runAsNonRoot assumes a non-root image USER (unverifiable here) *(Low)*
**File**: `config/manager/deployment.yaml:50-52`; `deploy/kscribe.yaml:565-567`
**Category**: Security / Ops
**Issue**: `runAsNonRoot: true` is set without a `runAsUser`, and `fsGroup: 65532` implies the distroless
nonroot UID. There is no `Dockerfile` in the repo to confirm the image declares `USER 65532` (or any
non-root USER). If the published image runs as root, the kubelet will refuse to start the pod
("container has runAsNonRoot and image will run as root"). Cannot be verified from this diff.
**Fix**: Confirm the image sets a non-root `USER` (ideally 65532 to match `fsGroup`), or add an explicit
`runAsUser: 65532` to the container securityContext to make the contract self-contained.

## What's Good

- The new tests are real behavioural tests, not always-pass: `TestReconcile_SQLiteFailureThenRecovery`
  fails the store once and asserts the `Diagnosing/!Persisted` → `Done/Persisted` transition; the
  AlreadyExists and sweep tests assert the actual post-conditions.
- MED-002 was solved without an import cycle by defining the one-method `Publisher` interface in the
  consumer package and keeping the `web` dependency confined to a 1-line adapter in main.go.
- The MED-001 sweep reuses the existing critical section — no second lock, no background goroutine —
  the minimal correct fix.

## Pre-Merge Checklist

- [x] HIGH-001 resolved — Diagnosing/unpersisted CRs recover to Done (test-covered)
- [x] HIGH-002 resolved — `AlreadyExists` treated as success; genuine errors still propagate
- [x] MED-001 resolved — deduper sweeps under-lock with injectable clock
- [x] MED-002 resolved — Publisher wired, nil-safe, id matches route key, no import cycle
- [x] MED-003 resolved — securityContext in both source and generated manifests
- [x] `go build`, `go vet`, `go test` (controller + web) pass
- [ ] `gofmt -w internal/controller/dedup.go` (LOW-002)

## Machine-Readable Verdict

```yaml
verdict: Approve
critical: 0
high: 0
medium: 0
low: 3
info: 0
blocking_ids: []
```
