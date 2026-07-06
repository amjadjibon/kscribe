---
date: 2026-07-06
branch: ticket-notifications
reviewer: Claude
verdict: Request Changes
---

# Code Review: ticket-notifications

## Summary

Reviewed `internal/notify/jira.go`, `internal/notify/linear.go`, their tests, `internal/config/config.go`, and the wiring in `cmd/kscribe/main.go`. The code closely mirrors the existing Slack/Resend notifiers in structure, error handling, and credential hygiene. One correctness bug in the Linear GraphQL-error detection can cause a silent false "success" for large error payloads.

## Findings

### [MED-001] Linear GraphQL error detection silently no-ops on responses over 512 bytes *(Medium)*
**File**: `internal/notify/linear.go:87-95`
**Category**: Correctness
**Issue**: The response body is read through `io.LimitReader(resp.Body, 512)` before being passed to `json.Unmarshal`. If Linear's GraphQL error response (HTTP 200 with an `errors` array) is longer than 512 bytes — plausible for validation errors that echo back input, or errors with multiple entries — the truncated bytes are not valid JSON, `json.Unmarshal` returns a non-nil error, and the `err == nil && len(lr.Errors) > 0` guard is false. `Notify` then returns `nil`, reporting success even though the ticket was never created. This defeats the purpose of TASK-002's explicit "treat GraphQL errors as failure" requirement for exactly the payloads most likely to trip it.
**Fix**: Read a larger cap for the GraphQL-error check (e.g. 8KB, generous for a JSON error envelope) separately from the 512-byte cap used for the truncated *error message* shown to the operator:
```go
rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
if resp.StatusCode >= 300 {
    return fmt.Errorf("linear error %d: %s", resp.StatusCode, truncate(rb, 512))
}
var lr linearResponse
if err := json.Unmarshal(rb, &lr); err == nil && len(lr.Errors) > 0 {
    return fmt.Errorf("linear graphql error: %s", truncate(rb, 512))
}
```
(or simplest: just bump the single `io.LimitReader` cap to something JSON-safe like 4096-8192 bytes — the existing tests don't distinguish between the two use cases, so a single larger limit is the smaller diff.)

---

## What's Good

- Both notifiers reuse `Subject` and the new shared `plainBody` helper rather than duplicating the "[kscribe] Phase: Reason ns/obj" formatting — no copy-paste from `slack.go`/`resend.go`.
- Credential handling matches the codebase convention exactly: `SetBasicAuth` for Jira, raw `Authorization` header for Linear (correctly noted as *not* using `Bearer`, which would silently fail against Linear's API), and both are marked `SEC-001: never logged` in `config.go`.
- `TestLinearNotifyGraphQLErrorsIn200` shows the right instinct — it's testing exactly the sharp edge of this feature (GraphQL's HTTP-200-on-error behavior) — the test body is just small enough (42 bytes) that it doesn't happen to trip MED-001.

## Pre-Merge Checklist

**Always:**
- [ ] All Critical and High findings resolved (none — only a Medium)
- [x] No secrets or credentials in committed files
- [x] `.gitignore` covers new artifact/config types (no new artifact types introduced)
- [x] Tests cover changed behaviour and at least one unhappy path
- [x] All async calls awaited or errors handled (Go: no goroutines introduced, errors handled)
- [x] Resources closed in all code paths (`resp.Body` closed via `defer` in both notifiers)

## Machine-Readable Verdict

```yaml
verdict: Request Changes
critical: 0
high: 0
medium: 1
low: 0
info: 0
blocking_ids: []
```
