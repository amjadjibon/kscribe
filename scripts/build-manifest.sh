#!/usr/bin/env bash
# build-manifest.sh — render deploy/kscribe.yaml from the Helm chart, the single
# source of truth. CRDs and RBAC are first regenerated from Go markers into the
# chart by `make manifests`. Rerunning produces no diff when sources are unchanged.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${REPO_ROOT}/deploy/kscribe.yaml"
NS="kscribe-system"

# Regenerate CRDs + RBAC from Go source markers into the chart.
make -C "${REPO_ROOT}" manifests

mkdir -p "${REPO_ROOT}/deploy"

# helm template doesn't emit a Namespace (install uses --create-namespace), so the
# flat kubectl manifest prepends one for a self-contained `kubectl apply -f`.
{
  echo "# GENERATED from charts/kscribe by scripts/build-manifest.sh — do not edit."
  echo "apiVersion: v1"
  echo "kind: Namespace"
  echo "metadata:"
  echo "  name: ${NS}"
  helm template kscribe "${REPO_ROOT}/charts/kscribe" \
    --include-crds \
    --namespace "${NS}"
} > "${OUT}"

echo "Written: ${OUT}"
