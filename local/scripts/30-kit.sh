#!/usr/bin/env bash
# Installs the kit: Bosun and the pipelines.
#
# Everything comes from the WORKING TREE -- the agent image is built here, and
# the charts are the directories beside this one. That is the point of a
# proving ground: it exercises the code in front of you, not whatever was last
# published. It also sidesteps the architecture question, since the published
# agent image is amd64 and this machine may not be.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials
: "${LLM_BASE_URL:?set LLM_BASE_URL to an OpenAI-compatible endpoint the cluster can reach, e.g. http://<host>:1234/v1}"
: "${LLM_MODEL:=qwen/qwen3.5-9b}"
: "${KIND_CLUSTER:=localdev}"
: "${AGENT_IMAGE:=bosun:local}"

# Two addresses for one repository, because two consumers disagree about what
# is acceptable:
#
#   ArgoCD and the agent take the Service over plain HTTP. No certificate, no
#   trust store, nothing to configure.
#
#   Kargo will not. It REFUSES to send credentials to a plain-HTTP endpoint
#   ("refused to get credentials for insecure HTTP endpoint"), which is a
#   defensible rule and one you only discover from a controller log -- the
#   promotion itself fails at `git push` with "could not read Username",
#   naming nothing. So Kargo gets the ingress address over HTTPS, and skips
#   verification of its self-signed certificate.
REPO_URL="${GITEA_SVC}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
KARGO_REPO_URL="${GITEA_URL}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
GITEA_ROOT="${GITEA_SVC}"

# The model endpoint is off-cluster, and the chart's allowPublicHTTPS rule
# excepts every RFC1918 range -- so a model on your LAN needs its own explicit
# ipBlock or the agent hangs with zero bytes and no error.
LLM_HOST="$(printf '%s' "$LLM_BASE_URL" | sed -E 's#^https?://##; s#[:/].*$##')"
LLM_PORT="$(printf '%s' "$LLM_BASE_URL" | sed -nE 's#^https?://[^:/]+:([0-9]+).*#\1#p')"
: "${LLM_PORT:=80}"

# The NetworkPolicy is ON, and it is not decoration: kindnet in this cluster
# ENFORCES NetworkPolicy. Measured, not assumed -- a busybox pod reaches
# 1.1.1.1 with no policy and hangs under a deny-all, which is the same
# zero-bytes hang every egress incident in this project produced. So the rules
# below are load-bearing: get the apiserver endpoints wrong and the agent
# crash-loops instead of quietly answering "not permitted to check".

# Live reads need the apiserver, and the apiserver is the destination people
# get wrong. `kubernetes.default.svc` is a ClusterIP, and a ClusterIP is DNAT'd
# to a real endpoint BEFORE NetworkPolicy is evaluated -- so an ipBlock naming
# it matches nothing and the connection hangs with zero bytes rather than being
# refused. Ask the Service what it actually points at.
APISERVER_EPS="$(kc -n default get endpoints kubernetes \
  -o jsonpath='{range .subsets[*]}{range .addresses[*]}{.ip}{"\n"}{end}{end}' | sed '/^$/d')"
APISERVER_PORT="$(kc -n default get endpoints kubernetes \
  -o jsonpath='{.subsets[0].ports[0].port}')"
: "${APISERVER_PORT:=6443}"
[ -n "$APISERVER_EPS" ] || bad "could not read the kubernetes Service endpoints"

# The API groups the agent may count objects in. `groups` scope, not `wide`:
# "everything except the core group" is the intent everyone has here and it is
# not expressible in Kubernetes RBAC, so this names the groups whose CRDs this
# cluster actually ships and leaves Secrets unreadable.
: "${LIVE_READ_GROUPS:=cert-manager.io acme.cert-manager.io argoproj.io kargo.akuity.io}"

# The account whose gate report the agent will believe.
#
# There is no per-host default that can be right here: GitHub Actions always
# comments as `github-actions[bot]`, and Gitea has no equivalent fixed identity
# -- the report arrives as whichever user minted the CI token. In this ground
# that is gate-run.sh, running as the admin. Naming it is what makes a forged
# report -- anyone who can comment can write the gate's marker -- a comment the
# agent ignores by author rather than an instruction wearing the gate's
# authority.
: "${GATE_REPORT_AUTHOR:=${GITEA_OWNER}}"

