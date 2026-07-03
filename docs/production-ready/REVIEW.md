---
date: 2026-07-03
branch: production-ready
diff_base: main
reviewer: Claude
iteration: 1
---

# Code Review: production-ready (iteration 1)

Scope: `git diff main...HEAD` — 38 files, +1430/−93. Retention pruning, Prometheus metrics, dashboard auth, diagnosis rate limiting, sonic→stdlib JSON.

## Findings

### [MED-001] Terminal-mirror upsert refreshes `updated_at`, so SQLite rows can't age out while their CR exists
**File**: `internal/controller/kscribediagnosis_controller.go` (incidentFromDiagnosis) + `internal/store/sqlite.go` (UpsertIncident)
**Issue**: `incidentFromDiagnosis` never sets `UpdatedAt`, and `UpsertIncident` substitutes `time.Now()` for a zero value. The reconciler's terminal-mirror path upserts on every resync (default 10m), so an incident's `updated_at` is refreshed for as long as its CR exists. `Store.Prune` cuts on `updated_at < cutoff`, so SQLite rows effectively live ~2× the retention window (they only start aging after the CR itself is pruned). Not unbounded growth, but a silent deviation from REQ-001.
**Fix**: In `incidentFromDiagnosis`, set `UpdatedAt` from `Status.CompletedAt` when present — the mirror of a terminal CR hasn't actually changed, and dashboard ordering by completion time is more truthful anyway.

### [LOW-001] RFC3339Nano lexicographic comparison is imprecise at sub-second boundaries
**File**: `internal/store/sqlite.go` (Prune)
**Issue**: RFC3339Nano trims trailing fractional zeros, so string ordering can misorder timestamps within the same second (e.g. `.5Z` vs `.51Z`). For a day-scale retention cutoff the error window is <1s — immaterial — but the comment claims lexicographic order matches chronological order unconditionally.
**Fix**: Soften the comment to note the sub-second caveat, or format the cutoff with fixed-width fractions. Comment fix is enough.

### [LOW-002] PruneDiagnosisCRs swallows per-CR delete errors silently
**File**: `internal/controller/prune.go`
**Issue**: Before extraction, each failed `Delete` was logged; now it's a bare `continue`. A stuck finalizer or RBAC gap would fail invisibly forever.
**Fix**: Return or log the last error / an error count so the caller's `slog.Error` path sees it.

### [LOW-003] Login endpoint has no brute-force throttling
**File**: `internal/web/auth.go` (loginSubmit)
**Issue**: Unlimited token guesses at `POST /login`. Comparison is constant-time and the token is operator-chosen, but a weak token could be brute-forced over a fast link.
**Fix**: Acceptable for a ClusterIP-only MVP; note in README that the token should be high-entropy. Optional: small in-memory attempt limiter later.

## What's Good

- ADR-003 write-ordering is preserved through the metrics instrumentation; DiagnosesTotal counts only terminal transitions, so requeues can't double-count.
- Auth matrix tests cover the SSE/browser/bearer/cookie split including the /healthz bypass; comparison is `subtle.ConstantTimeCompare`; SameSite=Lax blocks cross-site POSTs to /chat.
- The sonic swap preserved the SSE skip-malformed-chunk behaviour and inverted the `TestNoEncodingJSON` guard into `TestNoSonic` instead of deleting it.
- Rate-limit denial keeps CRs Pending with jittered requeue — nothing is dropped, and the fake-client test asserts the provider is never called.

## Machine-Readable Verdict

```yaml
verdict: Request Changes
critical: 0
high: 0
medium: 1
low: 3
blocking_ids: [MED-001]
```
