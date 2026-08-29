#!/usr/bin/env bash
# Proves this repository runs on somebody else's cluster, in somebody else's
# CI, against somebody else's model.
#
# This replaced hack/extraction-test.sh on 2026-08-23. That script existed to
# prove `delivery/` could be lifted out of the platform repository that hosted
# it, and enforced a one-way link rule to keep the lift cheap. The lift has
# happened: this repository is the package, there is no host to reach into, and
# a rule about escaping a directory that no longer exists fails on its own
# fixtures. What survives here are the checks that were never about extraction.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v helm >/dev/null 2>&1 || { echo "needs helm on PATH" >&2; exit 2; }

fail=0
ok()  { printf '  \033[32mok\033[0m    %s\n' "$*"; }
bad() { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; fail=1; }

# ---------------------------------------------------------------------------
# No environment assumptions.
# ---------------------------------------------------------------------------
echo "==> nothing assumes one particular environment"

# Literals from the environment this was built in. Every one of these must be
# a value, or the first adopter inherits someone else's cluster.
LEAKS='the-cluster|vcluster-media|integratn\.tech|10\.0\.4\.|onepassword-store'
# The excluded directories are dependencies and build output, not shipped code:
# site/node_modules alone is ~40MB of other people's JSON, and a syntax theme in
# there matching one of these patterns is not this project inheriting anyone's
# cluster. Scanning it also turned a one-second check into a minute of grep.
leaks() {
  grep -rniE "$LEAKS" --include='*.yaml' --include='*.yml' --include='*.go' \
    --include='*.json' --include='Dockerfile' \
    --exclude-dir=node_modules --exclude-dir=dist --exclude-dir=.astro \
    --exclude-dir=.git . 2>/dev/null \
    | grep -v '^\./hack/portability-test\.sh'
}
if [ -n "$(leaks)" ]; then
  leaks | sed 's/^/        /' | head -20
  bad "host-environment literal in shipped code"
else
  ok "no host-environment literals"
fi

# The owner's name baked into a chart is a leak: an adopter installing these
# charts must never inherit our registry or repository as a default.
#
# Scoped to charts/ on purpose. Go import paths, workflow image names and
# Chart.yaml `home:`/`sources:` are this project's own identity, not an
# assumption about anyone's environment; flagging those made the old check
# fail on facts rather than on faults.
owner_in_charts() {
  grep -rniE 'jamesatintegratnio' charts/ 2>/dev/null \
    | grep -vE '/Chart\.yaml:' \
    | grep -vE '/(README|CHANGELOG)\.md:'
}
if [ -n "$(owner_in_charts)" ]; then
  owner_in_charts | sed 's/^/        /' | head -20
  bad "this project's owner appears in a chart's values or templates"
else
  ok "no owner-specific defaults in charts"
fi

