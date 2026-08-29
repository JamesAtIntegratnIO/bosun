#!/usr/bin/env bash
# Act three: one pull request, both repairs.
#
# A chart stops serving an API version the repository still declares. The gate
# blocks, names the versions and the survivor, and the agent repairs, but the
# repair is not one thing, and this act exists to show both halves in a single
# comment.
#
#   The swap, with no model involved. `registry-pull.yaml` uses only fields
#   the new schema also has, so rewriting one apiVersion line is the whole job.
#   The gate's own report names the kind, the dropped version and the
#   destination; the rewrite is a deterministic function of it and the re-run
#   gate re-counts the consumers itself.
#
#   The reshape, where the swap alone is a silent data loss.
#   `platform-secrets.yaml` uses external-secrets v1alpha1's
#   `dataFrom: [{key, property, version}]`, which v1 replaced with
#   `dataFrom: [{extract: {key, property, version}}]`. Swap the version line and
#   the document still parses, still applies, and has every one of those fields
#   pruned by the apiserver on the way in. The render is fine. The gate goes
#   green. The secret quietly stops resolving.
#
#   Nobody can enumerate that in advance, so the model is shown the old schema
#   (from the CustomResourceDefinition the cluster serves right now, which
#   after this merge is gone) and the new one, and asked to translate. The
#   prompt is not what makes it safe: identity, schema-validity and value
#   provenance are all checked before a byte is written.
#
# Why external-secrets and not cert-manager. This act used to bump cert-manager
# v1.5.5 -> v1.6.0, and it worked for a while by accident: the sample repo's
# Application was being synced, so ArgoCD installed a 2021 cert-manager over
# the platform's and that is what served the v1alpha2 schema the structural
# check needs. Separating the demo's manifests from the real platform removed
# the accident, the schema went with it, and the act silently degraded to the
# swap, correctly reporting "the cluster serves no schema for ... at v1alpha2",
# which is the honest failure and not a demo.
#
# external-secrets is not part of the platform, so this act can own its own
# preconditions outright: it installs the old CRDs itself, explicitly, and says
# so. A demo whose premise is "the repository still declares v1alpha1" needs a
# cluster that still serves it, and that is a thing to arrange rather than to
# inherit.
#
#   usage: 70-demo-structural.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials
BRANCH="bump/external-secrets-0.16.0"
OLD_CHART="0.15.0"
NEW_CHART="0.16.0"
CHART_REPO="https://charts.external-secrets.io"

say "0. the precondition: a cluster that still serves v1alpha1"
# CRDs only. The agent reads the schema of the version being left; it never
# talks to the controller, and running a whole secrets operator to expose one
# openAPIV3Schema would be theatre. Server-side: these CRDs are far past the
# annotation limit a client-side apply writes.
command -v yq >/dev/null 2>&1 || bad "yq is not on PATH; it is what selects the CRDs out of the render"
helm template external-secrets external-secrets \
  --repo "$CHART_REPO" --version "$OLD_CHART" \
  --include-crds --set crds.create=true 2>/dev/null \
  | yq e 'select(.kind == "CustomResourceDefinition")' - \
  | kc apply --server-side --force-conflicts -f - >/dev/null
# The assertion is the schema itself. Applying without checking would leave the
# most likely failure, a chart that stopped shipping the version, a filter that
# matched nothing, looking exactly like success.
SERVED="$(kc get crd externalsecrets.external-secrets.io \
  -o jsonpath='{range .spec.versions[*]}{.name}={.served} {end}' 2>/dev/null)"
case "$SERVED" in
  *v1alpha1=true*) ok "externalsecrets.external-secrets.io serves: ${SERVED}" ;;
  *) bad "the cluster does not serve v1alpha1 (${SERVED:-nothing}); the structural check has no old schema to read" ;;
esac

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
CLONE="https://${GITEA_OWNER}:${GITEA_TOKEN}@gitea.${IDP_HOST}:${IDP_PORT}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
GIT_SSL_NO_VERIFY=true git clone -q "$CLONE" "$WORK/repo"
git -C "$WORK/repo" config http.sslVerify false
git -C "$WORK/repo" config user.name "a hurried human"
git -C "$WORK/repo" config user.email "human@localtest.me"

say "1. a repository that still declares external-secrets.io/v1alpha1"
git -C "$WORK/repo" checkout -q main
mkdir -p "$WORK/repo/apps" "$WORK/repo/addons"
# No automated syncPolicy on purpose. The gate must render this Application at
# both versions; nothing should install it. ArgoCD will show it OutOfSync and
# that is the correct state, not a fault, the cluster's external-secrets CRDs
# are the ones step 0 put there.
cat > "$WORK/repo/apps/external-secrets.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external-secrets
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: external-secrets
  source:
    repoURL: ${CHART_REPO}
    chart: external-secrets
    targetRevision: ${OLD_CHART}
    helm:
      values: |
        crds:
          create: true
YAML
# Case one: the swap is the whole job. Every field here exists in v1.
cat > "$WORK/repo/addons/registry-pull.yaml" <<'YAML'
apiVersion: external-secrets.io/v1alpha1
kind: ExternalSecret
metadata:
  name: registry-pull
  namespace: platform
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: registry-pull
  data:
    - secretKey: .dockerconfigjson
      remoteRef:
        key: platform/registry
        property: dockerconfig
