#!/usr/bin/env bash
# Act three: a bump the deterministic repair CANNOT finish on its own.
#
# The migration in act two is arithmetic: a CustomResourceDefinition stops
# serving a version, the gate names the survivor, and every declaring manifest
# has one line rewritten. No model is involved and none is needed.
#
# This is the case where that is not enough. cert-manager v1.6 served
# `v1alpha2`, `v1alpha3`, `v1beta1` and `v1`; v1.7 removed all but `v1`. The
# field rename landed back in v0.16 and a conversion webhook kept the old
# versions working -- so a repository still declaring `cert-manager.io/v1alpha2`
# had manifests that applied cleanly right up until the bump that deleted the
# version, and then did not.
#
# Swap the apiVersion line alone and the document still parses, still applies,
# and has six fields pruned by the apiserver on the way in. The render is fine.
# The gate is green. The certificate quietly loses its key algorithm, its size,
# its encoding, its email SANs, its URI SANs and its subject organization.
#
#   keyAlgorithm/keySize/keyEncoding -> privateKey.{algorithm,size,encoding}
#   emailSANs -> emailAddresses,  uriSANs -> uris
#   organization -> subject.organizations
#
# Nobody can enumerate that in advance, so the model is shown BOTH schemas --
# the old one from the CustomResourceDefinition installed right now, the new one
# by rendering the chart at the target version -- and asked to translate. What
# makes it safe is not the prompt: every proposal is checked for identity,
# schema-validity and value provenance before a byte is written.
#
#   usage: 70-demo-structural.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials
BRANCH="bump/cert-manager-1.6.0"
OLD_CHART="v1.5.5"
NEW_CHART="v1.6.0"

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
CLONE="https://${GITEA_OWNER}:${GITEA_TOKEN}@gitea.${IDP_HOST}:${IDP_PORT}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
GIT_SSL_NO_VERIFY=true git clone -q "$CLONE" "$WORK/repo"
git -C "$WORK/repo" config http.sslVerify false
git -C "$WORK/repo" config user.name "a hurried human"
git -C "$WORK/repo" config user.email "human@localtest.me"

say "1. a repository that still declares cert-manager.io/v1alpha2"
git -C "$WORK/repo" checkout -q main
mkdir -p "$WORK/repo/apps" "$WORK/repo/addons"
cat > "$WORK/repo/apps/cert-manager.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cert-manager
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: cert-manager
  source:
    repoURL: https://charts.jetstack.io
    chart: cert-manager
    targetRevision: ${OLD_CHART}
    helm:
      values: |
        installCRDs: true
YAML
# The manifest the bump breaks. Written the way a 2021 repository wrote them,
# and untouched since -- which is the whole point: it has been correct for
# years and stops being correct the day the version is removed.
cat > "$WORK/repo/addons/platform-tls.yaml" <<'YAML'
apiVersion: cert-manager.io/v1alpha2
kind: Certificate
metadata:
  name: platform-tls
  namespace: gateway
spec:
  secretName: platform-tls
  duration: 2160h
  renewBefore: 360h
  commonName: platform.localtest.me
  dnsNames:
    - platform.localtest.me
  emailSANs:
    - platform@localtest.me
  uriSANs:
    - spiffe://localtest.me/platform
  organization:
    - Example Platform Team
  keyAlgorithm: ecdsa
  keySize: 384
  keyEncoding: pkcs8
  issuerRef:
    name: internal-ca
    kind: ClusterIssuer
    group: cert-manager.io
YAML
git -C "$WORK/repo" add -A
git -C "$WORK/repo" commit -qm "chore(cert-manager): pin ${OLD_CHART} and a Certificate that predates the rename" 2>/dev/null || true
git -C "$WORK/repo" push -q "$CLONE" main
ok "main declares cert-manager ${OLD_CHART} and one cert-manager.io/v1alpha2 Certificate"

say "2. the bump that deletes the version"
git -C "$WORK/repo" checkout -q -B "$BRANCH"
sed -i.bak -E "s|^( *targetRevision: ).*|\1${NEW_CHART}|" "$WORK/repo/apps/cert-manager.yaml"
rm -f "$WORK/repo/apps/cert-manager.yaml.bak"
git -C "$WORK/repo" diff | grep -E '^[-+] ' | sed 's/^/    /'
git -C "$WORK/repo" commit -qam "chore(cert-manager): bump to ${NEW_CHART}"
git -C "$WORK/repo" push -q --force "$CLONE" "$BRANCH"

PR="$(gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls" \
  -d "$(BR="$BRANCH" V="$NEW_CHART" python3 -c 'import json,os; print(json.dumps({"head":os.environ["BR"],"base":"main","title":"chore(cert-manager): bump to "+os.environ["V"],"body":"A one-line version bump. cert-manager 1.7 removes the v1alpha2, v1alpha3 and v1beta1 API versions."}))')" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("number",""))')"
[ -n "$PR" ] || PR="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls?state=open" \
  | python3 -c 'import json,sys; d=[p for p in json.load(sys.stdin) if p["head"]["ref"]=="'"$BRANCH"'"]; print(d[0]["number"] if d else "")')"
ok "pull request #${PR}"
printf '    %s/%s/%s/pulls/%s\n' "$GITEA_URL" "$GITEA_OWNER" "$SAMPLE_REPO_NAME" "$PR"

say "3. the gate renders both versions and refuses"
set +e
bash "$ROOT/scripts/gate-run.sh" "$PR"
GATE_EXIT=$?
set -e
[ "$GATE_EXIT" -eq 0 ] && { bad "the gate passed a change that deletes an API version in use"; exit 1; }
ok "gate exit ${GATE_EXIT}"
grep -E 'no longer serves|still declare' "/tmp/gate-report-${PR}.md" | sed 's/^/    /' | head -8

say "4. the agent repairs, and finds the swap is not enough"
POD="$(kc -n bosun get pod -l app.kubernetes.io/name=bosun -o name | head -1)"
BEFORE="$(kc -n bosun logs "$POD" 2>/dev/null | wc -l | tr -d ' ')"
HEAD_BEFORE="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/${PR}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["head"]["sha"])')"

BODY="$(PR="$PR" BR="$BRANCH" F="$OLD_CHART" T="$NEW_CHART" python3 -c '
import json, os
print(json.dumps({
  "project": "delivery", "stage": "cert-manager", "promotion": "structural-demo",
  "artifact": "https://charts.jetstack.io cert-manager",
  "from": os.environ["F"].lstrip("v"), "to": os.environ["T"].lstrip("v"),
  "autoMerge": "never", "prNumber": int(os.environ["PR"]), "branch": os.environ["BR"],
  "files": ["apps/cert-manager.yaml"], "verifyApps": []}))')"
kc -n bosun exec -i "$POD" -- wget -q -O- --post-data "$BODY" \
  --header 'Content-Type: application/json' http://localhost:8080/v1/promotion-opened >/dev/null 2>&1 || true

for _ in $(seq 1 90); do
  kc -n bosun logs "$POD" 2>/dev/null | tail -n +$((BEFORE + 1)) \
    | grep -qE "PR ${PR}: triage (done|failed)" && break
  sleep 5
done
kc -n bosun logs "$POD" 2>/dev/null | tail -n +$((BEFORE + 1)) \
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
