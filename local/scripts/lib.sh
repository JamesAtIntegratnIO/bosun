#!/usr/bin/env bash
# Shared settings and helpers for the local proving ground.
#
# Sourced, never executed. Everything here is deliberately overridable from the
# environment so the same scripts work against a cluster you built by hand.

set -euo pipefail

: "${CLUSTER_CONTEXT:=kind-localdev}"
: "${IDP_HOST:=cnoe.localtest.me}"
: "${IDP_PORT:=8443}"
: "${GITEA_URL:=https://gitea.${IDP_HOST}:${IDP_PORT}}"
: "${ARGOCD_URL:=https://argocd.${IDP_HOST}:${IDP_PORT}}"
: "${GITEA_OWNER:=giteaAdmin}"
: "${SAMPLE_REPO_NAME:=delivery-sample}"
: "${KARGO_PROJECT:=delivery}"

# The same repository has two addresses, and which one you want depends on
# where you are standing.
#
#   GITEA_URL     through the ingress, TLS with a self-signed certificate.
#                 For anything running on the host: the seed push, the gate.
#   GITEA_SVC     the Service, plain HTTP, no certificate involved.
#                 For anything running in the cluster: Kargo, ArgoCD, the
#                 agent. Kargo's git-clone fails on the ingress address with
#                 `SSL certificate ... self-signed certificate (18)`, and
#                 teaching every in-cluster component to trust a throwaway CA
#                 is a lot of plumbing to reach a service two hops away.
: "${GITEA_SVC:=http://my-gitea-http.gitea.svc.cluster.local:3000}"
: "${GITEA_NAMESPACE:=gitea}"
: "${GITEA_SVC_PORT:=3000}"

# The chart the demo keeps current. Public, tiny, and with enough published
# versions that a bump is always available.
: "${DEMO_CHART_REPO:=https://stefanprodan.github.io/podinfo}"
: "${DEMO_CHART:=podinfo}"

