---
date: 2026-06-29
branch: kscribe-mvp-phase-8
reviewer: Claude
verdict: Request Changes
---

# Code Review: kscribe-mvp

## Verdict

**Request Changes** — Core security invariants (SEC-001 redaction, CON-003 sonic-only, secret handling, SQL parameterization) all hold, but two controller correctness bugs in the diagnosis lifecycle (a dead requeue path and unhandled `AlreadyExists`) can permanently strand CRs or storm the reconcile queue.

## Summary

Reviewed the full `git diff main...HEAD` for the kscribe operator, concentrating on the stated invariants and on correctness/concurrency that unit tests would not catch. The security posture is solid: every `Snapshot` is serialized only through `EncodeSnapshot`, which redacts before marshaling; no `encoding/json` exists in application code; the LLM API key is sourced from a Secret and never logged; all SQL is parameterized; migrations fail closed. The substantive problems are in the reconcile control flow: the `Diagnosing` phase guard makes the ADR-003 retry a no-op, and the event-watcher create path treats `AlreadyExists` as a hard error. Both are routinely triggered (storage blips, operator restarts) and have trivial fixes.

## Findings

### [HIGH-001] ADR-003 storage-failure requeue is a no-op; CR stuck in Diagnosing *(High)*
**File**: `internal/controller/kscribediagnosis_controller.go:52-57`, `170-183`
**Category**: Correctness
**Issue**: On `InsertDiagnosis` failure the reconciler sets `Persisted=false` and returns `ctrl.Result{RequeueAfter: 30s}, err` to retry the persist (ADR-003). But the CR was already moved to `Diagnosing` (line 80-92, persisted to the API server). On the requeue, `Reconcile` re-enters and the top-of-function guard only proceeds for `"" | Pending`; `Diagnosing` falls through to `default: return ctrl.Result{}, nil`. The requeue therefore does nothing — the SQLite write is never retried, the in-memory RCA is lost, and the CR is permanently `Diagnosing` / `Persisted=false`. The same trap bricks any CR if the operator crashes between the `Diagnosing` status update and `InsertDiagnosis`. ADR-003's *ordering* (persist before phase flip) is correct; its *recovery* path is not.
**Fix**: Allow `Diagnosing` to be re-processed when not yet persisted. Either add `Diagnosing` to the proceed case when `!kd.Status.Persisted` (re-running the agent), or — cheaper — short-circuit at the top: if phase is `Diagnosing` and a completed-but-unpersisted record exists, re-attempt `InsertDiagnosis` only. Minimal version:
```go
case kscribev1alpha1.DiagnosisPhaseDiagnosing:
    if kd.Status.Persisted { return ctrl.Result{}, nil }
    // fall through to re-run diagnosis + persist
```
Add a test that fails the store's `InsertDiagnosis` once, then asserts a later reconcile persists and reaches `Done`.

---