say "the agent's own account"
# The agent authenticates as whoever owns its token. Hand it the admin's and
# every comment and commit it makes carries the admin's name -- which is
# indistinguishable, at a glance, from a colleague having written it. So it
# gets a user of its own, and mints its own token as that user.
: "${AGENT_USER:=bosun}"
: "${AGENT_PASSWORD:=bosun-local-not-a-secret}"
: "${AGENT_BRAND:=Bosun}"
: "${AGENT_BRAND_MARK:=⚓}"

if gitea_api GET "/users/${AGENT_USER}" -o /dev/null -w '%{http_code}' | grep -q 200; then
  step "user ${AGENT_USER} exists"
else
  gitea_api POST "/admin/users" -d "$(python3 -c "
import json,sys
print(json.dumps({'username':sys.argv[1],'email':sys.argv[1]+'@localtest.me',
                  'password':sys.argv[2],'must_change_password':False}))" \
    "$AGENT_USER" "$AGENT_PASSWORD")" >/dev/null
  step "created ${AGENT_USER}"
fi

# The bot needs write access to the repository it fixes.
curl -sk -X PUT -H "Authorization: token ${GITEA_TOKEN}" \
  "${GITEA_URL}/api/v1/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/collaborators/${AGENT_USER}" \
  -H 'Content-Type: application/json' -d '{"permission":"write"}' >/dev/null
step "granted write on ${SAMPLE_REPO_NAME}"

# Tokens are minted by the user themselves, with basic auth -- an admin token
# cannot mint one on someone else's behalf.
AGENT_TOKEN="$(curl -sk -u "${AGENT_USER}:${AGENT_PASSWORD}" \
  -X POST "${GITEA_URL}/api/v1/users/${AGENT_USER}/tokens" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"agent-$(date +%s)\",\"scopes\":[\"write:repository\",\"write:issue\",\"read:user\"]}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("sha1",""))')"
[ -n "$AGENT_TOKEN" ] || bad "could not mint a token for ${AGENT_USER}"
ok "minted a token for ${AGENT_USER}"

say "agent credentials"
kc get namespace bosun >/dev/null 2>&1 || kc create namespace bosun >/dev/null
kc -n bosun delete secret agent-git >/dev/null 2>&1 || true
kc -n bosun create secret generic agent-git \
  --from-literal=token="$AGENT_TOKEN" >/dev/null
ok "agent-git"

say "building the agent image from the working tree"
docker build -q -t "$AGENT_IMAGE" "$ROOT/.." >/dev/null
ok "built $AGENT_IMAGE"
# kind nodes have their own image store; a locally built image is invisible
# until it is loaded, and the pod would sit ImagePullBackOff against a registry
# that has never heard of this tag.
command -v kind >/dev/null 2>&1 || bad "kind is not on PATH -- idpbuilder embeds it but does not install it"
kind load docker-image "$AGENT_IMAGE" --name "$KIND_CLUSTER" 2>&1 | sed 's/^/    /'
ok "loaded into kind/$KIND_CLUSTER"

# --set takes indexed paths, so a list is built rather than written. Kept next
# to the call it feeds so the indices and the flag cannot drift apart.
LIVE_READ_ARGS=""
i=0
for g in $LIVE_READ_GROUPS; do
  LIVE_READ_ARGS="${LIVE_READ_ARGS} --set liveReads.apiGroups[${i}]=${g}"
  i=$((i + 1))
done
APISERVER_ARGS=""
i=0
while read -r ip; do
  [ -n "$ip" ] || continue
  APISERVER_ARGS="${APISERVER_ARGS} --set networkPolicy.egress.apiServer.ipBlocks[${i}].cidr=${ip}/32"
  APISERVER_ARGS="${APISERVER_ARGS} --set networkPolicy.egress.apiServer.ipBlocks[${i}].port=${APISERVER_PORT}"
  i=$((i + 1))
done <<< "$APISERVER_EPS"