# ---------------------------------------------------------------------------
# Everything renders and every link resolves.
# ---------------------------------------------------------------------------
echo "==> everything renders"
shopt -s nullglob
charts=(charts/*/Chart.yaml)
for c in "${charts[@]}"; do
  d="$(dirname "$c")"
  targs=()
  for v in "$d"/ci/*-values.yaml; do [ -f "$v" ] && targs+=(-f "$v"); done
  if helm template test "${targs[@]}" "$d" >/dev/null 2>&1; then
    ok "helm template $d"
  else
    helm template test "${targs[@]}" "$d" 2>&1 | sed 's/^/        /' | head -10
    bad "helm template $d"
  fi
done

# ---------------------------------------------------------------------------
# The ArgoCD egress rule names a pod port.
#
# A NetworkPolicy matches the destination port of the packet, and a ClusterIP
# is DNAT'd to the backend pod's port before policy is evaluated, so this rule
# has to name argocd-server's container port, not the Service port that
# appears in `gate.argocd.baseURL`. Getting it wrong renders clean, lints
# clean, passes the schema and then drops every packet, which is why the check
# is here rather than left to a reviewer.
#
# What this asserts: the emitted port is `gate.argocd.podPort` and does not
# move with the URL. Deriving it from `baseURL`, the obvious-looking
# simplification, fails here.
# ---------------------------------------------------------------------------
echo "==> the ArgoCD egress rule names a pod port, not the URL's port"
argocd_egress_port() { # <baseURL>
  helm template t charts/bosun -f charts/bosun/ci/lint-values.yaml \
    --show-only templates/networkpolicy.yaml \
    --set gate.argocd.existingSecret=placeholder \
    --set gate.argocd.baseURL="$1" 2>/dev/null \
    | awk '/metadata.name: argocd$/{f=1} f&&/^ *port: /{print $2; exit}'
}
declared="$(sed -n 's/^ *podPort: \([0-9]*\).*/\1/p' charts/bosun/values.yaml)"
http_port="$(argocd_egress_port http://argocd-server.argocd.svc)"
https_port="$(argocd_egress_port https://argocd-server.argocd.svc)"
if [ -z "$declared" ]; then
  bad "charts/bosun/values.yaml declares no gate.argocd.podPort"
elif [ "$http_port" != "$declared" ] || [ "$https_port" != "$declared" ]; then
  bad "the ArgoCD egress rule opened ${http_port:-<none>} (http) and ${https_port:-<none>} (https), not podPort ${declared}"
elif [ "$declared" = "80" ] || [ "$declared" = "443" ]; then
  # The default itself, separately. The comparison above stays true if someone
  # sets the default back to a Service port, because it compares the render to
  # the default rather than to reality. 80 and 443 are the two numbers that
  # cannot be a container port here: they are what the argocd-server Service
  # publishes, and the packet has been DNAT'd past them.
  bad "gate.argocd.podPort defaults to ${declared}, which is a Service port -- the rule needs the pod's"
else
  ok "gate.argocd.podPort ${declared} is the egress port for both http and https baseURLs"
fi

if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then ok "go build ./..."; else
    go build ./... 2>&1 | sed 's/^/        /' | head -10; bad "go build ./..."
  fi
else
  echo "  skip  go build (no toolchain on PATH)"
fi

if python3 hack/check_links.py . >/dev/null 2>&1; then
  ok "every relative markdown link resolves"
else
  python3 hack/check_links.py . 2>&1 | sed 's/^/        /' | head -20
  bad "a relative markdown link does not resolve"
fi

# ---------------------------------------------------------------------------
# The dev shell renders with the same tools the images do.
#
# The gate's verdict is the output of `helm template`, so the helm that
# produces it is part of the answer. A contributor whose shell renders with a
# different helm than the image gets a verdict that is locally true and
# globally wrong, and nothing about it looks like a version problem. The
# Dockerfile already pins helm and kubeconform and says why; this asserts the
# flake and CI pin the same strings, because three copies of a version number
# is exactly the shape that drifts.
#
# CI is the copy that was going unchecked. `ci.yaml` installs both tools to run
# the chart lint and the seam tests, its comment said this script already
# checked those pins against the flake, and it did not: it read the Dockerfile
# and stopped. A CI that lints with a helm no image ships is the
# same locally-true, globally-wrong verdict from the other direction, and the
# comment asserting otherwise is how it would have stayed unnoticed.
# ---------------------------------------------------------------------------
echo "==> the dev shell, the image and CI agree on what renders"
if [ -f flake.nix ]; then
  # CI spells the versions two ways, `version: v3.19.0` for the setup action
  # and an `env:` entry for the download, so the third argument names which key
  # to read and the match is on the value rather than on a shape either one
  # owns.
  check_pin() { # <flake attribute> <Dockerfile ARG> <ci.yaml key>
    local attr="$1" arg="$2" ci="$3" fv dv mismatch=0
    fv="$(sed -n "s/^ *${attr} = \"\([^\"]*\)\";.*/\1/p" flake.nix | head -1)"
    if [ -z "$fv" ]; then
      bad "flake.nix does not pin ${attr}"
      return 0
    fi
    dv="$(sed -n "s/^ARG ${arg}=v\{0,1\}\(.*\)/\1/p" Dockerfile | head -1)"
    if [ "$dv" != "$fv" ]; then
      bad "Dockerfile builds with ${arg}=${dv:-<unset>}, the dev shell with ${attr}=${fv}"
      mismatch=1
    fi
    if [ -f .github/workflows/ci.yaml ]; then
      local cv
      cv="$(sed -n "s/^ *${ci}: *v\{0,1\}\(.*\)/\1/p" .github/workflows/ci.yaml | head -1)"
      if [ "$cv" != "$fv" ]; then
        bad ".github/workflows/ci.yaml runs ${ci}=${cv:-<unset>}, the dev shell with ${attr}=${fv}"
        mismatch=1
      fi
    fi
    if [ "$mismatch" -eq 0 ]; then ok "${attr} ${fv} matches the image and CI"; fi
    return 0
  }
  check_pin helmVersion HELM_VERSION version
  check_pin kubeconformVersion KUBECONFORM_VERSION KUBECONFORM_VERSION
else
  echo "  skip  no flake.nix"
fi

# ---------------------------------------------------------------------------
# Every unit documents itself.
# ---------------------------------------------------------------------------
echo "==> every unit documents itself"
for c in "${charts[@]}"; do
  d="$(dirname "$c")"
  for f in README.md CHANGELOG.md values.schema.json; do
    [ -f "$d/$f" ] && ok "$d/$f" || bad "$d/$f is missing"
  done
done
# The agent is the root package, so its docs are the root's. The gate is a
# package with a README; its changes are recorded in the root CHANGELOG.
for f in README.md CHANGELOG.md gate/README.md; do
  [ -f "$f" ] && ok "$f" || bad "$f is missing"
done

echo
[ "$fail" -eq 0 ] || { echo "PORTABILITY TEST FAILED -- see CONTRIBUTING.md"; exit 1; }
echo "portability test passed"
