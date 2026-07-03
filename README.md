# kscribe

kscribe is a Kubernetes operator that automatically diagnoses events using an LLM and persists RCA results into a SQLite history mirror.

---

## Limitations

**CON-005 — core v1 Events only.** kscribe watches `core/v1 Event` objects (Warning type) exclusively. It does not watch custom events or metrics signals. Pod-log enrichment is available via the tool executor (future wire-in); the MVP uses spec fields from the triggered event.

---

## Security notice — LLM data egress (SEC-003)

kscribe sends enriched, redacted cluster context (event messages, pod metadata, log lines) to the configured LLM provider (default: OpenAI). Sensitive strings matching known patterns (tokens, passwords, PEM keys, connection strings) are scrubbed before transmission (`KSCRIBE_REDACT_ENABLED=true` by default). You remain responsible for reviewing what cluster data leaves your environment. Do not disable redaction in production.

---

## In-cluster deployment

The Helm chart (`charts/kscribe`) is the single source of truth for the install.
`deploy/kscribe.yaml` is **generated** from it (`scripts/build-manifest.sh`) for
users who prefer plain `kubectl` — do not edit it by hand. See
[docs/manifests.md](docs/manifests.md) for the full pipeline and what to edit where.

### Option A — Helm (recommended)

```sh
helm install kscribe ./charts/kscribe \
  --namespace kscribe-system --create-namespace \
  --set llm.apiKey=<your-openai-api-key>
```

See [charts/kscribe/README.md](charts/kscribe/README.md) for all values
(image, resources, persistence, existing-secret, default policy).

### Option B — plain kubectl

```sh
kubectl apply -f deploy/kscribe.yaml
# the bundle ships an empty kscribe-llm Secret; set your key into it:
kubectl create secret generic kscribe-llm \
  --namespace kscribe-system \
  --from-literal=api-key=<your-openai-api-key> \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Verify

```sh
kubectl rollout status deployment/kscribe -n kscribe-system
kubectl get kscribediagnoses -n kscribe-system
```

The dashboard is available via the `kscribe-dashboard` ClusterIP Service on port 8080. Port-forward for local access:

```sh
kubectl port-forward svc/kscribe-dashboard 8080:8080 -n kscribe-system
```

---

## LLM provider

kscribe talks to any OpenAI-compatible chat-completions API. Configure it with
`KSCRIBE_LLM_PROVIDER` / `KSCRIBE_LLM_MODEL` / `KSCRIBE_LLM_BASE_URL` (or the
chart's `llm.*` values).

| Provider | `llm.provider` | `llm.model` (example) | Base URL |
|----------|----------------|-----------------------|----------|
| OpenAI | `openai` (default) | `gpt-4o-mini` | default |
| Google Gemini | `google` | `gemini-2.0-flash` | auto (Gemini OpenAI endpoint) |
| Z.AI (Zhipu GLM) | `zai` | `glm-4.6` | auto (Z.AI OpenAI endpoint) |
| Groq | `groq` | `llama-3.3-70b-versatile` | auto (Groq OpenAI endpoint) |
| Other (Ollama, vLLM, …) | `openai` | model name | set `llm.baseURL` |

Gemini (uses Google's OpenAI-compatible endpoint — no extra config beyond the key):

```sh
helm upgrade --install kscribe ./charts/kscribe \
  --namespace kscribe-system --create-namespace \
  --set llm.provider=google \
  --set llm.model=gemini-2.0-flash \
  --set llm.apiKey=$GEMINI_API_KEY
```

`llm.baseURL` overrides the endpoint for any other OpenAI-compatible server.

### Local end-to-end smoke test

`scripts/local-test.sh` runs the whole loop on a local cluster (minikube or
colima/k3s): build → load image → `helm install` → fire failing pods → wait for
diagnoses → print RCAs → clean up.

```sh
# defaults target LM Studio at http://192.168.100.37:1234/v1
LLM_BASE_URL=http://<host>:1234/v1 LLM_MODEL=google/gemma-4-e4b scripts/local-test.sh

KEEP=1 scripts/local-test.sh        # leave it running afterwards
SKIP_BUILD=1 scripts/local-test.sh  # reuse the image already in the cluster
UNINSTALL=1 scripts/local-test.sh   # helm uninstall at the end
```

---

## Custom Resource examples

### DiagnosisPolicy — namespace-scoped policy override

```yaml
apiVersion: kscribe.amjadjibon.dev/v1alpha1
kind: DiagnosisPolicy
metadata:
  name: my-policy
  namespace: my-app-namespace
spec:
  enabled: true
  eventReasons:
  - BackOff
  - OOMKilling
  llmProvider: openai
  llmModel: gpt-4o-mini
  maxIterations: 3
  redact: true
```

A `default` DiagnosisPolicy is automatically installed in `kscribe-system` and acts as the cluster-wide fallback when no namespace-scoped policy exists.

### KscribeDiagnosis — auto-created by the operator

KscribeDiagnosis CRs are created automatically from Warning events. You should not create them manually. Example of what the operator produces:

```yaml
apiVersion: kscribe.amjadjibon.dev/v1alpha1
kind: KscribeDiagnosis
metadata:
  name: ksd-<event-uid>
  namespace: kscribe-system
spec:
  involvedObjectKind: Pod
  involvedObjectName: my-pod
  involvedObjectNamespace: default
  reason: BackOff
  message: "Back-off restarting failed container"
  eventUID: "abc123"
status:
  phase: Done
  summary: "Container exits due to missing config mount"
  rootCause: "ConfigMap 'app-config' not found in namespace 'default'"
```

---

## Local development

```sh
# Build binary
make build

# Run all tests
make test

# Run go vet
make vet

# Regenerate deep-copy objects
make generate

# Regenerate CRD and RBAC manifests
make manifests

# Regenerate templ templates
make templ

# Rebuild deploy/kscribe.yaml and assert reproducibility
make manifest-check
```

Run locally against a cluster (requires KUBECONFIG or in-cluster config):

```sh
go run ./cmd/kscribe \
  --addr :8080 \
  --operator-namespace kscribe-system
```

Set `KSCRIBE_LLM_API_KEY` in your environment for LLM calls.

---

## Upgrades & migrations

Migrations run automatically at operator startup. **They fail closed (ADR-004):** if any migration cannot be applied cleanly, the process exits with an error rather than starting with a partially upgraded schema.

### Operational rollback procedure

1. Before upgrading, take a snapshot of the SQLite PVC (e.g. a VolumeSnapshot or a `cp` to a backup path).
2. Apply the new operator image.
3. If startup fails due to a migration error, restore the PVC snapshot to the pre-upgrade state and roll back the operator image.

The database is a queryable history mirror only — the CR status in the Kubernetes API remains the authoritative source of truth (ADR-003). Restoring the DB snapshot to a previous state does not affect active diagnoses.
