---
goal: Make kscribe production ready — retention, metrics, auth, cost caps
version: 1.2
date_created: 2026-07-03
last_updated: 2026-07-03
owner: amjadjibon
status: 'In progress'
tags: [feature, architecture]
---

# Production Readiness

![Status: In progress](https://img.shields.io/badge/status-In%20progress-yellow)

kscribe works end to end but has gaps that bite in a real cluster: unbounded SQLite/CR growth, no operational metrics, an unauthenticated dashboard, no cap on LLM spend during event storms, and a nonessential JSON dependency (sonic). This plan closes them in five phases; each operational phase ships a Helm-configurable knob.

## 1. Requirements & Constraints

- **REQ-001**: Old incidents, diagnoses, chat rows, and finished KscribeDiagnosis CRs are pruned automatically after a configurable retention window.
- **REQ-002**: Prometheus metrics expose diagnosis counts (by outcome), LLM token usage, and LLM request latency.
- **REQ-003**: Dashboard routes (except `/healthz`) can require a static bearer token sourced from a Secret.
- **REQ-004**: A global rate limit caps diagnoses started per hour; excess CRs wait (requeue), they are not dropped.
- **REQ-005**: All JSON encoding/decoding uses stdlib `encoding/json`; `github.com/bytedance/sonic` is removed from `go.mod` along with its transitive dependencies.
- **SEC-001**: Auth token never appears in logs or CR status; comparison uses `crypto/subtle.ConstantTimeCompare`.
- **CON-001**: The old "all JSON via sonic" house rule (CON-003 in code comments) is retired by Phase 5. Phases 1–4 need no JSON work; if any arises, use `encoding/json` and don't add new sonic call sites.
- **CON-002**: No new dependencies beyond what controller-runtime already vendors (prometheus client is already transitive via controller-runtime metrics).
- **CON-003**: Every new knob defaults to current behaviour where safe (auth off by default; retention on with 30d default since unbounded growth is the outage).
- **CON-004**: `deploy/kscribe.yaml` is generated — edit only `charts/kscribe`, then run `scripts/build-manifest.sh`.

## 2. Implementation Steps

> After completing all tasks in a phase, `git add -u` (plus explicit paths for new files) and commit. No `Co-authored-by:`. Tick `[x]` as each task completes.

### Phase 1: Retention & pruning

**Goal**: Stop unbounded growth of the SQLite DB and finished KscribeDiagnosis CRs — the only slow-motion outage in the current code. First because it's the highest-risk gap and has no dependencies.

- [x] TASK-001: Add `RetentionPeriod time.Duration` to `internal/config/config.go` (`KSCRIBE_RETENTION_PERIOD`, default `720h`; `0` disables pruning).
- [x] TASK-002: Add `(*Store).Prune(ctx, olderThan time.Time) (int64, error)` in `internal/store/sqlite.go` — one transaction, three explicit DELETEs keyed on (namespace, name): `chat_messages` (no FK at all), `diagnoses` (FK but no cascade), then `incidents` with `updated_at < olderThan`. `updated_at` is indexed (`idx_incidents_updated_at`) and stored as a SQLite `datetime('now')` string — format the cutoff the same way (see `fmtTimePtr` in `sqlite.go`). No migration needed.
- [x] TASK-003: Add a pruner runnable in `cmd/kscribe/main.go` via `mgr.Add(ctrlmgr.RunnableFunc(...))` — ticker loop (1h interval) calling `st.Prune` and deleting KscribeDiagnosis CRs whose phase is terminal (`Done`, `Partial`, or `Failed` — `Partial` counts as terminal) and older than the retention window, using `mgr.GetClient()`.
- [x] TASK-004: Expose `retentionPeriod` in `charts/kscribe/values.yaml` → env in `charts/kscribe/templates/deployment.yaml`; regenerate `deploy/kscribe.yaml` with `scripts/build-manifest.sh`.

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
  (int64, error) in internal/store/sqlite.go: one transaction, three
  explicit DELETEs keyed on (namespace, name) — chat_messages (has no FK),
  diagnoses (FK without cascade), then incidents with
  updated_at < olderThan. updated_at is indexed
  (idx_incidents_updated_at) and stored as a SQLite datetime('now')
  string, so format the cutoff identically (see fmtTimePtr/parseTime in
  sqlite.go). No new migration.
- TASK-003: In cmd/kscribe/main.go, add a pruner via
  mgr.Add(ctrlmgr.RunnableFunc(...)): hourly ticker; skip entirely when
  RetentionPeriod == 0; call st.Prune and also list+delete KscribeDiagnosis
  CRs (api/v1alpha1) in a terminal phase — the enum in
  kscribediagnosis_types.go is Pending|Diagnosing|Done|Partial|Failed;
  terminal means Done, Partial, or Failed — older than the window,
  using mgr.GetClient(). Log counts at Info level.
- TASK-004: Add retentionPeriod to charts/kscribe/values.yaml (default
  "720h") and wire it as the env var in
  charts/kscribe/templates/deployment.yaml; then run
  scripts/build-manifest.sh to regenerate deploy/kscribe.yaml (generated
  file — never hand-edit).

Key files:
- internal/config/config.go — env-tagged Config struct
- internal/store/sqlite.go — Store methods; sqlite via modernc.org/sqlite
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

- [x] TASK-005: Re-enable the controller-runtime metrics server in `cmd/kscribe/main.go`: replace `BindAddress: "0"` with a configurable `KSCRIBE_METRICS_ADDR` (default `:8081`) from `internal/config/config.go`.
- [x] TASK-006: Create `internal/metrics/metrics.go` registering on `sigs.k8s.io/controller-runtime/pkg/metrics.Registry`: `kscribe_diagnoses_total{outcome}` (counter), `kscribe_llm_tokens_total{provider,model}` (counter), `kscribe_llm_request_seconds{provider}` (histogram).
- [x] TASK-007: Increment counters in `internal/controller/kscribediagnosis_controller.go` where a diagnosis reaches a terminal phase (`Done`, `Partial`, or `Failed` — outcome label values `done|partial|failed`) and where token usage is recorded; time the provider call in `internal/agent/diagnosis_agent.go` (or wrap in the reconciler if the agent shouldn't import metrics).
- [x] TASK-008: Expose metrics port in `charts/kscribe/templates/deployment.yaml` + `service.yaml`; regenerate `deploy/kscribe.yaml`.

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
  kscribe_diagnoses_total counter with label outcome (done|partial|failed
  — matching the DiagnosisPhase enum in api/v1alpha1),
  kscribe_llm_tokens_total counter with labels provider, model,
  kscribe_llm_request_seconds histogram with label provider.
- TASK-007: Instrument internal/controller/kscribediagnosis_controller.go:
  increment kscribe_diagnoses_total when the CR transitions to a terminal
  phase (Done, Partial, or Failed); add token usage where TokensUsed
  lands in status.
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

- [x] TASK-009: Add `DashboardToken string` to `internal/config/config.go` (`KSCRIBE_DASHBOARD_TOKEN`, default empty = auth disabled).
- [x] TASK-010: In `internal/web/server.go`, add chi middleware (`r.Use`) in `Handler()` — the router is `github.com/go-chi/chi/v5` — checking `Authorization: Bearer <token>` OR a `kscribe_token` cookie via `subtle.ConstantTimeCompare`; mount `/healthz` and `/login` outside the middleware group. On failure: 401 for API/SSE paths. Add `GET/POST /login` (simple templ form) that sets the cookie so the HTMX dashboard stays usable in a browser.
- [x] TASK-011: Plumb the token into `web.New` from `cmd/kscribe/main.go`.
- [x] TASK-012: Add `dashboard.token` / `dashboard.existingSecret` support in `charts/kscribe` (env from Secret, matching the existing `kscribe-llm` secret pattern in `templates/secret.yaml`); regenerate `deploy/kscribe.yaml`.

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
- TASK-010: In internal/web/server.go, add auth middleware via chi's
  r.Use in Handler() (the router is github.com/go-chi/chi/v5 — use a
  r.Group for protected routes): skip when the configured token is empty;
  mount /healthz and /login outside the group.
  Accept Authorization: Bearer <token> or cookie
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
- internal/web/server.go — Handler() builds the chi router; healthz at /healthz
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

- [x] TASK-013: Add `MaxDiagnosesPerHour int` to `internal/config/config.go` (`KSCRIBE_MAX_DIAGNOSES_PER_HOUR`, default `30`; `0` = unlimited).
- [x] TASK-014: Add a small sliding-window limiter (stdlib only: mutex + timestamp slice) in `internal/controller` — `Allow() bool` consulted in `kscribediagnosis_controller.go` before starting a diagnosis; on deny, `RequeueAfter` with jitter and leave the CR Pending.
- [x] TASK-015: Record throttling visibly: increment `kscribe_diagnoses_throttled_total` (extends Phase 2 metrics) and set a status condition/message on the CR so `kubectl get kscribediagnoses` explains the wait.
- [x] TASK-016: Expose `maxDiagnosesPerHour` in `charts/kscribe/values.yaml` → deployment env; regenerate `deploy/kscribe.yaml`.

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

### Phase 5: Replace sonic with stdlib encoding/json

**Goal**: Drop `github.com/bytedance/sonic` (and the ~7 transitive modules it pulls in: sonic/loader, bytedance/gopkg, cloudwego/base64x, twitchyliquid64/golang-asm, klauspost/cpuid, golang.org/x/arch) in favour of stdlib `encoding/json`. Fewer deps, no cgo/asm surface, one less supply-chain worry. Last because it's mechanical and touches files the earlier phases also edit — doing it here avoids rebase churn.

**Depends on**: Phase 4 complete

- [x] TASK-017: Replace all sonic imports/calls with `encoding/json` in: `internal/store/sqlite.go`, `internal/enricher/payload.go`, `internal/agent/openai.go`, `internal/agent/schema.go`, `internal/agent/diagnosis_agent.go`, `internal/web/templates/viewmodel.go`, `internal/controller/tool_executor.go`, `internal/controller/kscribediagnosis_controller.go`, `cmd/kscribe/main.go` — `sonic.Marshal`→`json.Marshal`, `sonic.Unmarshal`→`json.Unmarshal`, decoder/encoder variants to `json.NewDecoder`/`json.NewEncoder`. Struct tags are already `json:"..."` so no tag changes.
- [x] TASK-018: Update the corresponding `_test.go` files (`internal/enricher/enricher_test.go`, `internal/agent/{openai,streaming}_test.go`, `internal/web/server_test.go`) the same way.
- [x] TASK-019: Delete or rewrite all `CON-003` code comments that say "sonic, not encoding/json" — they now state the opposite of reality.
- [x] TASK-020: `go mod tidy` and verify sonic and its transitive-only deps are gone from `go.mod`/`go.sum`.

**Completion criteria**: `grep -rn "sonic" --include='*.go' cmd internal` returns nothing; `grep sonic go.mod` returns nothing; `go build ./... && go test ./...` passes.

**git commit**: `git add -u && git commit -m "refactor: replace sonic with stdlib encoding/json"`

**Agent Prompt**:
```
You are a sub-agent implementing Phase 5 of production-ready for kscribe,
a Go Kubernetes operator. The codebase currently uses
github.com/bytedance/sonic for all JSON (an old house rule tagged CON-003
in comments). Replace it with stdlib encoding/json and remove the
dependency.

Branch: production-ready/phase-5  |  Base: production-ready/phase-4

Tasks:
- TASK-017: In internal/store/sqlite.go, internal/enricher/payload.go,
  internal/agent/openai.go, internal/agent/schema.go,
  internal/agent/diagnosis_agent.go, internal/web/templates/viewmodel.go,
  internal/controller/tool_executor.go,
  internal/controller/kscribediagnosis_controller.go, cmd/kscribe/main.go:
  swap sonic imports for encoding/json. sonic.Marshal→json.Marshal,
  sonic.Unmarshal→json.Unmarshal; any sonic streaming decoder/encoder →
  json.NewDecoder/json.NewEncoder. Watch internal/agent/openai.go: it
  parses SSE stream chunks and deliberately skips malformed chunks —
  preserve that behaviour. Struct tags are already json:"..." — do not
  touch them.
- TASK-018: Update the test files that import sonic the same way:
  internal/enricher/enricher_test.go, internal/agent/openai_test.go,
  internal/agent/streaming_test.go, internal/web/server_test.go.
- TASK-019: Remove or rewrite every code comment referencing CON-003 /
  "sonic, not encoding/json" so comments match the new reality. Also fix
  the doc mention in internal/web/templates/viewmodel.go and the
  "(CON-003: sonic used inside package)" comment in cmd/kscribe/main.go.
- TASK-020: Run go mod tidy; confirm bytedance/sonic, sonic/loader,
  bytedance/gopkg, cloudwego/base64x, twitchyliquid64/golang-asm,
  klauspost/cpuid, and golang.org/x/arch are gone from go.mod (some may
  remain if another module needs them — that's fine, only sonic itself
  must be a direct removal).

Completion criteria: grep -rn "sonic" --include='*.go' cmd internal
returns nothing; grep sonic go.mod returns nothing;
go build ./... && go test ./... passes.

When done: git add -u && git commit -m "refactor: replace sonic with stdlib encoding/json"
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
- [ ] TEST-006: Post-Phase-5 — full `go test ./...` green with zero sonic references (`grep -rn sonic cmd internal go.mod` empty); existing JSON round-trip tests (enricher, agent, viewmodel) pass unchanged, proving behavioural equivalence.

## 4. Risks & Assumptions

- **RISK-001**: Deleting KscribeDiagnosis CRs could race an in-flight reconcile — mitigation: pruner only deletes terminal phases (`Done`, `Partial`, `Failed`) older than the retention window.
- **RISK-002**: Neither `diagnoses` (FK, no cascade) nor `chat_messages` (no FK) cascade on incident delete — mitigation: `Prune` does three explicit DELETEs in one transaction; no schema change.
- **RISK-003**: Cookie auth on SSE endpoints — HTMX SSE sends cookies automatically, bearer does not; mitigation: middleware accepts either, tests cover the SSE path.
- **ASSUMPTION-001**: Prometheus client is available transitively via controller-runtime; no new go.mod requirement needed.
- **ASSUMPTION-002**: Retention cutoff uses `incidents.updated_at` (verified in `migrations/0001_init.sql`; indexed, stored as `datetime('now')` TEXT).
- **ASSUMPTION-003**: Single replica (matches existing CON-006 comments), so an in-process rate limiter and per-replica pruner are sufficient.
- **ASSUMPTION-004**: Webhook/Slack notifications were deliberately excluded — they're a feature, not production readiness; add as a separate plan if wanted.
- **RISK-004**: sonic and encoding/json differ on edge cases (HTML escaping defaults, number precision, map key ordering) — mitigation: existing round-trip tests must pass unchanged (TEST-006); the SSE chunk parser in `internal/agent/openai.go` keeps its skip-malformed-chunk behaviour.
- **ASSUMPTION-005**: Only sonic is removable; the remaining direct deps (templ, chi, bluemonday, goldmark, openai-go, cobra, caarlos0/env, modernc.org/sqlite, k8s/controller-runtime) are all actively imported and stay.
