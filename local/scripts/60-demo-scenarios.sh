#!/usr/bin/env bash
# The recorded incidents, replayed against the LIVE agent, on real pull requests.
#
# These are not invented scenarios. Each one is something that actually
# happened to this platform -- MetalLB swapping its FRR sidecars for a
# DaemonSet, argo-cd 10.0.0 flipping NetworkPolicy creation on, NGF requiring a
# newer Gateway API, authentik refusing to skip a version -- and each is
# already written down once as an eval fixture. This reads those same fixtures
# so the thing the eval measures and the thing you watch cannot drift apart.
#
# HONEST ABOUT WHAT IS REPLAYED: the gate's REPORT is the recorded one from
# each incident, posted as the gate would post it, because reproducing
# fourteen upstream chart versions locally would prove nothing extra. The
# agent, the model, the pull requests, the reasoning and every commit it
# pushes are live.
#
#   usage: 40-scenarios.sh [case-name-substring]
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials
FILTER="${1:-}"
CASES_JSON="$(mktemp)"; trap 'rm -f "$CASES_JSON"' EXIT
(cd "$ROOT/.." && GOTOOLCHAIN=auto go run ./evals/export) > "$CASES_JSON"
TOTAL=$(python3 -c "import json;print(len(json.load(open('$CASES_JSON'))))")
# The suite measures two prompts. This replays the RED-GATE path -- it seeds a
# failing gate and a blocking report -- so a case written for the green-gate
# explanation would be replayed under the wrong conditions and score nothing
# meaningful. Those are skipped by name below rather than silently mis-run.
REPLAYABLE=$(python3 -c "import json;print(sum(1 for c in json.load(open('$CASES_JSON')) if not c.get('Path')))")
say "$REPLAYABLE of $TOTAL recorded incidents replay against a red gate; agent is live"

AGENT_POD="$(kc -n bosun get pod -l app.kubernetes.io/name=bosun -o name | head -1)"
[ -n "$AGENT_POD" ] || bad "no agent pod"
MODEL="$(kc -n bosun get deploy bosun-bosun \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="LLM_MODEL")].value}')"
step "model: ${MODEL}"

CLONE="https://${GITEA_OWNER}:${GITEA_TOKEN}@gitea.${IDP_HOST}:${IDP_PORT}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
RESULTS="$(mktemp)"

for i in $(seq 0 $((TOTAL - 1))); do
  NAME=$(python3 -c "import json;print(json.load(open('$CASES_JSON'))[$i]['Name'])")
  [ -n "$FILTER" ] && case "$NAME" in *"$FILTER"*) ;; *) continue ;; esac

  # Which prompt this case measures. Empty is triage, which is what this
  # replays; anything else needs a gate in a state this script does not seed.
  CASE_PATH=$(python3 -c "import json;print(json.load(open('$CASES_JSON'))[$i].get('Path') or '')")
  if [ -n "$CASE_PATH" ]; then
    step "skipping ${NAME}: measures the ${CASE_PATH} prompt, which needs a green gate"
    continue
  fi

  WANT=$(python3 -c "import json;print(json.load(open('$CASES_JSON'))[$i]['WantClass'])")
  SUBJECT=$(python3 -c "import json;print(json.load(open('$CASES_JSON'))[$i]['Subject'])")
  say "${NAME}  (expected: ${WANT})"
  step "$SUBJECT"

  # --- a branch carrying this incident's repository fixture ---
  WORK="$(mktemp -d)"
  GIT_SSL_NO_VERIFY=true git clone -q "$CLONE" "$WORK/repo"
  git -C "$WORK/repo" config http.sslVerify false
  git -C "$WORK/repo" config user.name "kargo"
  git -C "$WORK/repo" config user.email "kargo@localtest.me"
  BRANCH="scenario/${NAME}"
  git -C "$WORK/repo" checkout -q -B "$BRANCH"
  python3 - "$CASES_JSON" "$i" "$WORK/repo" <<'PY'
