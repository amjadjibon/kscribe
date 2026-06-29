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
| `defaultPolicy.eventReasons` | BackOff, OOMKilling, Failed, FailedScheduling | Reasons that trigger a diagnosis |

## CRDs

CRDs live in `crds/` and are installed by Helm on first install. Helm does **not**
upgrade or delete CRDs — to update them, apply `config/crd/bases/*.yaml` (or
`deploy/kscribe.yaml`) manually.

## Note

Enriched (redacted) cluster context is sent to the configured LLM provider.
Redaction is always enforced (SEC-001).
