---
date: 2026-07-03
feature: fix-dashboard-object-reason
coverage_before: not measured
coverage_after: 83.9%
---

# QA Report: fix-dashboard-object-reason

## Coverage

| File | Before | After |
| ---- | ------ | ----- |
| internal/controller | not measured | 78.1% |
| internal/store | not measured | 83.1% |
| internal/web | not measured | 94.1% |

## Tests Added

- `TestAlreadyExistsBackfillsMissingEventMetadata` — proves a duplicate event whose diagnosis CR already exists still backfills missing involved object and reason metadata.
- `TestReconcile_WithRealStore` metadata assertions — proves reconcile-to-SQLite preserves object kind, object name, object namespace, and reason for dashboard reads.
- `TestList` metadata assertions — proves the incident list HTML renders `Pod/my-pod (default)` and `BackOff`.
- `TestDetail` metadata assertions — proves the incident detail HTML renders the same object and reason.

## Remaining Gaps

- Existing SQLite rows that already contain empty object/reason metadata still render the existing `Not captured` fallback until their source CR is reconciled or recreated. The fix backfills existing CRs on duplicate event processing and preserves correct data for future mirrors.

## Manual Test Cases

- [ ] In a cluster with an existing KscribeDiagnosis missing spec metadata, replay or update the matching Warning Event and confirm the dashboard row changes from `Not captured` to the involved object and reason after reconciliation.
- [ ] Create a new Warning Event and confirm the list and detail pages show the object label and event reason.