import json, os, sys
case = json.load(open(sys.argv[1]))[int(sys.argv[2])]
root = sys.argv[3]
for path, content in (case.get("Files") or {}).items():
    full = os.path.join(root, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    open(full, "w").write(content)
PY
  git -C "$WORK/repo" add -A
  git -C "$WORK/repo" commit -q -m "$SUBJECT"
  git -C "$WORK/repo" push -q --force "$CLONE" "$BRANCH"

  # A body, not just a title. Gitea renders an empty description as a bare
  # "No description provided." at the top of the conversation, which reads
  # like a broken first comment rather than an absent one.
  BODY_TEXT="Automated version bump.
Replayed from the \`${NAME}\` incident fixture; correct handling is **${WANT}**.
The gate report below is the one recorded from the original incident.
Everything after it -- the agent, the model, any commit -- is live.
See \`local/\` in the Bosun repository."
  PR=$(gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls" \
        -d "$(BR="$BRANCH" TI="$SUBJECT" BO="$BODY_TEXT" python3 -c 'import json,os; print(json.dumps({"head":os.environ["BR"],"base":"main","title":os.environ["TI"],"body":os.environ["BO"]}))')" \
      | python3 -c 'import json,sys; print(json.load(sys.stdin).get("number",""))')
  if [ -z "$PR" ]; then
    PR=$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls?state=open" \
        | python3 -c "import json,sys;d=[p for p in json.load(sys.stdin) if p['head']['ref']=='$BRANCH'];print(d[0]['number'] if d else '')")
  fi
  [ -n "$PR" ] || { bad "could not open a pull request for ${NAME}"; rm -rf "$WORK"; continue; }
  step "pull request #${PR}"

  HEAD_SHA=$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/${PR}" \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["head"]["sha"])')

  # --- the gate's recorded verdict, published the way the gate publishes it ---
  python3 - "$CASES_JSON" "$i" > "$WORK/comment.json" <<'PY'
import json, sys
case = json.load(open(sys.argv[1]))[int(sys.argv[2])]
print(json.dumps({"body": "<!-- gitops-gate -->\n" + case["GateReport"]}))
PY
  gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/issues/${PR}/comments" \
    --data-binary @"$WORK/comment.json" >/dev/null
  gitea_api POST "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/statuses/${HEAD_SHA}" \
    -d '{"context":"gate","state":"failure","description":"blocking change"}' >/dev/null
  step "gate report posted, status gate=failure"

  # --- the live agent ---
  BEFORE=$(kc -n bosun logs "$AGENT_POD" 2>/dev/null | wc -l | tr -d ' ')
  BODY=$(python3 - "$CASES_JSON" "$i" "$PR" "$BRANCH" <<'PY'
import json, sys
case = json.load(open(sys.argv[1]))[int(sys.argv[2])]
print(json.dumps({
  "project": "bosun", "stage": "scenario", "promotion": case["Name"],
  "artifact": case["Subject"], "from": "", "to": "", "autoMerge": "never",
  "prNumber": int(sys.argv[3]), "branch": sys.argv[4],
  # What the PROMOTION rewrote, not everything the fixture writes into the
  # repository. Kargo reports only the files its `updates:` block touched, and
  # the agent scopes its edits to exactly that -- sending the full fixture
  # here would hand it an authority the live pipeline never grants.
  "files": sorted(case.get("Changed") or (case.get("Files") or {}).keys()),
  "verifyApps": [],
}))
PY
)
  kc -n bosun exec -i "$AGENT_POD" -- \
    wget -q -O- --post-data "$BODY" --header 'Content-Type: application/json' \
    http://localhost:8080/v1/promotion-opened >/dev/null 2>&1 || true

  for _ in $(seq 1 60); do
    kc -n bosun logs "$AGENT_POD" 2>/dev/null | tail -n +$((BEFORE + 1)) \
      | grep -qE "PR ${PR}: triage done" && break
    sleep 5
  done

  # --- what it did ---
  AFTER_SHA=$(gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/pulls/${PR}" \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["head"]["sha"])')
  GOT="escalate/no_action"
  if [ "$HEAD_SHA" != "$AFTER_SHA" ]; then
    GOT="mechanical"
    ok "pushed a fix: ${HEAD_SHA:0:7} -> ${AFTER_SHA:0:7}"
    GIT_SSL_NO_VERIFY=true git -C "$WORK/repo" fetch -q "$CLONE" "$BRANCH"
    git -C "$WORK/repo" diff --unified=0 "$HEAD_SHA" "$AFTER_SHA" \
      | grep -E '^[-+][^-+]' | sed 's/^/      /'
  else
    step "pushed nothing"
  fi
  gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}/issues/${PR}/comments?limit=50" \
    | python3 -c '
import json,sys
cs=[c for c in json.load(sys.stdin) if not c["body"].startswith("<!-- gitops-gate -->")]
if cs:
    print("      " + cs[-1]["body"].strip().splitlines()[0][:150])'

  printf '%s\t%s\t%s\t%s\n' "$NAME" "$WANT" "$GOT" "$PR" >> "$RESULTS"
  rm -rf "$WORK"
done

say "summary"
printf '  %-38s %-11s %-11s %s\n' CASE EXPECTED OBSERVED PR
while IFS=$'\t' read -r n w g p; do
  mark=" "; [ "$w" = "$g" ] && mark="+"
  [ "$w" != mechanical ] && [ "$g" != mechanical ] && mark="+"
  printf '%s %-38s %-11s %-11s #%s\n' "$mark" "$n" "$w" "$g" "$p"
done < "$RESULTS"
echo
echo "  + means the agent's ACTION matched the case's class."
echo "  Explain-path cases are not replayed here; they need a green gate."
echo "  This shows whether it edited, not whether the edit was right --"
echo "  the eval suite checks the exact scalars. Run: go test ./evals/..."
rm -f "$RESULTS"
