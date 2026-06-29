#!/usr/bin/env bash
# build-manifest.sh — concatenates all kscribe resources into deploy/kscribe.yaml.
# Rerunning produces no diff when sources are unchanged (TASK-035 / TASK-038).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${REPO_ROOT}/deploy/kscribe.yaml"

# Regenerate CRDs and RBAC from Go source markers.
make -C "${REPO_ROOT}" manifests

mkdir -p "${REPO_ROOT}/deploy"

# Explicit, deterministic order: CRDs → Namespace → SA → ClusterRole → CRB → PVC → Svc → Deploy → CR
files=(
  "${REPO_ROOT}/config/crd/bases/kscribe.amjadjibon.dev_diagnosispolicies.yaml"
  "${REPO_ROOT}/config/crd/bases/kscribe.amjadjibon.dev_kscribediagnoses.yaml"
  "${REPO_ROOT}/config/manager/namespace.yaml"
  "${REPO_ROOT}/config/manager/serviceaccount.yaml"
  "${REPO_ROOT}/config/rbac/role.yaml"
  "${REPO_ROOT}/config/manager/clusterrolebinding.yaml"
  "${REPO_ROOT}/config/manager/pvc.yaml"
  "${REPO_ROOT}/config/manager/service.yaml"
  "${REPO_ROOT}/config/manager/deployment.yaml"
  "${REPO_ROOT}/config/manager/diagnosispolicy.yaml"
)

# Each source file already starts with ---; concatenating them is a valid
# multi-document YAML stream with no extra separators needed.
cat "${files[@]}" > "${OUT}"

echo "Written: ${OUT}"
