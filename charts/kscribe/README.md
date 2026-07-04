# kscribe Helm chart

Installs the kscribe operator: CRDs, RBAC, the manager Deployment, a SQLite PVC,
the dashboard Service, and a default `DiagnosisPolicy`.

## Install

```bash
helm install kscribe ./charts/kscribe \
  --namespace kscribe-system --create-namespace \
  --set llm.apiKey=<your-openai-key>
```

Or reference an existing Secret instead of passing the key inline:

```bash
helm install kscribe ./charts/kscribe \
  --namespace kscribe-system --create-namespace \
  --set llm.existingSecret=my-llm-secret --set llm.existingSecretKey=api-key
```

The operator boots without an API key and only needs it to call the LLM, so the
key is optional at install time.

## Common values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/amjadjibon/kscribe` | Manager image |
| `image.tag` | `""` (chart appVersion) | Image tag |
| `replicaCount` | `1` | Pinned to 1 (per-process Deduper + SQLite) |
| `llm.provider` / `llm.model` | `openai` / `gpt-4o-mini` | LLM backend |
| `llm.apiKey` | `""` | Inline key; creates a Secret |
| `llm.existingSecret` | `""` | Use an existing Secret instead |
| `persistence.size` | `1Gi` | SQLite PVC size |
| `persistence.storageClass` | `""` | PVC storage class |
| `persistence.existingClaim` | `""` | Reuse an existing PVC |
| `defaultPolicy.enabled` | `true` | Install the namespace default policy |
| `defaultPolicy.eventReasons` | BackOff, OOMKilling, Failed, FailedScheduling, Unhealthy, FailedMount, FailedAttachVolume, FailedCreate, FailedCreatePodSandBox, Evicted, BackoffLimitExceeded | Reasons that trigger a diagnosis |
| `retentionPeriod` | `720h` | Prune old incidents/diagnoses/chat rows and finished CRs hourly; `0` disables |
| `metrics.enabled` / `metrics.port` | `true` / `9090` | Prometheus endpoint with scrape annotations on the Service |
| `dashboard.token` | `""` (auth off) | Static bearer token for the dashboard; creates a Secret |
| `dashboard.existingSecret` | `""` | Use an existing Secret for the dashboard token instead |
| `maxDiagnosesPerHour` | `30` | Global LLM cost cap; over-limit CRs stay Pending and retry; `0` = unlimited |

## CRDs

CRDs live in `crds/` and are installed by Helm on first install. Helm does **not**
upgrade or delete CRDs — to update them, apply `config/crd/bases/*.yaml` (or
`deploy/kscribe.yaml`) manually.

## Note

Enriched (redacted) cluster context is sent to the configured LLM provider.
Redaction is always enforced (SEC-001).

LLM calls include guardrail system prompts that scope output to Kubernetes
incident diagnosis and remediation, and instruct the model to ignore unrelated
instructions embedded in logs, events, tool output, RCA text, or chat history.
Output is token-capped by request type: 1024 default, 900 per diagnosis turn,
500 for JSON repair, and 700 per incident-chat turn.

The dashboard and `KscribeDiagnosis` status record audit metadata for each run:
LLM provider, model, tokens used, start/completion timestamps, and persistence
state.
