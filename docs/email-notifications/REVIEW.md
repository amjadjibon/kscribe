---
date: 2026-07-04
branch: email-notifications
diff_base: main
reviewer: Claude
iteration: 1
---

# Code Review: email-notifications (iteration 1)

Scope: `git diff main...HEAD` — notify package, reconciler hook, wiring, chart, docs.

## Findings

### [LOW-001] Default `notifications.from` is an unverifiable placeholder
**File**: `charts/kscribe/values.yaml`
**Issue**: Resend rejects senders from unverified domains, so the default `kscribe@notifications.local` fails until overridden — surfaced only via logs/metrics.
**Fix**: Applied during review — values.yaml comment now states the domain-verification requirement and where failures surface.

### [LOW-002] Provider outages email on every failed diagnosis
**File**: `internal/controller/kscribediagnosis_controller.go`
**Issue**: A dead LLM key produces a Failed diagnosis (and email) per event, up to `maxDiagnosesPerHour` (30/h default). Bounded and arguably desirable (you want to know), so accepted without a separate notification limiter (CON-002).

## What's Good

- Email content uses only redacted RCA fields; every interpolation is HTML-escaped and covered by a test with hostile input.
- The send is fully detached (own context/timeout), so a slow Resend can never hold a reconcile; error path is metric-counted and the test proves reconcile success despite notifier failure.
- Send placement is after the SQLite persist succeeds, so a storage-error requeue doesn't emit an email for an RCA that was never recorded.
- Zero new dependencies; the client is ~50 lines of stdlib.

## Machine-Readable Verdict

```yaml
verdict: Approve
critical: 0
high: 0
medium: 0
low: 2
blocking_ids: []
```
