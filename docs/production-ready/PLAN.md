---
goal: Make kscribe production ready — retention, metrics, auth, cost caps
version: 1.0
date_created: 2026-07-03
last_updated: 2026-07-03
owner: amjadjibon
status: 'Planned'
tags: [feature, architecture]
---

# Production Readiness

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

kscribe works end to end but has four gaps that bite in a real cluster: unbounded SQLite/CR growth, no operational metrics, an unauthenticated dashboard, and no cap on LLM spend during event storms. This plan closes them in four independent phases, each shipping a Helm-configurable knob.

## 1. Requirements & Constraints

- **REQ-001**: Old incidents, diagnoses, chat rows, and finished KscribeDiagnosis CRs are pruned automatically after a configurable retention window.
- **REQ-002**: Prometheus metrics expose diagnosis counts (by outcome), LLM token usage, and LLM request latency.
- **REQ-003**: Dashboard routes (except `/healthz`) can require a static bearer token sourced from a Secret.
- **REQ-004**: A global rate limit caps diagnoses started per hour; excess CRs wait (requeue), they are not dropped.
- **SEC-001**: Auth token never appears in logs or CR status; comparison uses `crypto/subtle.ConstantTimeCompare`.
- **CON-001**: CON-003 house rule — all JSON via sonic, no `encoding/json`.
- **CON-002**: No new dependencies beyond what controller-runtime already vendors (prometheus client is already transitive via controller-runtime metrics).
- **CON-003**: Every new knob defaults to current behaviour where safe (auth off by default; retention on with 30d default since unbounded growth is the outage).
- **CON-004**: `deploy/kscribe.yaml` is generated — edit only `charts/kscribe`, then run `scripts/build-manifest.sh`.

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` (plus explicit paths for new files) and commit. No `Co-authored-by:`. Tick `[x]` as each task completes.

### Phase 1: Retention & pruning

**Goal**: Stop unbounded growth of the SQLite DB and finished KscribeDiagnosis CRs — the only slow-motion outage in the current code. First because it's the highest-risk gap and has no dependencies.

- [ ] TASK-001: Add `RetentionPeriod time.Duration` to `internal/config/config.go` (`KSCRIBE_RETENTION_PERIOD`, default `720h`; `0` disables pruning).
- [ ] TASK-002: Add `(*Store).Prune(ctx, olderThan time.Time) (int64, error)` in `internal/store/sqlite.go` — delete incidents with `last_seen < olderThan`, plus their diagnoses and chat rows (respect existing FK/cascade behaviour; check `internal/store/migrations/*.sql` and add `ON DELETE CASCADE` via a new migration only if not already present).
- [ ] TASK-003: Add a pruner runnable in `cmd/kscribe/main.go` via `mgr.Add(ctrlmgr.RunnableFunc(...))` — ticker loop (1h interval) calling `st.Prune` and deleting KscribeDiagnosis CRs whose phase is terminal (Completed/Failed) and older than the retention window, using `mgr.GetClient()`.
- [ ] TASK-004: Expose `retentionPeriod` in `charts/kscribe/values.yaml` → env in `charts/kscribe/templates/deployment.yaml`; regenerate `deploy/kscribe.yaml` with `scripts/build-manifest.sh`.

**Completion criteria**: `go test ./internal/store/...` passes including a new `TestPrune` that inserts an old and a new incident and asserts only the old one (and its diagnoses/chat rows) is deleted; `helm template charts/kscribe --set retentionPeriod=168h` renders `KSCRIBE_RETENTION_PERIOD=168h`.

**git commit**: `git add -u && git commit -m "feat: prune old incidents, chat, and finished diagnosis CRs"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 1 of production-ready for kscribe,
a Go Kubernetes operator (controller-runtime) that diagnoses Warning events
with an LLM and mirrors results into SQLite.

Context: Nothing ever deletes old SQLite rows or finished KscribeDiagnosis
CRs, so both grow forever. Add retention-based pruning.

Branch: production-ready/phase-1  |  Base: main

Tasks:
- TASK-001: Add RetentionPeriod time.Duration to internal/config/config.go
  (env KSCRIBE_RETENTION_PERIOD, envDefault "720h"; 0 disables pruning).
  Follow the existing caarlos0/env struct-tag style in that file.
- TASK-002: Add (*Store).Prune(ctx context.Context, olderThan time.Time)
  (int64, error) in internal/store/sqlite.go deleting incidents with
  last_seen < olderThan plus their diagnoses and chat rows. Check the
  schema in internal/store/migrations/*.sql first; if child tables lack
  ON DELETE CASCADE, either delete children explicitly in one transaction
  or add a new numbered migration. All JSON via sonic (house rule CON-003),
  though Prune likely needs none.
- TASK-003: In cmd/kscribe/main.go, add a pruner via
  mgr.Add(ctrlmgr.RunnableFunc(...)): hourly ticker; skip entirely when
  RetentionPeriod == 0; call st.Prune and also list+delete KscribeDiagnosis
  CRs (api/v1alpha1) in a terminal phase (Completed/Failed) older than the
  window using mgr.GetClient(). Log counts at Info level.
- TASK-004: Add retentionPeriod to charts/kscribe/values.yaml (default
  "720h") and wire it as the env var in
  charts/kscribe/templates/deployment.yaml; then run
  scripts/build-manifest.sh to regenerate deploy/kscribe.yaml (generated
  file — never hand-edit).

Key files:
- internal/config/config.go — env-tagged Config struct
- internal/store/sqlite.go — Store methods; sqlite via modernc or mattn (check imports)
- internal/store/migrations/ — numbered .sql migrations, embedded
- cmd/kscribe/main.go — manager wiring, existing RunnableFunc example for the web server
- charts/kscribe/values.yaml, charts/kscribe/templates/deployment.yaml

Completion criteria: go test ./internal/store/... passes including a new
TestPrune that inserts an old and a new incident and asserts only the old
one (and its diagnoses/chat rows) is deleted; helm template charts/kscribe
--set retentionPeriod=168h renders KSCRIBE_RETENTION_PERIOD=168h.

When done: git add -u (plus explicit paths for new files) &&
git commit -m "feat: prune old incidents, chat, and finished diagnosis CRs"
— no Co-authored-by.
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 2: Prometheus metrics

**Goal**: Operators can see diagnosis throughput, failures, token spend, and LLM latency. Independent of Phase 1 but stacked for linear review.

**Depends on**: Phase 1 complete

- [ ] TASK-005: Re-enable the controller-runtime metrics server in `cmd/kscribe/main.go`: replace `BindAddress: "0"` with a configurable `KSCRIBE_METRICS_ADDR` (default `:8081`) from `internal/config/config.go`.
- [ ] TASK-006: Create `internal/metrics/metrics.go` registering on `sigs.k8s.io/controller-runtime/pkg/metrics.Registry`: `kscribe_diagnoses_total{outcome}` (counter), `kscribe_llm_tokens_total{provider,model}` (counter), `kscribe_llm_request_seconds{provider}` (histogram).
- [ ] TASK-007: Increment counters in `internal/controller/kscribediagnosis_controller.go` where a diagnosis reaches Completed/Failed and where token usage is recorded; time the provider call in `internal/agent/diagnosis_agent.go` (or wrap in the reconciler if the agent shouldn't import metrics).
- [ ] TASK-008: Expose metrics port in `charts/kscribe/templates/deployment.yaml` + `service.yaml`; regenerate `deploy/kscribe.yaml`.

**Completion criteria**: With the operator running locally (`scripts/local-test.sh` or `go run ./cmd/kscribe` against a kind cluster), `curl -s localhost:8081/metrics | grep kscribe_` shows all three metric families; `go test ./...` passes.

**git commit**: `git add -u && git commit -m "feat: expose prometheus metrics for diagnoses and llm usage"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 2 of production-ready for kscribe,
a Go Kubernetes operator (controller-runtime) that diagnoses Warning events
with an LLM.

Context: The controller-runtime metrics server is disabled
(BindAddress "0" in cmd/kscribe/main.go) and no custom metrics exist.
Operators need visibility into diagnosis outcomes and LLM cost.

Branch: production-ready/phase-2  |  Base: production-ready/phase-1

Tasks:
- TASK-005: Add MetricsAddr string to internal/config/config.go
  (env KSCRIBE_METRICS_ADDR, envDefault ":8081") and use it for
  metricsserver.Options{BindAddress: ...} in cmd/kscribe/main.go.
- TASK-006: New package internal/metrics with vars registered on
  sigs.k8s.io/controller-runtime/pkg/metrics.Registry (prometheus client
  is already a transitive dep — add no new module requirements):
  kscribe_diagnoses_total counter with label outcome (completed|failed),
  kscribe_llm_tokens_total counter with labels provider, model,
  kscribe_llm_request_seconds histogram with label provider.
- TASK-007: Instrument internal/controller/kscribediagnosis_controller.go:
  increment kscribe_diagnoses_total when the CR transitions to
  Completed/Failed; add token usage where TokensUsed lands in status.
  Time each provider round-trip for the histogram — prefer instrumenting
  in the reconciler or a thin wrapper so internal/agent stays free of
  metrics imports.
- TASK-008: Add a metrics containerPort (8081) in
  charts/kscribe/templates/deployment.yaml, expose it in service.yaml
  with prometheus.io scrape annotations behind a values toggle
  (metrics.enabled, default true), then run scripts/build-manifest.sh.

Key files:
- cmd/kscribe/main.go — mgrOpts.Metrics currently BindAddress "0"
- internal/config/config.go — env-tagged Config
- internal/controller/kscribediagnosis_controller.go — reconcile loop, status transitions
- internal/agent/diagnosis_agent.go — LLM loop (read-only reference for where usage is tallied)
- charts/kscribe/templates/{deployment,service}.yaml

Completion criteria: go test ./... passes; running the operator locally,
curl -s localhost:8081/metrics | grep kscribe_ shows all three metric
families.

When done: git add -u (plus explicit paths for new files) &&
git commit -m "feat: expose prometheus metrics for diagnoses and llm usage"
— no Co-authored-by.
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 3: Dashboard auth

**Goal**: Optional static bearer-token auth on the dashboard so a misrouted Ingress doesn't expose cluster RCA data.

**Depends on**: Phase 2 complete

- [ ] TASK-009: Add `DashboardToken string` to `internal/config/config.go` (`KSCRIBE_DASHBOARD_TOKEN`, default empty = auth disabled).
- [ ] TASK-010: In `internal/web/server.go`, wrap `Handler()` routes (all except `/healthz`) with a check for `Authorization: Bearer <token>` OR a `kscribe_token` cookie, using `subtle.ConstantTimeCompare`. On failure: 401 for API/SSE paths. Add `GET/POST /login` (simple templ form) that sets the cookie so the HTMX dashboard stays usable in a browser.
- [ ] TASK-011: Plumb the token into `web.New` from `cmd/kscribe/main.go`.
- [ ] TASK-012: Add `dashboard.token` / `dashboard.existingSecret` support in `charts/kscribe` (env from Secret, matching the existing `kscribe-llm` secret pattern in `templates/secret.yaml`); regenerate `deploy/kscribe.yaml`.

**Completion criteria**: `go test ./internal/web/...` passes with new tests: token unset → 200 without auth; token set → 401 without credentials, 200 with correct bearer, 200 after login cookie, `/healthz` always 200.

**git commit**: `git add -u && git commit -m "feat: optional bearer-token auth for dashboard"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 3 of production-ready for kscribe.
kscribe serves an HTMX/templ dashboard (internal/web) alongside a
Kubernetes operator; the dashboard currently has zero authentication.

Branch: production-ready/phase-3  |  Base: production-ready/phase-2

Tasks:
- TASK-009: Add DashboardToken string to internal/config/config.go
  (env KSCRIBE_DASHBOARD_TOKEN, envDefault ""; empty disables auth).
- TASK-010: In internal/web/server.go, add auth middleware around the mux
  in Handler(): skip when the configured token is empty; always allow
  /healthz. Accept Authorization: Bearer <token> or cookie
  kscribe_token=<token>; compare with crypto/subtle.ConstantTimeCompare.
  Unauthenticated browser requests to HTML pages redirect to /login
  (GET shows a minimal form — follow the existing templ component style in
  internal/web/templates/; POST validates the token, sets an HttpOnly
  cookie, redirects to /). Non-HTML (SSE /stream, POST /chat) get plain
  401. Never log the token.
- TASK-011: Pass the token through web.New(...) from cmd/kscribe/main.go
  (extend the constructor signature or add a field on Server — match the
  existing style).
- TASK-012: In charts/kscribe: values dashboard.token (default "") and
  dashboard.existingSecret; template env KSCRIBE_DASHBOARD_TOKEN from a
  Secret following the existing kscribe-llm pattern in
  templates/secret.yaml + deployment.yaml. Run scripts/build-manifest.sh
  to regenerate deploy/kscribe.yaml.

Key files:
- internal/web/server.go — Handler() builds the mux; healthz at /healthz
- internal/web/server_test.go — existing test style (httptest)
- internal/web/templates/ — templ components (run `templ generate` if you
  add a .templ file; check Makefile for the target)
- internal/config/config.go, cmd/kscribe/main.go
- charts/kscribe/templates/{secret,deployment}.yaml

Completion criteria: go test ./internal/web/... passes with new tests:
token unset → 200 without auth; token set → 401 without credentials,
200 with correct bearer, 200 after login cookie; /healthz always 200.

When done: git add -u (plus explicit paths for new files) &&
git commit -m "feat: optional bearer-token auth for dashboard"
— no Co-authored-by.
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

### Phase 4: LLM rate limit / cost cap

**Goal**: An event storm (e.g. a deployment crashlooping across 50 pods) can't fire 50 LLM calls in a burst. Cap diagnoses started per hour; excess CRs requeue and run later.

**Depends on**: Phase 3 complete

- [ ] TASK-013: Add `MaxDiagnosesPerHour int` to `internal/config/config.go` (`KSCRIBE_MAX_DIAGNOSES_PER_HOUR`, default `30`; `0` = unlimited).
- [ ] TASK-014: Add a small sliding-window limiter (stdlib only: mutex + timestamp slice) in `internal/controller` — `Allow() bool` consulted in `kscribediagnosis_controller.go` before starting a diagnosis; on deny, `RequeueAfter` with jitter and leave the CR Pending.
- [ ] TASK-015: Record throttling visibly: increment `kscribe_diagnoses_throttled_total` (extends Phase 2 metrics) and set a status condition/message on the CR so `kubectl get kscribediagnoses` explains the wait.
- [ ] TASK-016: Expose `maxDiagnosesPerHour` in `charts/kscribe/values.yaml` → deployment env; regenerate `deploy/kscribe.yaml`.

**Completion criteria**: `go test ./internal/controller/...` passes including a limiter unit test (N allowed, N+1 denied, allowed again after window) and a reconciler test asserting a denied diagnosis stays Pending with a requeue instead of calling the provider.

**git commit**: `git add -u && git commit -m "feat: rate-limit diagnosis starts per hour"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 4 of production-ready for kscribe,
a Go operator that runs an LLM diagnosis per KscribeDiagnosis CR.

Context: Event dedup is per-object only; a crashloop across many pods
fires many concurrent LLM calls. Add a global hourly cap on diagnosis
starts; over-cap CRs requeue, never drop.

Branch: production-ready/phase-4  |  Base: production-ready/phase-3

Tasks:
- TASK-013: Add MaxDiagnosesPerHour int to internal/config/config.go
  (env KSCRIBE_MAX_DIAGNOSES_PER_HOUR, envDefault "30"; 0 = unlimited),
  plumb into the reconciler struct in cmd/kscribe/main.go.
- TASK-014: New file internal/controller/ratelimit.go: sliding-window
  limiter, stdlib only (sync.Mutex + []time.Time, prune old stamps on
  Allow). Consult it in kscribediagnosis_controller.go before the
  diagnosis starts (before the phase moves to Diagnosing); on deny,
  return ctrl.Result{RequeueAfter: 2-5 min with jitter} leaving the CR
  Pending. Mind the existing Concurrency semaphore — the limiter check
  goes before any LLM work.
- TASK-015: Add kscribe_diagnoses_throttled_total counter in
  internal/metrics (from Phase 2) and increment on deny; also set a
  human-readable status message/condition on the CR so kubectl explains
  the wait. Reuse the existing patchStatus conflict-retry helper.
- TASK-016: charts/kscribe/values.yaml: maxDiagnosesPerHour (default 30)
  → env in templates/deployment.yaml; run scripts/build-manifest.sh.

Key files:
- internal/controller/kscribediagnosis_controller.go — reconcile flow,
  phase transitions, patchStatus helper
- internal/controller/dedup.go — style reference for small stateful helpers
- internal/metrics/metrics.go — created in Phase 2
- internal/config/config.go, cmd/kscribe/main.go
- charts/kscribe/values.yaml, charts/kscribe/templates/deployment.yaml

Completion criteria: go test ./internal/controller/... passes including
a limiter unit test (N allowed, N+1 denied, allowed again after the
window) and a reconciler test asserting a denied diagnosis stays Pending
with RequeueAfter and the provider is never called.

When done: git add -u (plus explicit paths for new files) &&
git commit -m "feat: rate-limit diagnosis starts per hour"
— no Co-authored-by.
Write a one-paragraph summary of changes and commit SHA.
Do NOT push, open PRs, or modify PLAN.md.
```

---

## 3. Testing

- [ ] TEST-001: `internal/store/sqlite_test.go` — `TestPrune`: old incident + diagnoses + chat deleted, recent incident untouched, returns deleted count.
- [ ] TEST-002: `internal/web/server_test.go` — auth matrix: no token configured / bad bearer / good bearer / login cookie / `/healthz` bypass.
- [ ] TEST-003: `internal/controller/ratelimit_test.go` — window semantics incl. recovery after expiry.
- [ ] TEST-004: `internal/controller/kscribediagnosis_controller_test.go` — throttled CR stays Pending, provider not invoked.
- [ ] TEST-005: Manual/e2e — `scripts/local-test.sh` against kind: trigger a BackOff, confirm metrics at `:8081/metrics`, dashboard 401 without token, and pruning log line after shrinking `KSCRIBE_RETENTION_PERIOD`.

## 4. Risks & Assumptions

- **RISK-001**: Deleting KscribeDiagnosis CRs could race an in-flight reconcile — mitigation: pruner only deletes terminal phases (Completed/Failed) older than the retention window.
- **RISK-002**: SQLite migrations are embedded and numbered; a cascade-delete migration must not break existing DBs — mitigation: prefer explicit child deletes in a transaction over schema change.
- **RISK-003**: Cookie auth on SSE endpoints — HTMX SSE sends cookies automatically, bearer does not; mitigation: middleware accepts either, tests cover the SSE path.
- **ASSUMPTION-001**: Prometheus client is available transitively via controller-runtime; no new go.mod requirement needed.
- **ASSUMPTION-002**: `last_seen` (or equivalent timestamp column) exists on incidents for retention cutoff; if it's named differently the Phase 1 agent adapts to the actual schema in `migrations/0001_init.sql`.
- **ASSUMPTION-003**: Single replica (matches existing CON-006 comments), so an in-process rate limiter and per-replica pruner are sufficient.
- **ASSUMPTION-004**: Webhook/Slack notifications were deliberately excluded — they're a feature, not production readiness; add as a separate plan if wanted.
