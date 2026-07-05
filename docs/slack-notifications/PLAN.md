---
goal: Slack notifications for finished diagnoses via incoming webhook
version: 1.0
date_created: 2026-07-05
last_updated: 2026-07-05
owner: amjadjibon
status: 'Planned'
tags: [feature]
---

# Slack Notifications

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

Post the RCA to a Slack channel via an incoming webhook when a diagnosis finishes, alongside (or instead of) the existing Resend email notifications. Off by default; enabled by setting a webhook URL.

## 1. Requirements & Constraints

- **REQ-001**: On terminal phase transition, post one Slack message per diagnosis with phase, reason, object, summary, root cause, remediation.
- **REQ-002**: Best-effort like email — failures logged + counted in `kscribe_notifications_total`, never block reconciles. Email and Slack are independent: one failing must not stop the other.
- **REQ-003**: Off by default; enabled when `KSCRIBE_SLACK_WEBHOOK_URL` is set. Composes with email when both are configured.
- **SEC-001**: The webhook URL is a credential — Secret-backed in the chart, never logged. Message uses redacted RCA fields only.
- **CON-001**: No new dependency — incoming webhook is one HTTPS POST of `{"text": ...}` (mrkdwn).
- **CON-002**: Refactor `Notifier` to a structured payload (`notify.Notification`) so each channel renders its own format; reconciler builds the payload once. Keep the change surface small — email behaviour identical.
- **CON-003**: `deploy/kscribe.yaml` generated from the chart; run `scripts/build-manifest.sh`.

## 2. Implementation Steps

> After each phase: `git add -u` (+ explicit new files), commit. No `Co-authored-by:`. Tick `[x]` as tasks complete.

### Phase 1: Notification struct, Slack client, fanout

**Goal**: Structured notification pipeline with Slack + email renderers, fully tested, unwired.

- [ ] TASK-001: `internal/notify/notification.go` — `type Notification struct { Phase, Reason, Namespace, Object, Summary, RootCause string; Remediation []string }`.
- [ ] TASK-002: Change `Resend.Notify` to `Notify(ctx, n Notification) error` rendering subject/HTML internally (reuse `Subject`/`HTML`). Update `internal/controller` `Notifier` interface + `notifyTerminal` to build a `Notification`; update reconciler test's fake.
- [ ] TASK-003: `internal/notify/slack.go` — `type Slack struct { WebhookURL string; HTTPClient *http.Client }` with `Notify(ctx, n Notification) error` POSTing `{"text": <mrkdwn>}`; mrkdwn body mirrors the email content (`*[kscribe] Failed: OOMKilling prod/worker-1*` + fields). Non-2xx → error with status + ≤512B body. Escape Slack control entities (`&`, `<`, `>`).
- [ ] TASK-004: `internal/notify/multi.go` — `Multi(notifiers ...Notifier) Notifier` fan-out that calls every notifier and joins errors (`errors.Join`); define `type Notifier interface` in notify so controller can alias it.
- [ ] TASK-005: Tests — slack payload/escaping/error-truncation via httptest; Multi delivers to all despite one failing.

**Completion criteria**: `go test ./internal/notify/ ./internal/controller/` passes; `go build ./...` green.

