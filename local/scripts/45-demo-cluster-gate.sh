#!/usr/bin/env bash
# Act one-and-a-half: the gate, with no CI anywhere.
#
# Three beats, each a property a CI workflow could not have by construction:
#
#   1. A comment-only change goes green as "no change to what gets deployed",
#      an answer from a render, where a paths filter would have guessed.
#      And it posts no report, because a report that says nothing is noise.
#   2. A destination move goes red, with the report comment posted by the
#      agent itself: no CI ran, nothing was scraped.
#   3. A fix pushed to the red branch is re-gated because it is a new head
#      commit. A CI workflow needed a specially-minted bot token for this;
#      the sweep needs the commit to exist.
#
#   usage: 45-demo-cluster-gate.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials

# Every pod carrying the label, not whichever one `head -1` names.
#
# The agent waits up to a minute for in-flight triage before it exits, so an
# outgoing pod is still Running while the incoming one starts, and asking a
# single pod is a coin flip on which one answers. lib.sh documents the same
# hazard for `agent_pod`; the whole set of pods is the answer that does not
# race.
announced_the_gate() {
  kc -n bosun logs -l app.kubernetes.io/name=bosun --tail=200 2>/dev/null \
    | grep -q "gate: polling for open pull requests"
}

say "the agent is the gate"
wait_for "polling for open pull requests -- no CI anywhere in this act" 120 announced_the_gate

open_pr() { # branch title -> pr number
  local pr
  pr="$(gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls" \
    -d "$(BR="$1" TITLE="$2" python3 -c 'import json,os; print(json.dumps({"head":os.environ["BR"],"base":"main","title":os.environ["TITLE"],"body":"Opened by the local proving ground: the in-cluster gate act."}))')" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("number",""))')"
  # A second run force-pushes the same branches, and Gitea refuses to open a
  # pull request that is already open. Reuse it, the same fallback act two
  # uses, and without it this script works exactly once per cluster.
  [ -n "$pr" ] || pr="$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls?state=open" \
    | BR="$1" python3 -c 'import json,os,sys; d=[p for p in json.load(sys.stdin) if p["head"]["ref"]==os.environ["BR"]]; print(d[0]["number"] if d else "")')"
  printf '%s' "$pr"
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
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

say "the gate held all three properties"
