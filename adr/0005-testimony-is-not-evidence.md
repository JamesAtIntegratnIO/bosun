# 5. Testimony is not evidence, and it is aimed by the gate

- **Status:** accepted
- **Date:** 2026-08-24

## Context

The agent's explanations were grounded in two things: the gate's render diff,
which is computed, and the maintainers' release notes, which are claimed. That
split is stated in the prompt and enforced by the applier, which corroborates
any version-shaped value an edit writes against the evidence string the model
was shown.

Four live re-runs exposed the gap. A chart bump removed a `ClusterRole` and a
`ClusterRoleBinding`. The gate proved it. No release in the range said anything
about it. The best the agent could say was *"no release notes explain why"* —
correct, honest, and a handoff that gives a human a search rather than an
answer.

The commits between the two upstream tags did explain it. They usually do:
nobody polishes a commit message for a changelog, and it sits next to the code
it describes.

Reading them raises two questions that are not about plumbing.

**Which commits?** A range can be hundreds. Handing the model the whole range
and asking which ones support its conclusion is not evidence — it is a second
opinion from the same opinion, and it is exactly the shape this project has
avoided everywhere else.

**Which two refs?** A chart version and the git tags of the project it packages
are frequently different numbering. `compare/0.5.8...1.0.0` against a repository
whose tags are app versions is a 404 at best and, at worst, two real tags from
an unrelated sequence — which returns real commits from a range that is not this
promotion's, and reads exactly like the truth.

## Decision

**Commits are a third source, labelled testimony, alongside release notes.**
The gate report remains the only fact. The prompt says so in those words.

**The gate chooses the evidence.** `migrate.Subjects` reads the kinds and
resource names out of the gate's own findings — `ClusterRole`,
`trivy-operator-explorer`, a dropped CRD's kind and group — and those terms are
matched by string against commit messages and against the paths in the upstream
diff. The file paths carry most of the weight: a commit titled *"watch
namespaces via config"* does not contain the string `ClusterRole`; the template
it deleted does.

**Testimony never reaches the write path.** Not "the prompt tells it not to" —
the mechanical path does not fetch upstream at all, so no commit message is ever
in the evidence string the applier corroborates against. Without that rule, a
commit mentioning `v1.5.0` would make `v1.5.0` a corroborated value to write,
which is precisely the failure the corroboration check exists to prevent.
Upstream is read on the paths that produce prose: the green-gate explanation,
and an escalation.

**A range that cannot be established is not guessed.** Refs come from the
project's own release tags — base is the release the repository is *leaving*,
head is the newest in range — or, when no release lands in the promotion's
numbering, from the `org.opencontainers.image.revision` the publisher recorded
at build time. When neither meets, no comparison is made and the note says which
namespaces failed to meet.

**`CompareResolver` is a second interface**, type-asserted rather than added to
`Resolver`. A resolver that only reads releases keeps compiling and simply
contributes no commits.

## Consequences

**Good.** The class of finding the render proves and cannot explain now has an
answer, with links a reviewer can follow. The interesting negative survives too:
*"312 commits between these tags and none of them mentions this"* is a real fact
about a bump, and an empty section that simply vanished would have read as
"nothing was looked for".

**Bad.** String matching over commit subjects is crude. It will miss a commit
that describes the change in words the gate did not use, and the honest
consequence is a shorter section rather than a wrong one. It will also
occasionally include something irrelevant that happens to share a name.

**Also.** Two extra `api.github.com` calls on escalation paths, on a host the
agent already reaches. GitHub answers a compare with at most 250 commits and
reports the real total separately, so a larger range is marked truncated rather
than filtered as if it had been fully read.

## Alternatives rejected

- **Let the model pick the commits.** The failure mode is a model selecting the
  commits that agree with what it already decided.
- **Compare the promotion's own versions.** Silently wrong whenever the chart
  and the app are numbered differently, which is most of the interesting cases.
- **Show the commits on every path, including mechanical.** Would widen the
  corroboration surface to text nobody computed. The whole guarantee is that a
  version the agent writes was computed by the gate.
- **Page the whole range.** Thousands of commits to answer "what touched the
  ClusterRole" is not a question worth that, and a truncation that says so is
  more useful than an exhaustive read that costs the rate limit.