YAML
# Case two: the swap parses and loses six fields to the apiserver's pruner.
# v1alpha1 took `dataFrom: [{key, property, version}]`; v1 takes
# `dataFrom: [{extract: {key, property, version}}]`.
cat > "$WORK/repo/addons/platform-secrets.yaml" <<'YAML'
apiVersion: external-secrets.io/v1alpha1
kind: ExternalSecret
metadata:
  name: platform-secrets
  namespace: platform
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: platform-secrets
  dataFrom:
    - key: platform/production
      property: credentials
      version: "3"
YAML
git -C "$WORK/repo" add -A
git -C "$WORK/repo" commit -qm "chore(external-secrets): pin ${OLD_CHART} and two v1alpha1 ExternalSecrets" 2>/dev/null || true
git -C "$WORK/repo" push -q "$CLONE" main
ok "main declares external-secrets ${OLD_CHART} and two external-secrets.io/v1alpha1 ExternalSecrets"
step "registry-pull.yaml   : every field survives; the swap is enough"
step "platform-secrets.yaml: dataFrom moved under extract; the swap is not"

say "2. the bump that deletes the version"
git -C "$WORK/repo" checkout -q -B "$BRANCH"
sed -i.bak -E "s|^( *targetRevision: ).*|\1${NEW_CHART}|" "$WORK/repo/apps/external-secrets.yaml"
rm -f "$WORK/repo/apps/external-secrets.yaml.bak"
git -C "$WORK/repo" diff | grep -E '^[-+] ' | sed 's/^/    /'
git -C "$WORK/repo" commit -qam "chore(external-secrets): bump to ${NEW_CHART}"
git -C "$WORK/repo" push -q --force "$CLONE" "$BRANCH"

for old in $(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls?state=open" \
    | python3 -c "import json,sys;print(' '.join(str(p['number']) for p in json.load(sys.stdin) if p['head']['ref']=='$BRANCH'))"); do
  gitea_api PATCH "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/${old}" -d '{"state":"closed"}' >/dev/null
  step "closed the previous #${old}"
done
PR="$(gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls" \
  -d "$(BR="$BRANCH" V="$NEW_CHART" python3 -c 'import json,os; print(json.dumps({"head":os.environ["BR"],"base":"main","title":"chore(external-secrets): bump to "+os.environ["V"],"body":"A one-line version bump. external-secrets 0.16 stops serving the v1alpha1 API version."}))')" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("number",""))')"
[ -n "$PR" ] || bad "could not open a pull request"
ok "pull request #${PR}"
printf '    %s/%s/%s/pulls/%s\n' "$GITEA_URL" "$GITEA_OWNER" "$SAMPLE_REPO_NAME" "$PR"

say "3. the gate renders both versions and refuses"
# Nothing is run here. The agent sweeps the open pull requests and gates them
# itself, so this waits for the verdict it publishes.
SHA="$(head_sha "$PR")"
step "waiting for the sweep to render ${SHA:0:8} at both versions"
wait_for "the gate refused a change that deletes an API version in use" 300 \
  status_is "$SHA" failure
ok "blocked"
gate_report "$PR" | grep -E 'no longer serves|still declare' | sed 's/^/    /' | head -8

say "4. the agent repairs: swapping one, reshaping the other"
POD="$(agent_pod)"
[ -n "$POD" ] || bad "no agent pod"
BEFORE="$(kc -n bosun logs "$POD" | wc -l | tr -d ' ')"
HEAD_BEFORE="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/${PR}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["head"]["sha"])')"

BODY="$(PR="$PR" BR="$BRANCH" F="$OLD_CHART" T="$NEW_CHART" R="$CHART_REPO" python3 -c '
import json, os
print(json.dumps({
  "project": "delivery", "stage": "external-secrets", "promotion": "structural-demo",
  "artifact": os.environ["R"] + " external-secrets",
  "from": os.environ["F"], "to": os.environ["T"],
  "autoMerge": "never", "prNumber": int(os.environ["PR"]), "branch": os.environ["BR"],
  "files": ["apps/external-secrets.yaml"], "verifyApps": []}))')"
kc -n bosun exec -i "$POD" -- wget -q -O- --post-data "$BODY" \
  --header 'Content-Type: application/json' http://localhost:8080/v1/promotion-opened >/dev/null 2>&1 || true

for _ in $(seq 1 90); do
  kc -n bosun logs "$POD" | tail -n +$((BEFORE + 1)) \
    | grep -qE "PR ${PR}: (triage done|triage failed)" && break
  sleep 5
done
kc -n bosun logs "$POD" | tail -n +$((BEFORE + 1)) \
  | grep -E "outbound|PR ${PR}:" | sed 's/^/    /'

say "5. what it wrote"
HEAD_AFTER="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/${PR}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["head"]["sha"])')"
if [ "$HEAD_BEFORE" = "$HEAD_AFTER" ]; then
  bad "nothing was pushed"
else
  ok "pushed ${HEAD_BEFORE:0:7} -> ${HEAD_AFTER:0:7}"
  GIT_SSL_NO_VERIFY=true git -C "$WORK/repo" fetch -q "$CLONE" "$BRANCH"
  git -C "$WORK/repo" diff "$HEAD_BEFORE" "$HEAD_AFTER" -- addons/ | sed 's/^/    /'
fi

say "6. what it said"
gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/issues/${PR}/comments?limit=50" \
  | python3 -c '
import json,sys
cs=[c for c in json.load(sys.stdin) if not c["body"].startswith("<!-- gitops-gate -->")]
print("\n".join("    "+l for l in cs[-1]["body"].splitlines())) if cs else print("    (no comment)")'
