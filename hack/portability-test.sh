#!/usr/bin/env bash
# Proves this repository runs on somebody else's cluster, in somebody else's
# CI, against somebody else's model.
#
# This replaced hack/extraction-test.sh on 2026-08-23. That script existed to
# prove `delivery/` could be lifted out of the platform repository that hosted
# it, and enforced a one-way link rule to keep the lift cheap. The lift has
# happened: this repository IS the package, there is no host to reach into, and
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
leaks() {
  grep -rniE "$LEAKS" --include='*.yaml' --include='*.yml' --include='*.go' \
    --include='*.json' --include='Dockerfile' . 2>/dev/null \
    | grep -v '^\./hack/portability-test\.sh'
}
if [ -n "$(leaks)" ]; then
  leaks | sed 's/^/        /' | head -20
  bad "host-environment literal in shipped code"
else
  ok "no host-environment literals"
fi

# The owner's name baked into a CHART is a leak: an adopter installing these
# charts must never inherit our registry or repository as a default.
#
# Scoped to charts/ on purpose. Go import paths, workflow image names and
# Chart.yaml `home:`/`sources:` are this project's own identity, not an
# assumption about anyone's environment -- flagging those made the old check
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
# globally wrong -- and nothing about it looks like a version problem. Both
# Dockerfiles already pin helm and kubeconform and say why; this asserts the
# flake pins the same strings, because three copies of a version number is
# exactly the shape that drifts.
# ---------------------------------------------------------------------------
echo "==> the dev shell and the images agree on what renders"
if [ -f flake.nix ]; then
  check_pin() { # <flake attribute> <Dockerfile ARG>
    local attr="$1" arg="$2" fv dv f mismatch=0
    fv="$(sed -n "s/^ *${attr} = \"\([^\"]*\)\";.*/\1/p" flake.nix | head -1)"
    if [ -z "$fv" ]; then
      bad "flake.nix does not pin ${attr}"
      return 0
    fi
    for f in Dockerfile gate/Dockerfile; do
      dv="$(sed -n "s/^ARG ${arg}=v\{0,1\}\(.*\)/\1/p" "$f" | head -1)"
      if [ "$dv" != "$fv" ]; then
        bad "${f} builds with ${arg}=${dv:-<unset>}, the dev shell with ${attr}=${fv}"
        mismatch=1
      fi
    done
    if [ "$mismatch" -eq 0 ]; then ok "${attr} ${fv} matches both images"; fi
    return 0
  }
  check_pin helmVersion HELM_VERSION
  check_pin kubeconformVersion KUBECONFORM_VERSION
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
# The two commands. The agent is the root package, so its docs are the root's.
for d in . gate; do
  for f in README.md CHANGELOG.md; do
    [ -f "$d/$f" ] && ok "${d#./}/$f" || bad "$d/$f is missing"
  done
done

echo
[ "$fail" -eq 0 ] || { echo "PORTABILITY TEST FAILED -- see CONTRIBUTING.md"; exit 1; }
echo "portability test passed"
