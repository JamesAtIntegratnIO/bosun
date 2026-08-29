#!/usr/bin/env python3
"""Assert every relative markdown link in the tree resolves inside the tree.

This is the precise form of the one-way link rule in CONTRIBUTING.md: after
extraction the package is the whole world, so a link that resolves above the
package root is a link that will 404 in the standalone repository.

Skips absolute URLs, anchors, and mailto:. Fragments are stripped before the
path is checked, so `foo.md#section` is validated as `foo.md`.
"""
import pathlib
import re
import sys
import urllib.parse

LINK = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
SKIP_SCHEME = ("http://", "https://", "mailto:", "#", "data:")

# Directories whose markdown is not this package's documentation.
#
# `site/` is the documentation website, and its links follow the opposite rule:
# they are absolute site routes (`/start/quickstart/`), which this checker is
# specifically here to reject. Those are verified instead by
# site/scripts/check-links.mjs, which resolves every link against the built
# output, a stricter check than this one, because it knows which pages exist.
#
# The rest are vendored or generated trees that happen to contain READMEs.
PRUNE = {"node_modules", "dist", ".astro", ".git", ".desloppify"}
PRUNE_PATHS = {("site",)}


def _pruned(rel: pathlib.PurePath) -> bool:
    parts = rel.parts
    if any(part in PRUNE for part in parts):
        return True
    return any(parts[: len(prefix)] == prefix for prefix in PRUNE_PATHS)


def main(root_arg: str) -> int:
    root = pathlib.Path(root_arg).resolve()
    problems = []

    for md in sorted(root.rglob("*.md")):
        rel_md = md.relative_to(root)
        if _pruned(rel_md):
            continue
        for lineno, line in enumerate(md.read_text(encoding="utf-8").splitlines(), 1):
            for target in LINK.findall(line):
                target = target.strip()
                if not target or target.startswith(SKIP_SCHEME):
                    continue
                # Strip any "title" suffix and the fragment.
                target = target.split(" ", 1)[0]
                target = target.split("#", 1)[0]
                if not target:
                    continue
                target = urllib.parse.unquote(target)

                if target.startswith("/"):
                    problems.append((rel_md, lineno, target, "absolute path"))
                    continue

                resolved = (md.parent / target).resolve()
                try:
                    resolved.relative_to(root)
                except ValueError:
                    problems.append((rel_md, lineno, target, "resolves outside the package"))
                    continue
                if not resolved.exists():
                    problems.append((rel_md, lineno, target, "does not exist"))

    for rel_md, lineno, target, why in problems:
        print(f"        {rel_md}:{lineno}: {target} -- {why}", file=sys.stderr)

    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "."))
