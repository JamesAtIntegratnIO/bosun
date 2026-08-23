# bosun-sample

The repository under test. It is pushed into Gitea by `scripts/20-seed.sh` and
deliberately almost empty.

Each scenario clones this repository, writes one incident's recorded fixture
files onto a branch under `addons/`, and opens a pull request. The fixture
files ARE the content — anything committed here would only be overwritten.

`addons/` exists so that the path allowlist Bosun is installed with
(`triage.allowPaths=addons/**`) has a real directory behind it, and so the
first clone is not an empty tree.
