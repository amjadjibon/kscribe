#!/usr/bin/env bash
# local-test.sh — end-to-end smoke test for kscribe on a local cluster.
#
# Builds the image, loads it into the cluster (minikube or colima/k3s), installs
# the chart pointed at an LLM, fires off failing pods to generate Warning events,
# waits for the operator to diagnose them, prints the results, and cleans up.
#
# Usage:
#   scripts/local-test.sh                 # full run with defaults (LM Studio)
#   LLM_BASE_URL=... LLM_MODEL=... scripts/local-test.sh
#   KEEP=1 scripts/local-test.sh          # leave pods + release running
#   SKIP_BUILD=1 scripts/local-test.sh    # reuse the image already in the cluster
#   UNINSTALL=1 scripts/local-test.sh     # helm uninstall at the end
#
# Config (env overridable):
NS="${NS:-kscribe-system}"
RELEASE="${RELEASE:-kscribe}"
IMAGE="${IMAGE:-ghcr.io/amjadjibon/kscribe:latest}"
# LLM target. Default: LM Studio on the host's LAN IP. The base URL must include
# the API version segment (the client appends /chat/completions).
LLM_PROVIDER="${LLM_PROVIDER:-openai}"
LLM_BASE_URL="${LLM_BASE_URL:-https://api.groq.com/openai/v1}"
LLM_MODEL="${LLM_MODEL:-openai/gpt-oss-20b}"
LLM_API_KEY="${LLM_API_KEY:-local-no-key}"
TIMEOUT="${TIMEOUT:-180}"   # seconds to wait for diagnoses

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
log() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

for t in kubectl helm; do command -v "$t" >/dev/null || die "$t not found"; done
kubectl cluster-info >/dev/null 2>&1 || die "no reachable cluster (current-context: $(kubectl config current-context 2>/dev/null))"

# --- build + load image into the cluster ---------------------------------------
load_image() {
  local ctx; ctx="$(kubectl config current-context 2>/dev/null || true)"
  if command -v minikube >/dev/null && [ "$ctx" = "minikube" ]; then
    log "Building inside minikube docker-env"
    eval "$(minikube docker-env)"
    docker build -t "$IMAGE" .
  elif command -v colima >/dev/null && colima status >/dev/null 2>&1; then
    log "Building with docker, importing into colima k3s (containerd k8s.io)"
    docker build -t "$IMAGE" .
    docker save "$IMAGE" | colima ssh -- sudo ctr -n k8s.io images import -
  else
    log "Building with docker (assuming the cluster can see local images)"
    docker build -t "$IMAGE" .
  fi
}

# --- failing workloads that emit allowlisted Warning reasons -------------------
TEST_PODS=(crasher badimage toobig)
generate_incidents() {
  log "Creating failing pods in $NS (BackOff / Failed / FailedScheduling)"
  kubectl -n "$NS" delete pod "${TEST_PODS[@]}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "$NS" run crasher  --image=busybox --restart=Always -- /bin/false >/dev/null
  kubectl -n "$NS" run badimage --image=nope/doesnotexist:latest --restart=Always >/dev/null
  kubectl -n "$NS" run toobig --restart=Never --image=busybox \
    --overrides='{"spec":{"containers":[{"name":"toobig","image":"busybox","command":["sleep","3600"],"resources":{"requests":{"cpu":"1000"}}}]}}' >/dev/null
}

cleanup() {
  [ -n "${KEEP:-}" ] && { log "KEEP set — leaving pods and release in place"; return; }
  log "Cleaning up test pods"
  kubectl -n "$NS" delete pod "${TEST_PODS[@]}" --ignore-not-found >/dev/null 2>&1 || true
  if [ -n "${UNINSTALL:-}" ]; then
    log "Uninstalling release $RELEASE"
    helm uninstall "$RELEASE" -n "$NS" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# --- run -----------------------------------------------------------------------
[ -n "${SKIP_BUILD:-}" ] || load_image

log "Installing chart ($RELEASE) into $NS"
helm upgrade --install "$RELEASE" ./charts/kscribe \
  --namespace "$NS" --create-namespace \
  --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" \
  --set llm.provider="$LLM_PROVIDER" \
  --set llm.model="$LLM_MODEL" \
  --set llm.apiKey="$LLM_API_KEY" \
  >/dev/null

# Set the base URL directly so this works regardless of chart version.
kubectl -n "$NS" set env deploy/"$RELEASE" \
  KSCRIBE_LLM_PROVIDER="$LLM_PROVIDER" \
  KSCRIBE_LLM_BASE_URL="$LLM_BASE_URL" \
  KSCRIBE_LLM_MODEL="$LLM_MODEL" >/dev/null

log "Waiting for the operator to be ready"
kubectl -n "$NS" rollout status deploy/"$RELEASE" --timeout=120s

# Fresh start so we only measure this run's incidents.
kubectl -n "$NS" delete ksd --all >/dev/null 2>&1 || true
generate_incidents

log "Waiting up to ${TIMEOUT}s for diagnoses (provider=$LLM_PROVIDER model=$LLM_MODEL)"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  phases="$(kubectl -n "$NS" get ksd -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null || true)"
  total="$(grep -c . <<<"$phases" || true)"
  pending="$(grep -cE '^(Pending|Diagnosing)?$' <<<"$phases" || true)"
  done_n="$(grep -c '^Done$' <<<"$phases" || true)"
  printf '\r  incidents: %s total, %s done, %s in-progress ' "${total:-0}" "${done_n:-0}" "${pending:-0}"
  if [ "${total:-0}" -gt 0 ] && [ "${pending:-0}" -eq 0 ]; then echo; break; fi
  [ "$(date +%s)" -ge "$deadline" ] && { echo; log "timeout — some incidents still in progress"; break; }
  sleep 4
done

log "Results"
kubectl -n "$NS" get ksd

# Show one full RCA if any incident reached Done.
done_name="$(kubectl -n "$NS" get ksd -o jsonpath='{range .items[?(@.status.phase=="Done")]}{.metadata.name}{"\n"}{end}' 2>/dev/null | head -1)"
if [ -n "$done_name" ]; then
  log "Sample RCA ($done_name)"
  kubectl -n "$NS" get ksd "$done_name" -o jsonpath='{.status}' \
    | python3 -m json.tool 2>/dev/null | grep -iE 'phase|summary|rootCause|remediation|tokensUsed|persisted' | head -20 || true
else
  log "No incident reached Done — check the LLM target is reachable from the cluster:"
  echo "  kubectl -n $NS logs deploy/$RELEASE --tail=30"
fi

log "Done. (set KEEP=1 to leave it running, UNINSTALL=1 to remove the release)"
