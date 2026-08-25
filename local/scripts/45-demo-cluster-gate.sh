#!/usr/bin/env bash
# Act one-and-a-half: the gate with no CI anywhere.
#
# Every other act runs the gate the old way -- gate-run.sh standing in for a
# CI system, posting a report comment the agent scrapes back. This one proves
# the shape ADR 0008 made the default: gate.mode=cluster, where the agent
# polls the open pull requests and renders them itself, against the live
# inventory, with no adapter, no checked-in snapshot and no paths filter.
#
# Three beats, each a property the CI shape could not have by construction:
#
#   1. A comment-only change goes green as "no change to what gets deployed"
#      -- an ANSWER from a render, where CI's paths filter would have guessed.
#      And it posts no report, because a report that says nothing is noise.
#   2. A destination move goes red, with the report comment posted by the
#      agent itself -- no CI ran, nothing was scraped.
#   3. A fix pushed to the red branch is re-gated because it is a new head
#      commit. The CI shape needed a specially-minted bot token for this;
#      the sweep needs the commit to exist.
#
# The kit installs gate.mode=ci because the incident-replay acts feed the
# agent recorded reports, which only that mode reads. This act flips the mode
# and puts it back, the same way the egress act treats the deny list.
#
#   usage: 45-demo-cluster-gate.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials

mode_now() {
  kc -n bosun get deploy bosun-bosun \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="GATE_MODE")].value}'
}

# Through HELM, not `kubectl set env`, and that is not ceremony.
#
# Cluster mode reads the ArgoCD cluster Secrets, and the chart grants that with
# a namespaced Role it creates ONLY in cluster mode. Setting the environment
# variable by hand moves the switch and leaves the permission behind: the agent
# comes up, asks for the inventory, is refused, and -- by design -- refuses to
# start rather than gate against a world it cannot see. A CrashLoopBackOff that
# says `secrets is forbidden` is the correct behaviour and a mystifying way to
# open a demo. Helm moves both halves together, which is also how an operator
# turns this on.
set_mode() { # <mode>
  helm upgrade bosun "$ROOT/../charts/bosun" \
    --kube-context "$CLUSTER_CONTEXT" --namespace bosun \
    --reuse-values --set gate.mode="$1" \
    --wait --timeout 5m >/dev/null
}

ORIGINAL="$(mode_now)"
restore() {
  say "putting the gate back where the other acts expect it"
  set_mode "${ORIGINAL:-ci}"
  ok "GATE_MODE=$(mode_now)"
}
trap restore EXIT

say "the agent becomes the gate"
step "was: GATE_MODE=${ORIGINAL:-<chart default>}"
set_mode cluster
POD="$(agent_pod)"
[ -n "$POD" ] || { bad "no agent pod"; exit 1; }
kc -n bosun logs "$POD" | grep -q "gate: in-cluster" \
  || { bad "the agent did not announce the in-cluster gate"; exit 1; }
ok "polling for open pull requests -- no CI anywhere in this act"

# One status, by context name. Gitea has no check-runs API, so this is the
# whole surface. It arrives newest first -- but Gitea stamps whole seconds, and
# the gate posts `pending` and then its verdict inside one of them, so the
# order within that tie is arbitrary and taking the first match is a coin flip.
# This script lost that flip on its first run: it watched a `pending` from
# 01:04:02 while the `success` beside it, same second, went unread for 180s.
# Newest wins, and a tie is broken on meaning -- a verdict cannot precede the
# pending that announced it. The same rule the Gitea client itself applies.
gate_status() { # sha -> "state description"
  gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/statuses/$1?limit=50" \
    | python3 -c '
import json,sys
best = None
for s in json.load(sys.stdin):
    if s.get("context") != "gate":
        continue
    at, state = s.get("created_at",""), s.get("status","")
    if best is None or at > best[0] or (at == best[0] and best[1] == "pending" and state != "pending"):
        best = (at, state, s.get("description",""))
if best:
    print(best[1], best[2])'
}
status_is() { # sha state -> bool
  gate_status "$1" | grep -q "^$2"
}
head_sha() { # pr -> sha
  gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/$1" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["head"]["sha"])'
}
open_pr() { # branch title -> pr number
  local pr
  pr="$(gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls" \
    -d "$(BR="$1" TITLE="$2" python3 -c 'import json,os; print(json.dumps({"head":os.environ["BR"],"base":"main","title":os.environ["TITLE"],"body":"Opened by the local proving ground: the in-cluster gate act."}))')" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("number",""))')"
  # A second run force-pushes the same branches, and Gitea refuses to open a
  # pull request that is already open. Reuse it -- the same fallback act two
  # uses, and without it this script works exactly once per cluster.
  [ -n "$pr" ] || pr="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls?state=open" \
    | BR="$1" python3 -c 'import json,os,sys; d=[p for p in json.load(sys.stdin) if p["head"]["ref"]==os.environ["BR"]]; print(d[0]["number"] if d else "")')"
  printf '%s' "$pr"
}

