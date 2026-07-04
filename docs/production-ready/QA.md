# QA Report: production-ready

Date: 2026-07-03 · Branch: `production-ready` · Iteration 1

## Coverage (internal packages, `go test -cover`)

| Package | Coverage | Notes |
|---------|----------|-------|
| config | 100% | includes new RetentionPeriod / MetricsAddr / DashboardToken / MaxDiagnosesPerHour fields |
| metrics | 100% | testutil.CollectAndCount over all four collectors |
| web | 95.0% | auth middleware, login flow, bearer/cookie matrix, /healthz bypass all covered |
| store | 82.0% | Prune 75% (uncovered lines are tx-begin/commit error branches) |
| controller | 80.5% | RateLimiter 100%; throttled-reconcile path covered; PruneDiagnosisCRs covered |
| agent | 85.7% | unchanged by this feature apart from mechanical JSON swap |
| enricher | 54.9% | pre-existing; TestNoSonic guards the stdlib-JSON rule |
| web/templates | 0.3% | pre-existing generated templ code; out of scope |

## Feature-path checks

- **Retention**: `TestPrune` (store — old incident + diagnoses + chat deleted in one tx, recent survives), `TestPruneDiagnosisCRs` (controller — old Done deleted; recent Done and old Pending survive).
- **Metrics**: `TestCollectorsRegistered` (all collectors emit series; token counter value asserted).
- **Auth**: `TestAuthDisabledByDefault`, `TestAuthRequired` (401 for SSE, 303→/login for browser, /healthz open), `TestAuthBearer`, `TestLoginFlow` (bad token 401, good token sets HttpOnly cookie, cookie grants access), `TestLoginFormServed`.
- **Rate limit**: `TestRateLimiterWindow` (N allowed, N+1 denied, recovery after window), `TestRateLimiterDisabled` (0/nil never denies), `TestReconcile_RateLimited` (stays Pending, RequeueAfter 2–5m, store untouched, RateLimited condition set).
- **stdlib JSON**: full suite green after the swap — existing round-trip tests (enricher, agent, store, viewmodel) prove behavioural equivalence; `TestNoSonic` prevents regression.

## Changes made during QA

- Extracted CR-deletion logic from `cmd/kscribe/main.go` into `controller.PruneDiagnosisCRs` so the destructive path is unit-testable; added `TestPruneDiagnosisCRs`.

## Accepted gaps

- `runPruner` ticker loop in `package main` (thin logging shell around two tested functions) — not worth a main-package test harness.
- Manual e2e (kind cluster: metrics curl, dashboard 401, prune log line) deferred to TEST-005 in the plan; not runnable in this environment.

## Verdict

All 8 packages pass `go test ./... -count=1`. New feature code is at or near full coverage; remaining gaps are pre-existing or trivial shells.