**git commit**: `git add internal/notify/ && git add -u && git commit -m "feat: structured notifications with slack webhook and fanout"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of slack-notifications for kscribe,
a Go Kubernetes operator. It already emails RCA results via internal/notify
(Resend, Notify(ctx, subject, html string)). Add Slack and restructure.

Branch: slack-notifications-phase-1  |  Base: slack-notifications

Tasks:
- TASK-001: internal/notify/notification.go — Notification struct: Phase,
  Reason, Namespace, Object, Summary, RootCause string; Remediation []string.
- TASK-002: Resend.Notify(ctx, n Notification) error — render via existing
  Subject()/HTML(). Update the Notifier interface in
  internal/controller/kscribediagnosis_controller.go (notifyTerminal builds
  one Notification) and the fakeNotifier in its test.
- TASK-003: internal/notify/slack.go — Slack{WebhookURL string, HTTPClient
  *http.Client (10s default)} Notify POSTs {"text": mrkdwn} to WebhookURL.
  mrkdwn: bold header "[kscribe] <Phase>: <Reason> <ns>/<obj>", then
  Summary / Root cause / numbered remediation. Escape & < > per Slack.
  Non-2xx: error with status and ≤512 bytes of body.
- TASK-004: internal/notify/multi.go — type Notifier interface { Notify(
  ctx, n Notification) error }; Multi(...Notifier) Notifier calling all,
  errors.Join of failures.
- TASK-005: tests: httptest for slack (payload text contains escaped
  fields, auth-free POST, error truncation); Multi: one failing notifier
  doesn't stop the second (both called), joined error returned.

Key files:
- internal/notify/{resend,email}.go, internal/controller/
  kscribediagnosis_controller.go (notifyTerminal, Notifier),
  kscribediagnosis_controller_test.go (fakeNotifier)

Completion criteria: go test ./internal/notify/ ./internal/controller/
passes; go build ./... green.

When done: git add internal/notify/ && git add -u &&
git commit -m "feat: structured notifications with slack webhook and fanout"
— no Co-authored-by. One-paragraph summary + SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 2: Config, wiring, chart

**Goal**: Deployable — Slack fires in-cluster, composing with email.

**Depends on**: Phase 1 complete

- [ ] TASK-006: `internal/config/config.go` — `SlackWebhookURL` (`KSCRIBE_SLACK_WEBHOOK_URL`, default "").
- [ ] TASK-007: `cmd/kscribe/main.go` — build a notifier list (Resend when key+recipients; Slack when webhook set); 0 → nil, 1 → it, 2+ → `notify.Multi(...)`. Log which channels are enabled (names only).
- [ ] TASK-008: Chart — `notifications.slack.webhookUrl` / `existingSecret` / `existingSecretKey` (`slack-webhook-url`), Secret + helpers + deployment env (secretKeyRef, guarded); regenerate manifest; rows in chart README + main README production table.
- [ ] TASK-009: Reconciler notify test still passes (interface changed in Phase 1); add a Multi-in-reconciler smoke via the fake if not already covered.

**Completion criteria**: `go test ./...` passes; `helm template charts/kscribe --set notifications.slack.webhookUrl=https://hooks.slack.com/x` renders `KSCRIBE_SLACK_WEBHOOK_URL` via secretKeyRef.

**git commit**: `git add -u && git commit -m "feat: slack notifications via incoming webhook"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 2 of slack-notifications for kscribe.
Phase 1 added notify.Notification, notify.Slack, notify.Multi and moved the
Notifier interface to structured payloads. Wire config + Helm.

Branch: slack-notifications-phase-2  |  Base: slack-notifications-phase-1

Tasks:
- TASK-006: internal/config/config.go — SlackWebhookURL string
  (KSCRIBE_SLACK_WEBHOOK_URL, envDefault ""). SEC: never logged.
- TASK-007: cmd/kscribe/main.go — collect enabled notifiers: Resend when
  cfg.ResendAPIKey != "" && len(cfg.NotifyEmailTo) > 0; Slack when
  cfg.SlackWebhookURL != "". Set reconciler.Notifier to nil / the one /
  notify.Multi(all...). slog.Info the enabled channel names only.
- TASK-008: charts/kscribe — values notifications.slack.webhookUrl (""),
  existingSecret (""), existingSecretKey (slack-webhook-url); Secret +
  helper defines (kscribe.slackSecretName/Key) following the resend
  pattern in templates/secret.yaml + _helpers.tpl; deployment env
  KSCRIBE_SLACK_WEBHOOK_URL via secretKeyRef guarded on webhookUrl or
  existingSecret. Run scripts/build-manifest.sh. Add value rows to
  charts/kscribe/README.md and the notification row in README.md's
  Production configuration table (mention Slack).
- TASK-009: ensure go test ./... passes; extend the reconciler notify test
  only if Multi behaviour isn't already covered by notify unit tests.

Key files: cmd/kscribe/main.go (existing Resend wiring), charts/kscribe/
templates/{secret,deployment}.yaml, _helpers.tpl, both READMEs.

Completion criteria: go test ./... passes; helm template charts/kscribe
--set notifications.slack.webhookUrl=https://hooks.slack.com/x renders
KSCRIBE_SLACK_WEBHOOK_URL via secretKeyRef.

When done: git add -u (+ new files) &&
git commit -m "feat: slack notifications via incoming webhook"
— no Co-authored-by. One-paragraph summary + SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

## 3. Testing

- [ ] TEST-001: slack unit — payload, escaping (`&<>`), non-2xx truncation.
- [ ] TEST-002: Multi — all notifiers called despite failure; joined error.
- [ ] TEST-003: reconciler — structured notification carries summary (updated fake).
- [ ] TEST-004: `helm template` renders secret-backed webhook env.
- [ ] TEST-005: manual — real webhook, trigger BackOff in kind, see the message.

## 4. Risks & Assumptions

- **RISK-001**: Interface change touches the email path — mitigation: email rendering functions unchanged; existing notify tests keep passing.
- **ASSUMPTION-001**: Plain `{"text": ...}` mrkdwn is enough; Block Kit is out of scope until asked.
- **ASSUMPTION-002**: One webhook/channel; per-namespace routing out of scope (same as email).
- **ASSUMPTION-003**: Volume already bounded by `maxDiagnosesPerHour` (30/h default).
