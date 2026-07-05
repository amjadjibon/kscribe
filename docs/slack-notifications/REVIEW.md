---
date: 2026-07-05
branch: slack-notifications
diff_base: main
reviewer: Claude
iteration: 1
---

# Code Review: slack-notifications (iteration 1)

Scope: `git diff main...HEAD` — Notification struct, Slack client, Multi fanout, Notifier interface refactor, config/chart wiring.

## Findings

### [LOW-001] Multi shares one 10s timeout across channels
**File**: `internal/controller/kscribediagnosis_controller.go` (notifyTerminal)
**Issue**: With email + Slack both enabled, both sends share the single 10s context; a 9s email send leaves Slack 1s. Sequential by design (bounded goroutines); worst case a channel misses a diagnosis notification and increments `failed`. Accepted — per-channel timeouts add plumbing for a rare, observable, non-critical miss.

### [LOW-002] Webhook URL validity is unchecked at startup
**File**: `cmd/kscribe/main.go`
**Issue**: A malformed `KSCRIBE_SLACK_WEBHOOK_URL` only surfaces on first send (logged + counted). Accepted — consistent with how the LLM key behaves; fail-at-use with observability.

## What's Good

- One Notifier interface owned by `notify` (controller aliases it) — no drift between definitions; SUGGEST-001 from plan review applied.
- Slack escaping uses Slack's own entity rules (`&<>`), not HTML escaping — a classic cross-format bug avoided and tested with hostile input.
- Multi attempts every channel and joins errors, proven by test; a Slack outage can't suppress email or vice versa.
- Zero new dependencies; both channels remain best-effort and detached from reconciles.

## Machine-Readable Verdict

```yaml
verdict: Approve
critical: 0
high: 0
medium: 0
low: 2
blocking_ids: []
```
