# ArgoCD API fixtures

Recorded shapes, not a recorded capture. These files are hand-written to the
structures the ADR 0012 assessment measured on live installs, and every field
present is one the reads in `argocdapps.go` decode. They are not a sanitised
dump of anyone's ArgoCD, so they prove the decode handles each shape; they do
not prove those are the only shapes in the wild.

If a capture from a live install is ever taken, it should replace these rather
than sit beside them: two sets of fixtures for one wire contract is the drift
this directory exists to prevent.

| File | Shape |
|---|---|
| `homelab-applications.json` | one ArgoCD holding a fleet: multi-source addons resolving `$values/` through a sibling `ref:`, in-repo directory apps, a hydrated app with a `drySource`, inline `valuesObject`, and the same repository spelt three ways |
| `homelab-applicationsets.json` | 4 ApplicationSets, 2 of them roots with no tracking annotation |
| `split-repo-applications.json` | roots in an infrastructure repository, content in a second one: `directory.recurse` with an `exclude` pointer, and nothing at all belonging to the content repository's own config |
| `split-repo-applicationsets.json` | one untracked root whose manifest is not in the gated repository |
