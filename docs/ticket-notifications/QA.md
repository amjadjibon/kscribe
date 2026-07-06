---
date: 2026-07-06
feature: ticket-notifications
coverage_before: 86.7%
coverage_after: 86.8%
---

# QA Report: ticket-notifications

## Coverage

| File | Before | After |
| ---- | ------ | ----- |
| internal/notify/jira.go | n/a (new file) | 85.0% (Notify), 100% (plainBody) |
| internal/notify/linear.go | n/a (new file) | 84.6% (Notify) |
| internal/notify (package total) | 86.7% | 86.8% |

## Tests Added

Written during implementation, reviewed here for adequacy against the existing per-notifier convention (Slack: 88.9%, Resend.Send: 81.8%):

- `TestJiraNotify` — happy path: project key, summary line (`Subject` format), ADF description body (Summary/Root cause/Remediation), Basic Auth header (email + API token), request path.
- `TestJiraNotifyErrorTruncated` — non-2xx response surfaces status code and truncates body to ≤600 bytes in the error message.
- `TestLinearNotify` — happy path: `teamId`, `title` (`Subject` format), plain-text `description`, raw `Authorization` header (no `Bearer` prefix, per Linear's convention).
- `TestLinearNotifyGraphQLErrorsIn200` — HTTP 200 with a GraphQL `errors` array is treated as a failure (this is the sharpest edge case for this feature — GraphQL doesn't use HTTP status codes for mutation failures).
- `TestLinearNotifyErrorTruncated` — non-2xx response surfaces status code and truncates body.

## Remaining Gaps

- `internal/notify/jira.go` / `linear.go`, the `hc.Do` network-failure branch (connection refused, DNS failure) is untested — same untested gap already present in `slack.go`/`resend.go` for the identical branch. Not a regression; consistent with existing project convention of not simulating transport-level failures.
- `json.Marshal`/`http.NewRequestWithContext` error branches are unreachable with well-formed struct literals and a valid URL string, so they're untestable without contrived inputs — same as existing notifiers.
- `cmd/kscribe/main.go` notifier-wiring block (the `if cfg.JiraBaseURL != ""` / `if cfg.LinearAPIKey != ""` conditionals) has no test — `cmd/kscribe` has no test files at all, matching the existing Slack/Resend wiring which is also untested at that layer.

## Manual Test Cases

- [ ] Point `KSCRIBE_JIRA_BASE_URL`/`KSCRIBE_JIRA_EMAIL`/`KSCRIBE_JIRA_API_TOKEN`/`KSCRIBE_JIRA_PROJECT_KEY` at a real Jira Cloud site and confirm an issue is created with the expected summary/description on a diagnosis completion.
- [ ] Point `KSCRIBE_LINEAR_API_KEY`/`KSCRIBE_LINEAR_TEAM_ID` at a real Linear workspace and confirm an issue is created in the target team.
