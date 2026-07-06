---
goal: Create Jira and Linear tickets on diagnosis completion
version: 1.0
date_created: 2026-07-06
last_updated: 2026-07-06
owner: kscribe
status: 'In progress'
tags: [feature]
---

# Ticket Notifications (Jira / Linear)

![Status: In Progress](https://img.shields.io/badge/status-In%20Progress-yellow)

Adds two new `notify.Notifier` implementations — Jira Cloud and Linear — so a
finished `KscribeDiagnosis` can open a ticket, exactly like the existing
Slack/Resend notifiers. Both are optional and independently enabled by config.

## 1. Requirements & Constraints

- **REQ-001**: `internal/notify/jira.go` implements `Notifier` via Jira Cloud REST API v3 (`POST {BaseURL}/rest/api/3/issue`), basic auth (email + API token).
- **REQ-002**: `internal/notify/linear.go` implements `Notifier` via Linear GraphQL API (`POST https://api.linear.app/graphql`, `issueCreate` mutation), `Authorization: <APIKey>` header (no `Bearer` prefix — Linear convention).
- **REQ-003**: Both notifiers are wired into `cmd/kscribe/main.go` alongside Resend/Slack, added to `notifiers` slice and `channels` log list, each gated on its own required config being non-empty.
- **SEC-001**: `JiraAPIToken` and `LinearAPIKey` are credentials — `envDefault:""`, comment "never logged", and error bodies capped at 512 bytes like existing notifiers.
- **CON-001**: Stdlib only (`net/http`, `encoding/json`) — no new dependency, matching Slack/Resend.

## 2. Implementation Steps

> After completing all tasks, `git add -u` and commit. No `Co-authored-by:`. Tick `[x]` as each task completes.

### Phase 1: Jira and Linear notifiers

**Goal**: Add both ticket-creation notifiers, config, wiring, and tests in one pass — they share the same interface, config file, and wiring point, so splitting them would just be churn.

- [ ] TASK-001: Add `internal/notify/jira.go`: `Jira` struct `{BaseURL, Email, APIToken, ProjectKey string; HTTPClient *http.Client}`. `Notify` builds an issue summary/description from `Notification` (reuse the `[kscribe] Phase: Reason ns/obj` convention from `Slack.Notify`/`Subject`), POSTs `{"fields":{"project":{"key":ProjectKey},"summary":...,"description":{"type":"doc","version":1,"content":[...]},"issuetype":{"name":"Task"}}}` to `BaseURL+"/rest/api/3/issue"` with `req.SetBasicAuth(Email, APIToken)`. Default 10s-timeout `http.Client` if nil. Non-2xx → error with status + body capped at 512 bytes, same pattern as `resend.go`/`slack.go`.
- [ ] TASK-002: Add `internal/notify/linear.go`: `Linear` struct `{APIKey, TeamID string; HTTPClient *http.Client}`. `Notify` POSTs a GraphQL `issueCreate` mutation (`{"query": "...", "variables": {"input": {"teamId": TeamID, "title": ..., "description": ...}}}`) to `https://api.linear.app/graphql` with header `Authorization: <APIKey>` (raw key, Linear does not use `Bearer`). Same default-client and error-truncation pattern. Treat a GraphQL response body containing `"errors"` as a failure too (GraphQL returns 200 on partial errors).
- [ ] TASK-003: In `internal/config/config.go`, add fields after `SlackWebhookURL`: `JiraBaseURL`, `JiraEmail`, `JiraAPIToken` (SEC-001 comment), `JiraProjectKey`, `LinearAPIKey` (SEC-001 comment), `LinearTeamID` — all `envDefault:""`.
- [ ] TASK-004: In `cmd/kscribe/main.go`, after the Slack block (~line 229), add: if `cfg.JiraBaseURL != "" && cfg.JiraProjectKey != ""` append `&notify.Jira{...}` and `"jira"` to channels; if `cfg.LinearAPIKey != "" && cfg.LinearTeamID != ""` append `&notify.Linear{...}` and `"linear"` to channels.
- [ ] TASK-005: Add `internal/notify/jira_test.go` and `internal/notify/linear_test.go` following `resend_test.go` conventions: `httptest.NewServer` capturing the request, assert auth header/basic-auth, assert body fields, assert error-status handling with truncation, assert GraphQL-errors-in-200 handling for Linear.

**Completion criteria**: `go build ./... && go test ./internal/notify/... ./internal/config/... ./cmd/...` pass.

**git commit**: `git add -u && git commit -m "feat: create jira and linear tickets on diagnosis completion"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of ticket-notifications (the only phase).

Context: kscribe is a Kubernetes operator that diagnoses failing workloads with an
LLM and notifies operators on completion. internal/notify/ defines a Notifier
interface (Notify(ctx, Notification) error) with existing implementations Slack
(internal/notify/slack.go), Resend/email (internal/notify/resend.go, email.go),
and a fan-out helper Multi (internal/notify/multi.go). This phase adds two more
notifiers, Jira and Linear, that open a ticket instead of/alongside messaging.

Branch: ticket-notifications/phase-1  |  Base: main

Tasks:
- TASK-001: Add internal/notify/jira.go. Struct Jira{BaseURL, Email, APIToken, ProjectKey string; HTTPClient *http.Client}. Notify(ctx, n Notification) error builds a summary via the same "[kscribe] Phase: Reason ns/obj" convention used in slack.go/resend.go (check for a shared Subject() helper in resend.go before duplicating it), and a description including Summary/RootCause/Remediation. POST JSON to BaseURL+"/rest/api/3/issue" using Jira's Atlassian Document Format for the description field: {"fields":{"project":{"key":ProjectKey},"summary":<summary>,"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":<body>}]}]},"issuetype":{"name":"Task"}}}. Use req.SetBasicAuth(Email, APIToken). Default HTTPClient to &http.Client{Timeout: 10*time.Second} when nil. On status >= 300, read up to 512 bytes of body (io.LimitReader) and return an error including the status code and body, matching the exact style in internal/notify/resend.go's Send method and internal/notify/slack.go's Notify method.
- TASK-002: Add internal/notify/linear.go. Struct Linear{APIKey, TeamID string; HTTPClient *http.Client}. Notify(ctx, n Notification) error POSTs to https://api.linear.app/graphql a JSON body {"query": "mutation IssueCreate($input: IssueCreateInput!) { issueCreate(input: $input) { success } }", "variables": {"input": {"teamId": TeamID, "title": <summary>, "description": <body>}}}. Set header Authorization to the raw APIKey (no "Bearer " prefix - this is Linear's convention, different from Slack/Resend). Same default-client and 512-byte-truncated-error pattern as Jira/Slack/Resend. Because GraphQL returns HTTP 200 even when the mutation fails, after a successful HTTP call also decode the response body and check for a top-level "errors" array; if present and non-empty, return an error containing the response body (capped at 512 bytes).
- TASK-003: In internal/config/config.go, add after the SlackWebhookURL field (around line 82): JiraBaseURL, JiraEmail, JiraAPIToken (comment "SEC-001: never logged"), JiraProjectKey string fields with env tags KSCRIBE_JIRA_BASE_URL, KSCRIBE_JIRA_EMAIL, KSCRIBE_JIRA_API_TOKEN, KSCRIBE_JIRA_PROJECT_KEY (all envDefault:""); and LinearAPIKey (comment "SEC-001: never logged"), LinearTeamID string fields with env tags KSCRIBE_LINEAR_API_KEY, KSCRIBE_LINEAR_TEAM_ID (envDefault:"").
- TASK-004: In cmd/kscribe/main.go, in the notifier-wiring block (look for the `if cfg.SlackWebhookURL != ""` block, around line 226-229), add two more blocks after it: `if cfg.JiraBaseURL != "" && cfg.JiraProjectKey != ""` append &notify.Jira{BaseURL: cfg.JiraBaseURL, Email: cfg.JiraEmail, APIToken: cfg.JiraAPIToken, ProjectKey: cfg.JiraProjectKey} to notifiers and "jira" to channels; `if cfg.LinearAPIKey != "" && cfg.LinearTeamID != ""` append &notify.Linear{APIKey: cfg.LinearAPIKey, TeamID: cfg.LinearTeamID} to notifiers and "linear" to channels.
- TASK-005: Add internal/notify/jira_test.go and internal/notify/linear_test.go. Follow the exact conventions in internal/notify/resend_test.go (httptest.NewServer, capture request body/headers, assert on them, defer srv.Close(), use BaseURL override to point at the test server). Cover: success path (assert basic auth / Authorization header and request body fields), non-2xx error path with truncation assertion (error message under ~600 bytes), and for Linear specifically a 200-OK-but-GraphQL-errors-in-body case that must still return an error.

Key files:
- internal/notify/notification.go — the Notification struct and Notifier interface, do not change
- internal/notify/slack.go, internal/notify/resend.go — the patterns to mirror exactly (error truncation, default HTTP client, escaping)
- internal/notify/resend_test.go — the test conventions to mirror
- internal/config/config.go — where to add the new fields
- cmd/kscribe/main.go — the notifier wiring block (~lines 216-239)

Completion criteria: go build ./... && go test ./internal/notify/... ./internal/config/... ./cmd/... pass.

When done: git add -u && git commit -m "feat: create jira and linear tickets on diagnosis completion" — no Co-authored-by
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

## 3. Testing

- [ ] TEST-001: `internal/notify/jira_test.go` — success POST, basic-auth header, error truncation.
- [ ] TEST-002: `internal/notify/linear_test.go` — success POST, `Authorization` header (no Bearer), HTTP-200-with-GraphQL-errors treated as failure, error truncation.
- [ ] TEST-003: `go build ./...` and `go vet ./...` clean.

## 4. Risks & Assumptions

- **ASSUMPTION-001**: Jira issue type is hardcoded to `"Task"` — matches the simplicity of existing notifiers (no per-channel config proliferation); revisit if a real deployment needs a configurable issue type.
- **ASSUMPTION-002**: No dedup/rate-limiting for ticket creation beyond what already exists at the diagnosis level (`RateLimiter` in the controller) — creating a ticket per diagnosis completion is the same cardinality as the existing Slack/email notifications.
- **RISK-001**: Linear returns 200 with an `errors` array on validation failures (e.g. bad `teamId`) — mitigated by TASK-002's explicit body-error check.
