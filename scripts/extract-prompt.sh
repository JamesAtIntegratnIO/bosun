#!/usr/bin/env bash
# Print a shipped prompt, so evals measure the prompt that actually ships
# rather than a copy that has drifted from it.
#
#   extract-prompt.sh                 # systemPrompt, the triage classifier
#   extract-prompt.sh explainPrompt   # the green-gate explanation
#
# Two prompts ship and both are measured. Hard-coding one symbol here is how
# the second went unmeasured for as long as it did.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
SYMBOL="${1:-systemPrompt}" python3 - <<'PY'
import os, pathlib, re, sys
symbol = os.environ["SYMBOL"]
s = pathlib.Path("prompt.go").read_text()
m = re.search(r'const ' + re.escape(symbol) + r' = `(.*?)`\n', s, re.S)
if not m:
    sys.exit(f"no `const {symbol}` in prompt.go")
print(m.group(1), end="")
PY
