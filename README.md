# kscribe

kscribe is a Kubernetes operator that automatically diagnoses events using an LLM and persists RCA results into a SQLite history mirror.

## Upgrades & migrations

Migrations run automatically at operator startup. **They fail closed (ADR-004):** if any migration cannot be applied cleanly, the process exits with an error rather than starting with a partially upgraded schema.

### Operational rollback procedure

1. Before upgrading, take a snapshot of the SQLite PVC (e.g. a VolumeSnapshot or a `cp` to a backup path).
2. Apply the new operator image.
3. If startup fails due to a migration error, restore the PVC snapshot to the pre-upgrade state and roll back the operator image.

The database is a queryable history mirror only — the CR status in the Kubernetes API remains the authoritative source of truth (ADR-003). Restoring the DB snapshot to a previous state does not affect active diagnoses.
