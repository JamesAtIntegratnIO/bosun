#!/usr/bin/env bash
# Act five: the agent reaches the internet, and says where it went.
#
# 0.15.0 replaced the egress allow-list with an open door and a log. The
# allow-list was correct and it was a full-time job, every chart repository,
# every registry's blob CDN and every redirect target had to be named before the
# agent could read it, and three separate incidents in this project added a host
# after the fact. The symptom each time was a two-minute timeout and a brief
# that said it had no evidence, which is the quiet failure the whole component
# exists to end.
#
# What replaced it is accountability after the fact: every outbound request is
# logged, method, host and path, with the query string redacted because
# release-asset URLs are pre-signed and carry a JWT, and `triage.egressDeny`
# forbids a host by name or by `*.suffix`.
#
# The log half is visible in every other act. The deny half is not, because a
# working deployment never contacts a host it has forbidden. So this forbids one
# it is about to contact.
#
# It changes the running deployment and puts it back, including on failure. That
# is invasive, and it is the only way to demonstrate a rule whose whole purpose
# is to stop something that otherwise works.
#
#   usage: 85-demo-egress.sh [deny-pattern]     (default: *.docker.io)
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials
DENY="${1:-*.docker.io}"
BRANCH="egress/a-host-it-was-told-not-to-visit"

ORIGINAL="$(kc -n bosun get deploy bosun-bosun \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="EGRESS_DENY")].value}')"
# During a rollout there are two Running pods and `logs deploy/...` picks one
# of them, so confirming the restore from a log line is a coin flip, the
# first two versions of this printed the old pod's startup banner and the
# restore looked like it had not happened. The deployment's own spec is the
# authoritative answer and needs no pod at all.
deny_now() {
  kc -n bosun get deploy bosun-bosun \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="EGRESS_DENY")].value}'
}
restore() {
  say "putting the deny list back"
  kc -n bosun set env deploy/bosun-bosun "EGRESS_DENY=${ORIGINAL}" >/dev/null
  kc -n bosun rollout status deploy/bosun-bosun --timeout=180s >/dev/null
  local back; back="$(deny_now)"
  if [ "$back" = "$ORIGINAL" ]; then
    ok "EGRESS_DENY=${back:-<nothing forbidden>}"
  else
    bad "the deny list did not go back: wanted ${ORIGINAL:-<nothing>}, deployment says ${back:-<nothing>}"
  fi
}
trap restore EXIT

say "forbidding ${DENY}, which the upstream lookup is about to need"
step "was: ${ORIGINAL:-<nothing forbidden>}"
kc -n bosun set env deploy/bosun-bosun "EGRESS_DENY=${DENY}" >/dev/null
kc -n bosun rollout status deploy/bosun-bosun --timeout=180s >/dev/null
POD="$(agent_pod)"
[ -n "$POD" ] || bad "no running agent pod"
step "now: $(deny_now)"

say "1. a pull request the agent will escalate"
# The escalate path is one of the two that read upstream notes, and reading
# them starts by asking the registry which repository publishes the artifact.
# That is the request about to be refused.
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"; restore' EXIT
CLONE="https://${GITEA_OWNER}:${GITEA_TOKEN}@gitea.${IDP_HOST}:${IDP_PORT}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
GIT_SSL_NO_VERIFY=true git clone -q "$CLONE" "$WORK/repo"
git -C "$WORK/repo" config http.sslVerify false
git -C "$WORK/repo" config user.name "kargo"
git -C "$WORK/repo" config user.email "kargo@localtest.me"
git -C "$WORK/repo" checkout -q -B "$BRANCH"
# A namespace moving under a version bump: nothing a bump can cause, so the
# agent escalates rather than editing, and reaches for upstream notes on the
# way past.
sed -i.bak -E 's|^( *namespace: )podinfo-hub|\1podinfo-tenant|' "$WORK/repo/apps/podinfo-hub.yaml"
rm -f "$WORK/repo/apps/podinfo-hub.yaml.bak"
git -C "$WORK/repo" commit -qam "chore(podinfo): bump, and move the namespace"
git -C "$WORK/repo" push -q --force "$CLONE" "$BRANCH"

# The agent will not re-triage a pull request it has already escalated, and it
# tracks that with a label, which a force-push does not clear. A second run of
# this script against the same open pull request therefore proves nothing: the
# agent short-circuits before it reaches for upstream notes; nothing is
# refused, and the demo reports a failure that is its own reuse. Close any
# open one first so every run starts from a pull request with no history.
for old in $(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls?state=open" \
    | python3 -c "import json,sys;print(' '.join(str(p['number']) for p in json.load(sys.stdin) if p['head']['ref']=='$BRANCH'))"); do
  gitea_api PATCH "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/${old}" -d '{"state":"closed"}' >/dev/null
  step "closed the previous #${old}"
done

PR="$(gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls" \
  -d "$(BR="$BRANCH" python3 -c 'import json,os; print(json.dumps({"head":os.environ["BR"],"base":"main","title":"chore(podinfo): bump, and move the namespace","body":"Opened so the agent reaches for upstream notes and finds a host it was told not to visit."}))')" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("number",""))')"
[ -n "$PR" ] || bad "could not open a pull request"
ok "pull request #${PR}"

# Nothing seeds a verdict here. The namespace move is a real change to what
# gets deployed, so the agent's own sweep renders it and blocks it, which is
# what the triage below then has to answer for.
HEAD_SHA="$(head_sha "$PR")"
step "waiting for the sweep to render ${HEAD_SHA:0:8}"
wait_for "the gate blocked it" 300 status_is "$HEAD_SHA" failure

say "2. where it went, and where it was stopped"
BEFORE="$(kc -n bosun logs "$POD" | wc -l | tr -d ' ')"
BODY="$(PR="$PR" BR="$BRANCH" python3 -c '
import json, os
print(json.dumps({
  "project": "delivery", "stage": "egress", "promotion": "egress-demo",
  "artifact": "podinfo", "from": "6.7.0", "to": "6.14.1", "autoMerge": "never",
  "prNumber": int(os.environ["PR"]), "branch": os.environ["BR"],
  "files": ["apps/podinfo-hub.yaml"], "verifyApps": []}))')"
kc -n bosun exec -i "$POD" -- wget -q -O- --post-data "$BODY" \
  --header 'Content-Type: application/json' http://localhost:8080/v1/promotion-opened >/dev/null 2>&1 || true

for _ in $(seq 1 60); do
  kc -n bosun logs "$POD" | tail -n +$((BEFORE + 1)) \
    | grep -qE "PR ${PR}: (triage done|triage failed)" && break
  sleep 5
done
SLICE="$(kc -n bosun logs "$POD" | tail -n +$((BEFORE + 1)))"
printf '%s\n' "$SLICE" | grep -E "outbound|PR ${PR}:" | sed 's/^/    /'

say "3. the verdict"
if printf '%s' "$SLICE" | grep -q "outbound REFUSED"; then
  ok "refused, and the log names the rule that did it"
else
  bad "nothing was refused -- either the agent did not reach ${DENY}, or the deny list is not being applied"
fi
# A refusal must degrade, never derail: the agent still has to reach a verdict.
if printf '%s' "$SLICE" | grep -qE "PR ${PR}: (escalated:|no action needed:)"; then
  ok "and it still reached a verdict without what it could not read"
else
  bad "the refusal stopped the triage; a blocked host must shorten the brief, not end the run"
fi
