# Rendered manifests, and ArgoCD's source hydrator

Short version: **the source hydrator cannot feed a pre-merge gate**, but it can
give you an exact baseline, and if your repository renders manifests into git
by any means the gate will diff them at resource level, which is a far stronger
signal than anything derivable from Application definitions.

## Why the hydrator does not gate a merge

ArgoCD's source hydrator (beta since v3.5) renders your dry source and commits
plain YAML to a hydrated branch. It looks like it should solve the diff problem
outright. It does not, for one reason:

> "The hydrator triggers only when a new commit is detected in the dry source."

Hydration is a function of the **configured** dry revision, whatever is on
`main`. It never runs for a pull request's head commit. There is no dry-run
API, no preview endpoint, and no commit-server call that renders without
committing. The only way to hydrate a proposed change is to create a throwaway
Application pointing at the PR branch, which needs a live ArgoCD, git write
credentials, and leaves real commits behind.

`hydrateTo` is often mistaken for the answer. It pushes hydrated output to a
staging branch so something else can PR it onward:

> "Argo CD will only push changes to the hydrateTo branch, it will not create a
> PR or otherwise facilitate moving those changes to the syncSource branch."

That gates a deploy, not a merge. The sequence is: merge to `main` → hydrate →
push to `environments/dev-next` → your tooling opens `dev-next → dev`. By the
time a rendered diff exists, the code change is already in.

Kargo does what the hydrator declines to do: `helm-template` /
`kustomize-build` → `git-commit` → `git-open-pr`. If you want rendered YAML in
a pull request *before* merge, that is the mechanism.

## What this gate does with it

**`type: rendered`** reads manifests already committed to git: hydrator output,
Kargo's rendered promotion branches, or any CI job that commits its render.

```yaml
sources:
  - name: hydrated
    type: rendered
    paths: ["environments/prod/**/manifest.yaml"]
```

Objects from these sources are diffed at resource level: added, removed,
changed, and, called out separately, **apiVersion changed**, which is the one
that blocks. An API version moving under an existing resource is a migration,
and a migration is the class of change a render cannot catch.

Using a hydrated branch as the **baseline** works well: `hydrator.metadata`
carries `drySha`, so you can tie rendered output back to the commit that
produced it. Since ArgoCD v3.3 the last-hydrated SHA lives in a git note
(`refs/notes/hydrator.metadata`) and the branch has *no commit* when the render
did not change, so map hydrated to dry via the note, never the commit log.

## What the object diff does not cover

If your repository does **not** render manifests into git, `type: rendered`
sources contribute nothing. Two things fill most of that gap, and one hole
remains.

**A chart whose version moved is covered.** `diff -repo <path>` pulls the chart
at both versions, renders each with that Application's own value files, and
diffs the resources down to the field ([`chartdiff.go`](../chartdiff.go)). A
chart whose defaults flip, adding NetworkPolicies you did not ask for, is a
one-line version change at Application level and an obvious addition at object
level.

**A values-only change is not.** Chart-diff runs only for rows whose version
moved, because it costs a chart pull and two renders per changed Application.
Editing values under an unchanged chart version is compared at Application
level only: which Applications exist, on which clusters, at which versions.

For that residual case, a report is a weaker signal than it looks, and a triage
agent reading it is reasoning from less than it appears to have. Rendering
manifests into git closes it.

## Things ArgoCD does not give you

These look like they should exist:

- **Nothing offline expands ApplicationSets.** `argocd appset generate` looks
  like the offline tool and is an RPC to a live API server, as is `--dry-run`.
  Rendering generators yourself is the only offline route.
- **The hydrator is single-source.** `sourceHydrator` and `sources` are
  mutually exclusive, so Helm values from a second repository do not work with
  it.
- **The hydrator does not sign commits**, so a hydrated branch with signature
  verification enabled will reject them.