say "bosun"
helm upgrade --install bosun "$ROOT/../charts/bosun" \
  --kube-context "$CLUSTER_CONTEXT" \
  --namespace bosun \
  --set image.repository="${AGENT_IMAGE%%:*}" \
  --set image.tag="${AGENT_IMAGE##*:}" \
  --set image.pullPolicy=Never \
  --set git.provider=gitea \
  --set git.apiBase="$GITEA_ROOT" \
  --set git.owner="$GITEA_OWNER" \
  --set branding.name="$AGENT_BRAND" \
  --set branding.mark="$AGENT_BRAND_MARK" \
  --set git.author.name="$AGENT_BRAND" \
  --set git.author.email="${AGENT_USER}@localtest.me" \
  --set git.repo="$SAMPLE_REPO_NAME" \
  --set git.repoURL="$REPO_URL" \
  --set git.insecureSkipTLSVerify=false \
  --set "networkPolicy.egress.namespaces[0].name=${GITEA_NAMESPACE}" \
  --set "networkPolicy.egress.namespaces[0].ports[0]=${GITEA_SVC_PORT}" \
  --set "networkPolicy.egress.ipBlocks[0].cidr=${LLM_HOST}/32" \
  --set "networkPolicy.egress.ipBlocks[0].port=${LLM_PORT}" \
  --set git.existingSecret=agent-git \
  --set llm.provider=openai \
  --set llm.baseURL="$LLM_BASE_URL" \
  --set llm.model="$LLM_MODEL" \
  --set gate.checkName=gate \
  --set gate.wait=3m \
  --set gate.poll=10s \
  --set 'triage.allowPaths[0]=apps/**' \
  --set 'triage.allowPaths[1]=addons/**' \
  --set gate.reportAuthor="$GATE_REPORT_AUTHOR" \
  --set liveReads.enabled=true \
  --set liveReads.scope=groups \
  --set liveReads.argocdNamespace=argocd \
  ${LIVE_READ_ARGS} \
  ${APISERVER_ARGS} \
  --set networkPolicy.enabled=true \
  --set networkPolicy.egress.allowPublicHTTPS=true \
  --set 'triage.egressDeny[0]=*.invalid.localtest.me' \
  --wait --timeout 5m >/dev/null
# The image tag never changes, so helm sees an identical pod spec and keeps the
# running pod -- with the OLD binary in it. Every rebuild therefore needs an
# explicit rollout, or you spend an hour debugging code that is not running.
kc -n bosun rollout restart deploy/bosun-bosun >/dev/null
kc -n bosun rollout status deploy/bosun-bosun --timeout=180s >/dev/null
ok "bosun ready (restarted onto the freshly built image)"

say "kargo-pipelines"
helm upgrade --install kargo-pipelines "$ROOT/../charts/kargo-pipelines" \
  --kube-context "$CLUSTER_CONTEXT" \
  --namespace kargo \
  -f "$ROOT/values/kargo-pipelines.yaml" \
  --set git.repoURL="$KARGO_REPO_URL" \
  --set git.insecureSkipTLSVerify=true \
  --wait --timeout 5m >/dev/null
ok "warehouse and stages created"

say "kargo git credentials"
# After the chart, never before: kargo-pipelines owns the Project namespace
# and helm refuses to adopt one that already exists without its ownership
# labels. Kargo also matches credentials to a repository by NORMALISED URL,
# so this repoURL and the Warehouse's must agree down to the trailing .git.
kc -n "$KARGO_PROJECT" delete secret sample-repo >/dev/null 2>&1 || true
kc -n "$KARGO_PROJECT" create secret generic sample-repo \
  --from-literal=repoURL="$KARGO_REPO_URL" \
  --from-literal=username="$GITEA_OWNER" \
  --from-literal=password="$GITEA_TOKEN" >/dev/null
kc -n "$KARGO_PROJECT" label secret sample-repo \
  kargo.akuity.io/cred-type=git --overwrite >/dev/null
ok "git credentials in ${KARGO_PROJECT}"


say "kit installed"
kc -n kargo get warehouses,stages -A --no-headers 2>/dev/null | sed 's/^/  /' || true
