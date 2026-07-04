---
goal: Email notifications for finished diagnoses via Resend
version: 1.0
date_created: 2026-07-04
last_updated: 2026-07-04
owner: amjadjibon
status: 'Completed'
tags: [feature]
---

# Email Notifications (Resend)

![Status: Completed](https://img.shields.io/badge/status-Completed-brightgreen)

When a diagnosis reaches a terminal phase (Done, Partial, Failed), kscribe emails the RCA summary via the Resend API so on-call engineers hear about incidents without watching the dashboard. Disabled unless an API key and recipients are configured.

## 1. Requirements & Constraints

- **REQ-001**: On terminal phase transition, send one email per diagnosis with reason, involved object, phase, summary, root cause, and remediation.
- **REQ-002**: Notifications are best-effort — a send failure is logged and counted in metrics, never blocks or fails the reconcile.
- **REQ-003**: Feature is off by default; enabled only when `KSCRIBE_RESEND_API_KEY` and `KSCRIBE_NOTIFY_EMAIL_TO` are both set.
- **SEC-001**: API key comes from a Secret (chart) / env; never logged. Email body uses the already-redacted RCA fields only — no raw cluster context.
- **CON-001**: No new Go dependency — Resend is a single HTTPS POST (`https://api.resend.com/emails`); use stdlib `net/http` + `encoding/json`.
- **CON-002**: Email volume is naturally bounded by `maxDiagnosesPerHour` (default 30/h), so no separate notification rate limit.
- **CON-003**: `deploy/kscribe.yaml` is generated — edit `charts/kscribe`, run `scripts/build-manifest.sh`.
- **CON-004**: Reconciler must not import the notify package's HTTP client directly for testing ergonomics — use a narrow interface field like the existing `Publisher`.

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` (plus explicit paths for new files) and commit. No `Co-authored-by:`. Tick `[x]` as each task completes.

### Phase 1: Resend client and config

**Goal**: A tested, self-contained notify package plus the config knobs, with no wiring yet.

- [x] TASK-001: Create `internal/notify/resend.go`: `type Resend struct { APIKey, From string; To []string; BaseURL string; HTTPClient *http.Client }` with `Send(ctx, subject, html string) error` POSTing `{from, to, subject, html}` with `Authorization: Bearer`; non-2xx → error with status and ≤512 bytes of body (match `truncErr` hygiene in `internal/agent/openai.go`). `BaseURL` defaults to `https://api.resend.com`; overridable for tests.
- [x] TASK-002: Add `Incident` email rendering in `internal/notify/email.go`: `Subject(...)` (`[kscribe] <Phase>: <Reason> <ns>/<object>`) and `HTML(...)` building a minimal inline-styled body from phase, reason, object, summary, root cause, remediation steps. HTML-escape all fields (`html/template` or `html.EscapeString`).
- [x] TASK-003: Add to `internal/config/config.go`: `ResendAPIKey` (`KSCRIBE_RESEND_API_KEY`, default ""), `NotifyEmailFrom` (`KSCRIBE_NOTIFY_EMAIL_FROM`, default `kscribe@notifications.local`), `NotifyEmailTo` (`KSCRIBE_NOTIFY_EMAIL_TO`, comma-separated, default empty).
- [x] TASK-004: Tests `internal/notify/resend_test.go`: httptest server asserting auth header, JSON body, success, non-2xx error truncation; email rendering test asserting escaping of `<script>` in a summary.

**Completion criteria**: `go test ./internal/notify/` passes; `go build ./...` green; package has no non-stdlib imports.

**git commit**: `git add internal/notify/ && git add -u && git commit -m "feat: resend email client and notification rendering"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of email-notifications for kscribe,
a Go Kubernetes operator that diagnoses Warning events with an LLM.

Context: kscribe should email RCA results via the Resend API when a diagnosis
finishes. This phase builds the standalone notify package and config knobs.

Branch: email-notifications-phase-1  |  Base: email-notifications

Tasks:
- TASK-001: internal/notify/resend.go — Resend struct (APIKey, From string;
  To []string; BaseURL string; HTTPClient *http.Client) with
  Send(ctx context.Context, subject, html string) error. POST
  {from, to, subject, html} as JSON to BaseURL+"/emails" with
  Authorization: Bearer <APIKey>. BaseURL defaults to
  https://api.resend.com when empty; HTTPClient defaults to a client with
  a 10s timeout. Non-2xx: error including status and at most 512 bytes of
  response body. stdlib only (net/http, encoding/json).
- TASK-002: internal/notify/email.go — Subject(phase, reason, namespace,
  object string) string ("[kscribe] <Phase>: <Reason> <ns>/<object>") and
  HTML(...) string building a small inline-styled HTML body from phase,
  reason, object, summary, rootCause, remediation. HTML-escape every
  interpolated field.
- TASK-003: internal/config/config.go — add ResendAPIKey
  (KSCRIBE_RESEND_API_KEY, envDefault ""), NotifyEmailFrom
  (KSCRIBE_NOTIFY_EMAIL_FROM, envDefault "kscribe@notifications.local"),
  NotifyEmailTo ([]string, KSCRIBE_NOTIFY_EMAIL_TO, envSeparator ",",
  envDefault ""). Follow the existing caarlos0/env tag style.
- TASK-004: internal/notify/resend_test.go — httptest server: assert
  Authorization header, decoded JSON body fields, 200 → nil error,
  500 with long body → error containing status and truncated body.
  Rendering test: a summary containing <script> must be escaped in HTML().

Key files:
- internal/agent/openai.go — truncErr pattern for error-body hygiene
- internal/config/config.go — env-tagged Config struct

Completion criteria: go test ./internal/notify/ passes; go build ./... green;
internal/notify imports stdlib only.

When done: git add internal/notify/ && git add -u &&
git commit -m "feat: resend email client and notification rendering"
— no Co-authored-by.
Write a one-paragraph summary and the commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 2: Reconciler hook, wiring, chart

**Goal**: Emails actually fire on terminal transitions; deployable via Helm.

**Depends on**: Phase 1 complete

- [x] TASK-005: Add `Notifier` interface (`Notify(ctx, subject, html string) error`) + field on `KscribeDiagnosisReconciler` (nil = disabled), mirroring the `Publisher` pattern. In both terminal paths (Failed branch and Done/Partial success patch in `internal/controller/kscribediagnosis_controller.go`), fire the notification in a goroutine with its own timeout context (10s, detached from the reconcile ctx); log errors, increment `kscribe_notifications_total{status="sent"|"failed"}` (new counter in `internal/metrics/metrics.go`).
- [x] TASK-006: Wire in `cmd/kscribe/main.go`: when `cfg.ResendAPIKey != "" && len(cfg.NotifyEmailTo) > 0`, set the reconciler's Notifier to the notify.Resend client (log at Info that notifications are enabled, without the key).
- [x] TASK-007: Chart: `notifications.resend.apiKey` / `existingSecret` / `existingSecretKey` (Secret pattern like `kscribe-llm`), `notifications.from`, `notifications.to` (list, joined with `,` in the template). Env wiring in `templates/deployment.yaml` + secret in `templates/secret.yaml` + helpers; regenerate `deploy/kscribe.yaml`; add row to chart README values table and main README production table.
- [x] TASK-008: Reconciler test: fake Notifier records subject/html; terminal reconcile triggers exactly one notification containing the summary; notifier error does not fail the reconcile; nil Notifier is a no-op.

**Completion criteria**: `go test ./...` passes; `helm template charts/kscribe --set notifications.resend.apiKey=x --set notifications.to={a@b.c}` renders `KSCRIBE_RESEND_API_KEY` from a Secret and `KSCRIBE_NOTIFY_EMAIL_TO=a@b.c`.

**git commit**: `git add -u && git commit -m "feat: email diagnosis results via resend on terminal phases"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 2 of email-notifications for kscribe.
Phase 1 added internal/notify (Resend client + email rendering) and config
knobs (ResendAPIKey, NotifyEmailFrom, NotifyEmailTo). Now wire it up.

Branch: email-notifications-phase-2  |  Base: email-notifications-phase-1

Tasks:
- TASK-005: internal/controller/kscribediagnosis_controller.go — add
  Notifier interface { Notify(ctx context.Context, subject, html string) error }
  and a Notifier field on KscribeDiagnosisReconciler (nil = disabled),
  mirroring the existing Publisher pattern. On both terminal paths (the
  provider-failure branch and the Done/Partial success path), if Notifier
  != nil, spawn a goroutine with context.WithTimeout(context.Background(),
  10*time.Second) (detached — reconcile ctx ends immediately) that renders
  subject/html via internal/notify and calls Notify; on error slog/log and
  increment the new counter kscribe_notifications_total{status} (add a
  CounterVec in internal/metrics/metrics.go, registered like the others;
  increment "sent" on success, "failed" on error).
- TASK-006: cmd/kscribe/main.go — when cfg.ResendAPIKey != "" and
  len(cfg.NotifyEmailTo) > 0, set reconciler Notifier to &notify.Resend{
  APIKey: ..., From: cfg.NotifyEmailFrom, To: cfg.NotifyEmailTo}. Log
  "email notifications enabled" with recipient count only — never the key.
- TASK-007: charts/kscribe — values: notifications.resend.apiKey (""),
  notifications.resend.existingSecret (""), notifications.resend.
  existingSecretKey (resend-api-key), notifications.from
  (kscribe@notifications.local), notifications.to ([]). Secret template
  following the kscribe-llm/dashboard pattern in templates/secret.yaml +
  _helpers.tpl; deployment env: KSCRIBE_RESEND_API_KEY (secretKeyRef,
  guarded), KSCRIBE_NOTIFY_EMAIL_FROM, KSCRIBE_NOTIFY_EMAIL_TO (join
  notifications.to with ","; guard on non-empty). Run
  scripts/build-manifest.sh (deploy/kscribe.yaml is generated). Add a row
  to charts/kscribe/README.md values table and the "Production
  configuration" table in README.md.
- TASK-008: internal/controller/kscribediagnosis_controller_test.go — fake
  Notifier capturing calls (use a channel or WaitGroup: the send is async).
  Assert: successful terminal reconcile → exactly 1 notification whose html
  contains the RCA summary; notifier returning an error does not make
  Reconcile return an error; nil Notifier stays a no-op (existing tests
  keep passing).

Key files:
- internal/controller/kscribediagnosis_controller.go — Publisher pattern,
  terminal paths (search "DiagnosesTotal.WithLabelValues")
- internal/metrics/metrics.go — collector registration
- charts/kscribe/templates/{secret,deployment}.yaml, _helpers.tpl
- cmd/kscribe/main.go — reconciler wiring

Completion criteria: go test ./... passes; helm template charts/kscribe
--set notifications.resend.apiKey=x --set 'notifications.to={a@b.c}'
renders KSCRIBE_RESEND_API_KEY via secretKeyRef and
KSCRIBE_NOTIFY_EMAIL_TO=a@b.c.

When done: git add -u (plus explicit new file paths) &&
git commit -m "feat: email diagnosis results via resend on terminal phases"
— no Co-authored-by.
Write a one-paragraph summary and the commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

## 3. Testing

- [ ] TEST-001: `internal/notify/resend_test.go` — auth header, body shape, non-2xx truncation, HTML escaping.
- [ ] TEST-002: reconciler — async notification fires once with summary; error swallowed; nil no-op.
- [ ] TEST-003: `helm template` renders secret-backed key and joined recipient list.
- [ ] TEST-004: manual — set a real Resend key + verified sender, trigger a BackOff in kind, receive the email.

## 4. Risks & Assumptions

- **RISK-001**: Async send may race test teardown — mitigation: tests synchronize on a channel from the fake Notifier, not sleeps.
- **RISK-002**: A requeued terminal CR (e.g. mirror-only reconcile) must not re-send — the send hooks only into the transition paths (which run once per diagnosis), not the terminal-mirror path.
- **ASSUMPTION-001**: Resend's `/emails` endpoint accepts `{from, to[], subject, html}` with Bearer auth — per Resend's public API docs.
- **ASSUMPTION-002**: One email per diagnosis is acceptable volume because `maxDiagnosesPerHour` (default 30) already bounds diagnosis throughput (CON-002).
- **ASSUMPTION-003**: Digest/batching, per-namespace routing, and other providers (SMTP, SendGrid) are out of scope until asked for.