# Used by every script that sources this file -- 20-seed, 30-kit, 95-reset --
# which shellcheck cannot see across the source boundary.
# shellcheck disable=SC2034
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
step() { printf '  \033[36m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32mok\033[0m    %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$*" >&2; return 1; }

kc() { kubectl --context "$CLUSTER_CONTEXT" "$@"; }

# Wait for a condition rather than sleeping. Every wait in these scripts has a
# deadline: a demo that hangs forever is worse than one that fails, because
# nobody can tell the difference between slow and stuck.
wait_for() {
  local desc="$1" deadline="$2"; shift 2
  local end=$((SECONDS + deadline))
  while [ $SECONDS -lt $end ]; do
    if "$@" >/dev/null 2>&1; then ok "$desc"; return 0; fi
    sleep 3
  done
  bad "$desc (waited ${deadline}s)"
}

# The agent pod, resolved safely.
#
# Every demo script used `get pod -l ... | head -1` and kept the answer for the
# rest of the run. During a rollout there are two pods matching that label, one
# of them terminating, and `head -1` picks by name order, so a script that
# starts moments after anything restarted the deployment can hold the name of a
# pod that is about to vanish. Every later `kc logs` then fails, and because
# those pipe into `wc -l` under `set -o pipefail`, the script dies with no
# message at all beyond kubectl's "pods not found".
#
# That killed the scenario replay on its first case, immediately after the
# egress demo had rolled the deployment twice. It also killed an earlier run
# that was written off at the time as "the pod was probably being replaced",
# which was correct, and should have been fixed then rather than noted.
#
# Waits for the rollout to settle, then returns a pod that is Running.
agent_pod() {
  kubectl --context "$CLUSTER_CONTEXT" -n bosun rollout status deploy/bosun-bosun \
    --timeout=180s >/dev/null 2>&1 || true
  kubectl --context "$CLUSTER_CONTEXT" -n bosun get pod \
    -l app.kubernetes.io/name=bosun \
    --field-selector=status.phase=Running -o name 2>/dev/null | head -1
}

# Gitea's certificate is self-signed, so every call to it needs -k. Wrapped so
# that fact lives in one place rather than being copied into a dozen curls.
gitea_api() {
  local method="$1" path="$2"; shift 2
  curl -sk -X "$method" \
    -H "Authorization: token ${GITEA_TOKEN}" \
    -H "Content-Type: application/json" \
    "${GITEA_URL}/api/v1${path}" "$@"
}

load_credentials() {
  GITEA_TOKEN="$(idpbuilder get secrets -p gitea -o json 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["token"])')"
  GITEA_PASSWORD="$(idpbuilder get secrets -p gitea -o json 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["password"])')"
  export GITEA_TOKEN GITEA_PASSWORD
}

# The address the agent dials argocd-server on, which is not the one a human
# uses. It is the Service, and whether it speaks TLS on it is a property of
# this ArgoCD rather than a preference: `server.insecure` in
# argocd-cmd-params-cm makes argocd-server serve plain HTTP, and idpbuilder
# sets it because ArgoCD sits behind the ingress here. Guessing wrong is the
# worst failure in this file, the pod hangs for the full timeout and dies
# blaming ArgoCD, so it is read rather than assumed.
#
# The pod port is 8080 either way, and that is the whole point of
# gate.argocd.podPort: the Service publishes 80 and 443 against the same
# container port, and a NetworkPolicy is matched after the ClusterIP has been
# DNAT'd away.
argocd_service_url() {
  local insecure
  insecure="$(kc -n argocd get cm argocd-cmd-params-cm \
    -o jsonpath='{.data.server\.insecure}' 2>/dev/null || true)"
  if [ "$insecure" = "true" ]; then
    printf 'http://argocd-server.argocd.svc'
  else
    printf 'https://argocd-server.argocd.svc'
  fi
}

# ---------------------------------------------------------------------------
# Reading the gate's verdict off a pull request.
#
# The gate is the agent: it sweeps the open pull requests and publishes a
# commit status and a report comment. So a demo asks Gitea what the gate said,
# rather than running anything itself.
# ---------------------------------------------------------------------------

head_sha() { # <pr> -> sha
  gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/$1" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["head"]["sha"])'
}

# One status, by context name. Gitea has no check-runs API, so this is the
# whole surface. It arrives newest first, but Gitea stamps whole seconds, and
# the gate posts `pending` and then its verdict inside one of them, so the
# order within that tie is arbitrary and taking the first match is a coin flip.
# A demo lost that flip on its first run: it watched a `pending` from 01:04:02
# while the `success` beside it, same second, went unread for 180s. Newest
# wins, and a tie is broken on meaning; a verdict cannot precede the pending
# that announced it. The same rule the Gitea client itself applies.
gate_status() { # <sha> -> "state description"
  gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/statuses/$1?limit=50" \
    | CTX="${GATE_CHECK_NAME:-gate}" python3 -c '
import json,os,sys
ctx = os.environ["CTX"]
best = None
for s in json.load(sys.stdin):
    if s.get("context") != ctx:
        continue
    at, state = s.get("created_at",""), s.get("status","")
    if best is None or at > best[0] or (at == best[0] and best[1] == "pending" and state != "pending"):
        best = (at, state, s.get("description",""))
if best:
    print(best[1], best[2])'
}

status_is() { # <sha> <state> -> bool
  gate_status "$1" | grep -q "^$2"
}

# The gate's own report comment: the newest one carrying the marker. The gate
# edits its comment in place rather than appending per push, so on a settled
# pull request there is exactly one.
gate_report() { # <pr> -> the report body
  gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/issues/$1/comments?limit=50" \
    | python3 -c '
import json,sys
bodies = [c["body"] for c in json.load(sys.stdin) if "<!-- gitops-gate -->" in c["body"]]
print(bodies[-1] if bodies else "")'
}
