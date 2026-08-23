#!/usr/bin/env bash
# Puts the sample repository into Gitea. That is the whole of the seed: the
# scenarios clone this repository, write an incident's fixture files onto a
# branch, and open a pull request against it.
#
# Idempotent: re-running force-pushes the repository contents again rather
# than failing on "already exists".
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

load_credentials
[ -n "${GITEA_TOKEN:-}" ] || bad "no Gitea token -- is the cluster up?"

say "gitea repository ${GITEA_OWNER}/${SAMPLE_REPO_NAME}"
if gitea_api GET "/repos/${GITEA_OWNER}/${SAMPLE_REPO_NAME}" -o /dev/null -w '%{http_code}' | grep -q 200; then
  step "already exists"
else
  gitea_api POST "/user/repos" \
    -d "{\"name\":\"${SAMPLE_REPO_NAME}\",\"private\":false,\"auto_init\":false}" >/dev/null
  step "created"
fi

say "pushing the sample repository"
# A fresh clone every time: the working copy is disposable, and reusing one
# across runs is how you end up demonstrating yesterday's state.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cp -R "$ROOT/sample-repo/." "$WORK/"
PUSH_URL="https://${GITEA_OWNER}:${GITEA_TOKEN}@gitea.${IDP_HOST}:${IDP_PORT}/${GITEA_OWNER}/${SAMPLE_REPO_NAME}.git"

git -C "$WORK" init -q -b main
git -C "$WORK" config http.sslVerify false
git -C "$WORK" config user.name "local proving ground"
git -C "$WORK" config user.email "local@localtest.me"
git -C "$WORK" add -A
git -C "$WORK" commit -q -m "the sample repository under test"
git -C "$WORK" push -q --force "$PUSH_URL" main
ok "pushed $(git -C "$WORK" rev-parse --short main)"


say "seeded"
printf '  gitea    %s/%s/%s\n' "$GITEA_URL" "$GITEA_OWNER" "$SAMPLE_REPO_NAME"
