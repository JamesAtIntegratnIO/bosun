package gate

import (
	"fmt"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// AnnotateConsumers counts, for every dropped served version, the manifests in
// the repository that still declare it. Those manifests are the actual blast
// radius, they are what breaks at apply, so Blocking() keys on them: consumers
// present blocks, consumers counted at zero reports.
//
// Only possible with a worktree, which is why it runs from the diff command's
// -repo path and nowhere else. A diff without a repository leaves the findings
// unscanned, and unscanned blocks.
func AnnotateConsumers(repoRoot string, res *DiffResult) {
	for i := range res.Objects {
		o := &res.Objects[i]
		if o.Kind != ObjectCRDVersionRemoved {
			continue
		}
		d, ok := droppedFromChange(*o)
		if !ok {
			// No consumer kind or no surviving version: the finding cannot
			// name what to look for, so it stays unscanned and blocking.
			continue
		}
		hits, err := migrate.Scan(repoRoot, d)
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

// markMigrationConsistent flags apiVersion moves that are exactly what a
// crdVersionRemoved finding in the same diff demands: the same consumer kind,
// from a dropped version, to the named survivor. That move is the repair, not
// a new migration, and without this, no pull request that fixes a dropped
// served version could ever go green, because the fix itself would trip the
// apiVersion rule. The first live repair proved it: 27 manifests migrated,
// consumers counted at zero, and the gate red on its own prescription.
//
// The match is deliberately exact. A move to any other version, or of any
// kind the findings do not name, is still an unexplained migration and still
// blocks.
func markMigrationConsistent(objects []ObjectChange) {
	allowed := map[string]bool{}
	for _, o := range objects {
		if o.Kind != ObjectCRDVersionRemoved {
			continue
		}
		d, ok := droppedFromChange(o)
		if !ok || d.Target == "" {
			continue
		}
		for _, v := range d.Versions {
			allowed[d.Kind+"|"+d.Group+"/"+v+"|"+d.Group+"/"+d.Target] = true
		}
	}
	if len(allowed) == 0 {
		return
	}
	for i := range objects {
		o := &objects[i]
		if o.Kind != ObjectAPIVersionMoved {
			continue
		}
		kind := o.Object
		if j := strings.Index(kind, "/"); j >= 0 {
			kind = kind[:j]
		}
		if allowed[kind+"|"+o.From+"|"+o.To] {
			o.PartOfMigration = true
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
	// No Target required: a CRD removed outright has no survivor, and its
	// consumers are exactly as scannable; Scan needs the group, the kind and
	// the versions; only a rewrite needs a destination, and Migrate refuses an
	// empty one on its own.
	dot := strings.Index(name, ".")
	if dot < 0 || o.Resource == "" {
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
