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
REPLAYABLE=$(python3 -c "import json;print(sum(1 for c in json.load(open('$CASES_JSON')) if c.get('Path') != 'restructure'))")
say "$REPLAYABLE of $TOTAL recorded incidents replay as pull requests; agent is live"

AGENT_POD="$(agent_pod)"
[ -n "$AGENT_POD" ] || bad "no agent pod"
MODEL="$(kc -n bosun get deploy bosun-bosun \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="LLM_MODEL")].value}')"
step "model: ${MODEL}"
# The green path phrases its verdict with the check's name in it, so scoring
# needs the name this deployment was given rather than the chart default.
CHECK_NAME="$(kc -n bosun get deploy bosun-bosun \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="GATE_CHECK_NAME")].value}')"
: "${CHECK_NAME:=addons-gate}"

CLONE="https://${GITEA_OWNER}:${GITEA_TOKEN}@gitea.${IDP_HOST}:${IDP_PORT}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"
RESULTS="$(mktemp)"

for i in $(seq 0 $((TOTAL - 1))); do
  NAME=$(python3 -c "import json;print(json.load(open('$CASES_JSON'))[$i]['Name'])")
  [ -n "$FILTER" ] && case "$NAME" in *"$FILTER"*) ;; *) continue ;; esac

  # Which prompt this case measures, and therefore which gate it needs.
  #
  #   ""          triage -- a RED gate and a blocking report
  #   explain     a GREEN gate whose render still changed. Every case here used
  #               to be skipped as "needs a green gate", which was true and
  #               fixable: the gate's verdict is seeded by this script, so
  #               seeding a green one is a two-character difference. It matters
  #               more than the count suggests -- the explain path is the only
  #               one that reads upstream release notes and commits, so nine
  #               skipped cases meant that whole authority was never exercised
  #               against a live model here.
  #   restructure not a pull request at all. Those cases are a document, an old
  #               schema and a new one, and the live equivalent is
  #               70-demo-structural.sh, which performs the whole migration
  #               against a real cluster.
  CASE_PATH=$(python3 -c "import json;print(json.load(open('$CASES_JSON'))[$i].get('Path') or '')")
  if [ "$CASE_PATH" = restructure ]; then
    step "skipping ${NAME}: a document and two schemas, not a pull request -- run make demo-structural"
    continue
  fi
  GATE_STATE=failure; GATE_DESC="blocking change"
  if [ "$CASE_PATH" = explain ]; then
    GATE_STATE=success; GATE_DESC="no blocking change"
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
    -d "{\"context\":\"gate\",\"state\":\"${GATE_STATE}\",\"description\":\"${GATE_DESC}\"}" >/dev/null
  step "gate report posted, status gate=${GATE_STATE}"

  # --- the live agent ---
  BEFORE=$(kc -n bosun logs "$AGENT_POD" | wc -l | tr -d ' ')
  BODY=$(python3 - "$CASES_JSON" "$i" "$PR" "$BRANCH" <<'PY'
import json, re, sys
case = json.load(open(sys.argv[1]))[int(sys.argv[2])]
# The recorded subject reads "bump <chart> chart <from> -> <to>", and the
# agent needs those three as fields: a promotion whose from/to are empty
# cannot look up what changed between them, so the explain path would run
# with a render and no way to ask why. Parsed rather than added to the
# fixtures, because the fixtures are the eval suite's and the eval suite
# supplies its own upstream text.
m = re.match(r"bump (\S+) chart (\S+) -> (\S+)", case["Subject"])
chart, frm, to = (m.group(1), m.group(2), m.group(3)) if m else (case["Subject"], "", "")
print(json.dumps({
  "project": "bosun", "stage": "scenario", "promotion": case["Name"],
  "artifact": chart, "from": frm, "to": to, "autoMerge": "never",
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
  # Which of the three verdicts, taken from the agent's OWN classification.
  #
  # The first attempt at this counted the agent's comments -- escalate speaks,
  # no_action stays silent. It does not: `no action needed` posts a comment
  # too, headed "No change proposed.", which is the right behaviour and made
  # the discriminator wrong. Watching the head SHA alone was worse, collapsing
  # escalate and no_action into one class so the case whose entire point is
  # that the agent stays QUIET passed by matching a bucket that contained every
  # escalation.
  #
  # The log line is the agent saying what it decided, and there are two shapes
  # of it because the red and green paths phrase their verdicts differently:
  #
  #   escalated: ...                    red path, needs a human
  #   <check> is green, but flagged: ...  green path, same meaning
  #   no action needed: ...             red path, deliberately quiet
  #   <check> is green: ...             green path, same meaning
  #
  # Anything else -- "could not reach the model", "could not read the branch"
  # -- is an ERROR, and lands as `unknown` so it fails the run rather than
  # passing as a quiet verdict it never reached.
  LOG_SLICE=$(kc -n bosun logs "$AGENT_POD" | tail -n +$((BEFORE + 1)))
  GOT=unknown
  if printf '%s' "$LOG_SLICE" | grep -qE "PR ${PR}: (escalated:|${CHECK_NAME} is green, but flagged:)"; then
    GOT=escalate
  elif printf '%s' "$LOG_SLICE" | grep -qE "PR ${PR}: (no action needed:|${CHECK_NAME} is green:)"; then
    GOT=no_action
  fi
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
    # First line with something in it. The explain path leads with an HTML
    # marker and the triage path with the brand, and neither is the sentence
    # a reader of this run wants.
    lines = [l.strip() for l in cs[-1]["body"].splitlines()]
    body = [l for l in lines if l and not l.startswith("<!--") and not l.startswith("\u2693")]
    print("      " + (body[0][:150] if body else "(no text)"))'

  printf '%s\t%s\t%s\t%s\n' "$NAME" "$WANT" "$GOT" "$PR" >> "$RESULTS"
  rm -rf "$WORK"
done

say "summary"
printf '  %-38s %-11s %-11s %s\n' CASE EXPECTED OBSERVED PR
FAILED=0
while IFS=$'\t' read -r n w g p; do
  mark="+"; [ "$w" = "$g" ] || { mark=" "; FAILED=$((FAILED + 1)); }
  printf '%s %-38s %-11s %-11s #%s\n' "$mark" "$n" "$w" "$g" "$p"
done < "$RESULTS"
echo
echo "  + means the agent's ACTION matched the case's class exactly."
echo "  mechanical = it pushed a commit; escalate = it asked for a human;"
echo "  no_action  = it deliberately had nothing to say, which for some cases"
echo "               is the right answer; unknown = it never reached a verdict."
echo "  Restructure cases are a document and two schemas rather than a pull"
echo "  request -- run make demo-structural for the live version of that path."
echo "  This shows whether it edited, not whether the edit was right --"
echo "  the eval suite checks the exact scalars. Run: go test ./evals/..."
[ "$FAILED" -eq 0 ] || bad "${FAILED} case(s) did not match"
rm -f "$RESULTS"
