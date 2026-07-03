---
date: 2026-07-03
plan: docs/production-ready/PLAN.md
plan_version: 1.2
reviewer: Claude
verdict: Ready
---

# Plan Review: production-ready

## Verdict

**Ready** — all findings from the v1.1 review are resolved; task specs now match the verified codebase (phase enum, schema columns, chi router) and every phase has runnable completion criteria.

## Findings

### [SUGGEST-001] Phase 2 completion criteria partly requires a live cluster

**Phase**: 2
**Issue**: "curl localhost:8081/metrics against a kind cluster" is a manual step; an agent running unattended can't fully self-verify the phase.
**Fix**: Add a unit-level check the agent can run: `prometheus/client_golang/prometheus/testutil.CollectAndCount` (already vendored) against the three collectors in `internal/metrics`, keeping the curl as the manual e2e confirmation (TEST-005 already covers it).

---

### [SUGGEST-002] Frontmatter goal omits Phase 5

**Phase**: Frontmatter
**Issue**: `goal:` reads "retention, metrics, auth, cost caps" — the dependency cleanup added in v1.1 isn't reflected, which slightly misleads anyone scanning `docs/*/PLAN.md` frontmatter.
**Fix**: Append ", stdlib JSON" to the goal line.

---

## Resolved from v1.1 review

- REVISE-001 (nonexistent `Completed` phase) — all references now use `Done | Partial | Failed`, `Partial` explicitly terminal, metrics label `done|partial|failed`.
- REVISE-002 (`last_seen` column) — now `updated_at` with the `datetime('now')` string-format caveat and index named.
- SUGGEST-001–003 (five-phase intro, chi middleware, explicit three-DELETE prune) — all applied.

## What's Good

- Task specs cite verified code facts (enum from `kscribediagnosis_types.go`, `idx_incidents_updated_at`, `fmtTimePtr`, the `patchStatus` conflict-retry helper) — agents won't trip on invented symbols.
- Completion criteria are commands (`go test`, `helm template --set`, `grep -rn sonic`), and Phase 5's grep-empty gate makes the dep removal binary.
- Risk section is honest about the irreversibility profile: no destructive migrations anywhere (prune is explicit DELETEs; sonic swap is behaviour-checked by existing round-trip tests per RISK-004/TEST-006).

## Machine-Readable Verdict

```yaml
verdict: Ready
block: 0
revise: 0
suggest: 2
blocking_ids: []
```
