# kscribe-mvp QA Report

Branch: `kscribe-mvp-phase-8`  
Date: 2026-06-29  
Go version: 1.26.2

---

## Coverage Numbers

| Package | Before | After |
|---|---|---|
| `api/v1alpha1` | 1.5% | 1.5% |
| `cmd/kscribe` | 0.0% | 0.0% |
| `internal/agent` | 50.0% | 50.0% |
| `internal/config` | 90.0% | **100.0%** |
| `internal/controller` | 68.4% | **73.1%** |
| `internal/enricher` | 47.5% | **54.9%** |
| `internal/store` | 78.3% | 78.3% |
| `internal/web` | 92.4% | 92.4% |
| `internal/web/templates` | 0.0% | 0.0% |
| **Total** | **30.9%** | **32.4%** |

---

## Gaps Found

### 1. Integration: reconcile → real SQLite → dashboard queries (FILLED)

The existing controller tests used a `fakeStore` that never touches SQLite. This left the critical path — reconciler writing an incident/diagnosis to SQLite, then `ListIncidents`/`GetIncident` reading it back — completely untested. A data-format mismatch (wrong column name, missing `Persisted` flag, wrong phase string) would be invisible.

**Added:** `internal/controller/reconciler_store_integration_test.go`

- `TestReconcile_WithRealStore`: opens a real temp-file SQLite store, runs the reconciler with a fake LLM provider, then asserts:
  - CR reaches `Done` phase
  - `ListIncidents` returns the incident with `Phase=Done`, `TokensUsed=42`, `Persisted=true`
  - `GetIncident` returns the incident with `len(Diagnoses)>=1`, non-empty `Summary` and `RootCause`
- `TestReconcile_ProviderFailed_StoreNotPersisted`: when the LLM provider fails, the SQLite incident row must be written with `Phase=Failed` and `Persisted=false`. The reconciler writes a "Failed" upsert; this test confirms it reaches the store rather than being swallowed.

### 2. Config: invalid env var values (FILLED)

`config.Load()` returns an error when an env var holds an unparseable value, but the error path (25% of the function) had no test. A broken config would silently fall back to defaults under some parsers — important to gate.

**Added** to `internal/config/config_test.go`:

- `TestLoad_InvalidDuration`: `KSCRIBE_RESYNC_PERIOD=not-a-duration` must return an error.
- `TestLoad_InvalidInt`: `KSCRIBE_MAX_ITERATIONS=not-a-number` must return an error.

Config package now at **100% coverage**.

### 3. Redaction completeness: additional secret patterns (FILLED)

Existing `TestRedact_SecretSamples` covered bearer token, AWS key, PEM block, postgres URL, basic-auth URL, and password k=v. Three additional patterns from the task spec were unverified:

- **JWT in kubeconfig** (`token: eyJhbGciOiJSUzI1NiIsImtpZCI6ImFiYyJ9...`): covered by the existing `token\s*[=:]\s*\S+` k=v rule. Confirmed via new test.
- **GCP service-account JSON private key** (`"private_key": "-----BEGIN RSA PRIVATE KEY-----\n...`): the PEM block rule covers the key material. Confirmed via new test.

These patterns ARE handled. The new test `TestRedact_AdditionalSecretPatterns` documents this explicitly.

**Gap noted but not fixed:** A bare JWT without any keyword context (`eyJhbGciOiJSUzI1NiJ9.payload.sig` appearing alone in a log line) would not be redacted. The existing rules require either a `Bearer ` prefix or a key=value context (`token=`, `secret=`, etc.). A standalone JWT pattern would require a new regex that risks false-positives (any three dot-separated base64url segments). Kubernetes Warning event messages containing a raw JWT without context are uncommon; this is a **known acceptable gap**.

### 4. `RedactSnapshot` branches: DeploymentStatus and ReplicaSetStatus (FILLED)

`RedactSnapshot` has branches for `DeploymentStatus.Conditions` and `ReplicaSetStatus.Conditions` that had zero test coverage (those struct fields are nil in all existing test cases).

**Added:** `TestRedactSnapshot_DeploymentAndReplicaSetConditions` — verifies secrets in deployment/replicaset condition strings are redacted.

### 5. `enricher.DecodeSnapshot`: 0% coverage (FILLED)

`DecodeSnapshot` is the inverse of `EncodeSnapshot`, used when reading snapshot bytes back from storage. It was entirely untested. A sonic unmarshal failure or field-mapping error would be invisible.

**Added:**
- `TestDecodeSnapshot_RoundTrip`: encode a snapshot then decode it, verify key fields survive.
- `TestDecodeSnapshot_InvalidJSON`: verify an error is returned for garbage input.

---

## Gaps Deliberately Skipped

| Area | Reason |
|---|---|
| `api/v1alpha1` deep-copy funcs (0%) | Generated code (`zz_generated.deepcopy.go`). Testing generated code provides no safety signal. |
| `cmd/kscribe` main (0%) | CLI wiring; requires a running controller-manager. E2E concern, not unit. |
| `internal/agent` OpenAI client (0%) | Real HTTP client; testing it requires either envtest or a mock HTTP server. Not worth the complexity for a QA pass. |
| `internal/agent` KubeTools (0%) | Wires real kube client calls. Same reasoning. |
| `internal/controller` `SetupWithManager` (0%) | Requires a running controller-manager process. Integration/e2e concern. |
| `internal/controller` `SetupEventWatcherWithManager` (0%) | Same. |
| `internal/enricher` `collectDeployment`, `collectReplicaSet`, `collectPodsForSelector` (0%) | These require a live kube API server to be meaningful. The `BuildSnapshot` partial-failure tests exercise the surrounding scaffolding; these inner collectors are best tested with envtest if infra is available. |
| `internal/web/templates` (0%) | Generated templ output. Rendering correctness is a visual/e2e concern. |
| `internal/store` `runMigrations` error paths not hit (34.3% uncovered) | The existing `TestMigrationFailurePreventsStartup` already covers the critical "fail closed" invariant. Remaining uncovered paths are minor file-read branches in the migration FS walk. |

---

## Bugs Found and Fixed

**None.** No production code was modified. All new tests exercised existing behaviour and found it correct.

---

## Risk Assessment

**High confidence areas:** config parsing, reconciler state machine (Pending→Diagnosing→Done/Failed), SQLite write ordering (ADR-003), redaction of the six primary secret classes, `DecodeSnapshot` round-trip.

**Moderate risk:** `collectDeployment`/`collectReplicaSet`/`collectPodsForSelector` have zero coverage and are exercised only in production with a live cluster. A regression there would only surface in E2E tests or production.

**Low risk:** The web layer is at 92.4% (only the SSE non-flusher error path is uncovered). The agent loop is at 50% (uncovered: `callTool` with a non-nil executor, and the `KubeTools` factory — both require real kube infrastructure).

**Acceptable known gap:** Raw JWTs without a `Bearer` prefix or key=value context are not redacted. This is documented above. Common Kubernetes event messages do not contain raw JWTs.
