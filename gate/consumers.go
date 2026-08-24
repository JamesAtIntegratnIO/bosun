package main

import (
	"fmt"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// AnnotateConsumers counts, for every dropped served version, the manifests in
// the repository that still declare it. Those manifests are the actual blast
// radius -- they are what breaks at apply -- so Blocking() keys on them:
// consumers present blocks, consumers counted at zero reports.
//
// Only possible with a worktree, which is why it runs from the diff command's
// -repo path and nowhere else. A diff without a repository leaves the findings
// unscanned, and unscanned blocks.
func AnnotateConsumers(res *DiffResult, root string) {
	for i := range res.Objects {
		o := &res.Objects[i]
		if o.Kind != "crdVersionRemoved" {
			continue
		}
		d, ok := droppedFromChange(*o)
		if !ok {
			// No consumer kind or no surviving version: the finding cannot
			// name what to look for, so it stays unscanned and blocking.
			continue
		}
		hits, err := migrate.Scan(root, d)
		if err != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("could not scan the repository for %s consumers: %v", d.CRD, err))
			continue
		}
		o.ConsumersKnown = true
		for _, h := range hits {
			o.ConsumerFiles = append(o.ConsumerFiles, h.Path)
		}
	}
}

// droppedFromChange rebuilds the migration contract from a finding. It is the
// in-process twin of migrate.ParseReport: the same data, before it has been
// through a report line.
func droppedFromChange(o ObjectChange) (migrate.Dropped, bool) {
	name := strings.TrimPrefix(o.Object, "CustomResourceDefinition/")
	if i := strings.Index(name, " in "); i >= 0 {
		name = name[:i]
	}
	dot := strings.Index(name, ".")
	if dot < 0 || o.Resource == "" || o.To == "" {
		return migrate.Dropped{}, false
	}
	var versions []string
	for _, v := range strings.Split(o.From, ",") {
		if v = strings.TrimSpace(v); v != "" {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return migrate.Dropped{}, false
	}
	return migrate.Dropped{
		CRD:      name,
		Group:    name[dot+1:],
		Kind:     o.Resource,
		Versions: versions,
		Target:   o.To,
	}, true
}