### [HIGH-002] Event-watcher Create does not treat AlreadyExists as success → reconcile error storm after restart *(High)*
**File**: `internal/controller/event_watcher.go:113` (and `processEvent` 54-72)
**Category**: Correctness
**Issue**: Idempotency relies on two layers — the deterministic CR name `ksd-<uid>` and the in-memory `Deduper`. The `Deduper` is per-process and cleared on restart (`dedup.go:14`). Kubernetes Events persist ~1h, so after any operator restart the watcher re-lists existing Warning events, `ShouldProcess` returns true (fresh map), and `createDiagnosis` calls `Client.Create` for a CR that already exists. The returned `AlreadyExists` error propagates out of `Reconcile` as a non-nil error, so controller-runtime requeues with backoff and retries forever (until the event is GC'd), per event — error-level log spam and wasted work that also masks genuine create failures. The deterministic name correctly guarantees "exactly one CR per event" (REQ-001), but the error handling defeats idempotency.
**Fix**: Treat `AlreadyExists` as success:
```go
import apierrors "k8s.io/apimachinery/pkg/api/errors"
...
if err := r.deps.Client.Create(ctx, ksd); err != nil && !apierrors.IsAlreadyExists(err) {
    return err
}
return nil
```

---

### [MED-001] Deduper map grows unbounded (lazy eviction only on same-key re-access) *(Medium)*
**File**: `internal/controller/dedup.go:31-40`
**Category**: Memory / Correctness
**Issue**: `ShouldProcess` evicts a stale entry only when *that same key* is queried again. Keys are event UIDs, which are unique and never re-queried, so expired entries are never removed — the `seen` map grows for the life of the process (one entry per accepted event). The `// Lazy-evicts stale entries on access` comment overstates the behavior. Bounded only by events/hour × uptime and reset by the single-replica restart, so not catastrophic, but it is an unbounded map on the hot path.
**Fix**: Sweep on write — when the map exceeds a threshold, drop entries whose expiry is in the past — or run a background `time.Ticker` GC goroutine that deletes expired keys. A few lines in `ShouldProcess`:
```go
if len(d.seen) > 1024 {
    for k, exp := range d.seen { if now.After(exp) { delete(d.seen, k) } }
}
```

---

### [MED-002] SSE broker has no publisher in production; dashboard never live-updates *(Medium)*
**File**: `cmd/kscribe/main.go:128,144-154`; `internal/web/server.go:72-106`
**Category**: Correctness
**Issue**: `broker.Publish` is invoked only from a test (`internal/web/server_test.go:197`). `main.go` constructs the broker and hands it to `web.New`, but the `KscribeDiagnosisReconciler` is never given a broker reference and never publishes, so the `/incidents/{ns}/{name}/stream` SSE endpoint accepts connections but no diagnosis-progress events are ever emitted. The live-update feature is wired end-to-end except for the producer.
**Fix**: Inject the broker into the reconciler and `Publish` a rendered fragment on each phase transition (Diagnosing / Done / Partial / Failed), keyed by `namespace + "/" + name`. If live updates are out of MVP scope, drop the SSE handler and broker to avoid dead infrastructure.

---

### [MED-003] Manager Deployment has no securityContext hardening *(Medium)*
**File**: `deploy/kscribe.yaml:536-570` (container spec)
**Category**: Security
**Issue**: For a security-sensitive operator with cluster-wide read RBAC, the pod/container spec sets no `securityContext`: no `runAsNonRoot`, no `allowPrivilegeEscalation: false`, no `capabilities: drop: [ALL]`, no `seccompProfile`, no `readOnlyRootFilesystem`. The container can run as root and escalate. These are free hardening wins.
**Fix**: Add a container `securityContext`:
```yaml
securityContext:
  runAsNonRoot: true
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true   # /data is a writable PVC mount; add an emptyDir for /tmp if needed
  capabilities: { drop: ["ALL"] }
  seccompProfile: { type: RuntimeDefault }
```

---

### [LOW-001] RedactEnabled config flag does not gate redaction (misleading) *(Low)*
**File**: `internal/config/config.go:38-39`; `internal/enricher/payload.go:85-88`
**Category**: Correctness / Clarity
**Issue**: `EncodeSnapshot` always calls `RedactSnapshot` regardless of `KSCRIBE_REDACT_ENABLED`. The flag only flows into `prompt_redacted` (DB column) and a log line. Setting it to `false` does **not** disable redaction. This is fail-safe (good) but the field comment "controls whether sensitive data is scrubbed" is wrong and could mislead an operator into thinking they can toggle it.
**Fix**: Either honor the flag explicitly at the single `EncodeSnapshot` chokepoint (keeping redaction the default), or update the comment/docs to state redaction is always on and the flag is audit metadata only. Given SEC-001, keeping it always-on and fixing the comment is the safer choice.

---

### [LOW-002] Rich enricher context (BuildSnapshot) is dead in production *(Low)*
**File**: `internal/controller/kscribediagnosis_controller.go:99-108`; `internal/enricher/context_builder.go`
**Category**: Correctness / Completeness
**Issue**: The reconciler builds the LLM snapshot from CR spec fields only (reason/message/object identity). `BuildSnapshot` — which collects pod logs, env vars, related events, node conditions — is called only from tests, and `ToolExecutor` is nil so tool calls return a stub error. In its current form the operator sends almost no diagnostic context to the LLM. This is an explicit MVP `ponytail` decision, not a defect, but it materially limits RCA quality and should be tracked. (Positive side effect: it keeps the un-redacted log/env collection path out of the LLM flow entirely, reinforcing SEC-001.)
**Fix**: Wire `BuildSnapshot` (with a `kubernetes.Interface` + `client.Client`) and a real `ToolExecutor` into the reconciler; the `EncodeSnapshot` chokepoint already enforces redaction so no SEC-001 change is needed.

---

### [LOW-003] Raw provider response body embedded in stored error *(Low)*
**File**: `internal/agent/openai.go:60-63`
**Category**: Security / Hygiene
**Issue**: On a non-2xx provider response the full body is interpolated into the error (`provider error %d: %s`). That error becomes `Outcome.RawError`, surfaced in the CR `Diagnosed=False` condition message and persisted to SQLite. Provider error bodies are low-risk but can contain organization/account detail; they are also unbounded in size.
**Fix**: Truncate the body (e.g. first 512 bytes) and avoid echoing it verbatim into durable CR status; log the full body at debug level instead.

## What's Good

- **SEC-001 holds firmly**: `EncodeSnapshot` (`payload.go:85-88`) is the only serialization path for a `Snapshot` and redacts-then-marshals; redaction cannot be bypassed, env vars sourced from `valueFrom` are stubbed to a placeholder (`context_builder.go:199-201`), and the only other `sonic.Marshal` calls are the OpenAI request and the RCA payload — neither carries raw cluster data.
- **CON-003 clean**: no `encoding/json` anywhere in application code (one test file imports it, which the constraint does not cover); sonic used throughout.
- **Store is injection-proof and fails closed**: every query is fully parameterized including the `LIMIT ?` and upsert (`store/sqlite.go`), `SetMaxOpenConns(1)` avoids SQLite write races, and `runMigrations` runs each file in its own transaction and returns an error (closing the DB) on any failure (`migrations.go`, `sqlite.go:86-89`).
- **Secret handling**: `KSCRIBE_LLM_API_KEY` comes from a `secretKeyRef` and the startup `slog.Info` config dump deliberately omits it.
- **SSE broker concurrency is correct**: mutex-guarded subscriber map, non-blocking drop on full buffers (no publisher deadlock), and `unsubscribe` prunes empty incident maps — no goroutine or subscriber leak given the `defer cancel()` in the handler.
- **RBAC is least-privilege**: read-mostly verbs on events/pods/nodes/logs/deployments/replicasets, write only on the operator's own CRDs and their `/status`.

## Pre-Merge Checklist

**Always:**
- [ ] HIGH-001 resolved — Diagnosing/unpersisted CRs can recover and persist
- [ ] HIGH-002 resolved — `AlreadyExists` treated as success on the create path
- [x] No secrets or credentials in committed files (API key via Secret)
- [x] `.gitignore` covers new artifact/config types
- [x] SQL parameterized; migrations fail closed
- [ ] Deployment hardened with a securityContext (MED-003)

## Machine-Readable Verdict

```yaml
verdict: Request Changes
critical: 0
high: 2
medium: 3
low: 3
info: 0
blocking_ids: [HIGH-001, HIGH-002]
```