WORK="$(mktemp -d)"
cleanup_work() { rm -rf "$WORK"; restore; }
trap cleanup_work EXIT
CLONE="https://${GITEA_OWNER}:${GITEA_TOKEN}@gitea.${IDP_HOST}:${IDP_PORT}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
GIT_SSL_NO_VERIFY=true git clone -q "$CLONE" "$WORK/repo"
git -C "$WORK/repo" config http.sslVerify false
git -C "$WORK/repo" config user.name "a hurried human"
git -C "$WORK/repo" config user.email "human@localtest.me"

say "1. a change that deploys nothing gets a rendered answer, not a guessed one"
BENIGN="cluster-gate/a-comment-is-not-a-deployment"
git -C "$WORK/repo" checkout -q -B "$BENIGN" origin/main
printf '# reviewed by the proving ground\n' >> "$WORK/repo/apps/podinfo-hub.yaml"
git -C "$WORK/repo" commit -qam "docs(podinfo): a comment, and nothing else"
git -C "$WORK/repo" push -q --force "$CLONE" "$BENIGN"
PR1="$(open_pr "$BENIGN" "docs(podinfo): a comment, and nothing else")"
[ -n "$PR1" ] || { bad "could not open the benign pull request"; exit 1; }
SHA1="$(head_sha "$PR1")"
step "pull request #${PR1} at ${SHA1:0:8}"
wait_for "the sweep rendered it and answered success" 180 status_is "$SHA1" success
DESC="$(gate_status "$SHA1")"
echo "$DESC" | grep -q "no change to what gets deployed" \
  || { bad "the green should say WHY: got '${DESC}'"; exit 1; }
ok "the status says why: ${DESC#success }"
MARKED="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/issues/${PR1}/comments?limit=50" \
  | python3 -c 'import json,sys; print(sum(1 for c in json.load(sys.stdin) if "<!-- gitops-gate -->" in c["body"]))')"
[ "$MARKED" = "0" ] || { bad "a no-change render must not post a report; found ${MARKED}"; exit 1; }
ok "and no report comment -- nothing to read means nothing posted"

say "2. a destination move goes red, reported by the agent itself"
RED="cluster-gate/a-cluster-it-should-not-reach"
git -C "$WORK/repo" checkout -q -B "$RED" origin/main
sed -i.bak -E 's|^( *server: ).*|\1https://somewhere-else.invalid:6443|' "$WORK/repo/apps/podinfo-tenant.yaml"
rm -f "$WORK/repo/apps/podinfo-tenant.yaml.bak"
git -C "$WORK/repo" commit -qam "chore(podinfo): move tenant (in passing, unremarked)"
git -C "$WORK/repo" push -q --force "$CLONE" "$RED"
PR2="$(open_pr "$RED" "chore(podinfo): tidy the tenant app")"
[ -n "$PR2" ] || { bad "could not open the targeting pull request"; exit 1; }
SHA2="$(head_sha "$PR2")"
step "pull request #${PR2} at ${SHA2:0:8}"
wait_for "the sweep rendered it and blocked" 180 status_is "$SHA2" failure
REPORTS="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/issues/${PR2}/comments?limit=50" \
  | python3 -c 'import json,sys; print(sum(1 for c in json.load(sys.stdin) if "<!-- gitops-gate -->" in c["body"] and "gitops-gate:head" in c["body"]))')"
[ "$REPORTS" = "1" ] || { bad "expected exactly one head-stamped report from the agent; found ${REPORTS}"; exit 1; }
ok "one report comment, marker first, stamped with the head commit"

say "3. the fix is re-gated because it exists, not because a token was minted right"
git -C "$WORK/repo" checkout -q "$RED"
git -C "$WORK/repo" checkout -q origin/main -- apps/podinfo-tenant.yaml
git -C "$WORK/repo" commit -qam "fix(podinfo): the tenant stays where it was"
git -C "$WORK/repo" push -q --force "$CLONE" "$RED"
SHA3="$(head_sha "$PR2")"
[ "$SHA3" != "$SHA2" ] || { bad "the push did not move the head"; exit 1; }
step "new head ${SHA3:0:8} -- in CI this is where the wrong token waits forever"
wait_for "the new head commit got its own verdict: success" 180 status_is "$SHA3" success
ok "re-gated on the next sweep -- the retrigger trap does not exist here"

say "the in-cluster gate held all three properties"
