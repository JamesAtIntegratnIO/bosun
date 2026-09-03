package gate

import (
	"fmt"
	"sort"
	"strings"
)

// Row is one generated Application, normalized so two renders can be compared
// regardless of field ordering, whitespace, or where in the values layers a
// setting came from.
type Row struct {
	AppSet string `json:"appset"`
	// FromAppSet is whether AppSet names an ApplicationSet.
	//
	// It does not always, and that is the reason this field exists. An
	// expanded row carries the ApplicationSet's own `metadata.name`; a row
	// read from an Application this repository commits directly carries the
	// name of the config SOURCE it was read from, which is a label an
	// operator chose rather than an object anything serves. The two are
	// indistinguishable in the string.
	//
	// The diff groups by AppSet either way and has no use for the
	// distinction. A surface that publishes "the ApplicationSet this
	// Application was generated from" has every use for it: without this,
	// half those answers name something that does not exist.
	FromAppSet bool   `json:"fromAppSet,omitempty"`
	Cluster    string `json:"cluster"`
	App        string `json:"app"`
	Project    string `json:"project"`
	Namespace  string `json:"namespace"`
	// SourceType is how this Application gets its manifests, which is a
	// different vocabulary from the config's SourceType (manifests, rendered,
	// helm, kustomize, argocd-bootstrap) despite the shared name; that one
	// says where the gate reads Applications from. Named separately so the two
	// cannot be assigned to each other, and typed so the accepted values are
	// the const block rather than a trailing comment; the comment here claimed
	// a `manifest` value nothing ever assigns.
	SourceType RowSource `json:"sourceType"`
	ChartRepo  string    `json:"chartRepo"`
	Chart      string    `json:"chart"`
	Version    string    `json:"version"`
	Path       string    `json:"path"`

	// ValueFiles and ValuesInline are what this Application renders its chart
	// with. Carried on the row so a diff can re-render the chart at both
	// versions later, without a second pass over the repository.
	//
	// Rendering with the wrong values is worse than not rendering: a chart
	// default that this repository overrides would show as a change when
	// nothing changed, and the override that matters, the one being flipped
	// out from under you, would be invisible.
	ValueFiles   []string `json:"valueFiles,omitempty"`
	ValuesInline string   `json:"valuesInline,omitempty"`
}

// Key identifies an Application across renders. Deliberately excludes Version:
// a version change is an expected, reportable event, whereas a change to this
// key means the Application itself moved, appeared or vanished.
func (r Row) Key() string {
	return r.Cluster + "\x00" + r.App
}

type Table struct {
	Rows []Row `json:"rows"`
	// Objects are the Kubernetes resources a source produced directly,
	// from a rendered-manifests branch, or from any source whose output is
	// not itself an Application. Empty when nothing in the repository is
	// rendered, which is the common case and is why the object diff is
	// reported only when there is something to report.
	Objects  []Object   `json:"objects,omitempty"`
	Warnings []Markdown `json:"warnings,omitempty"`

	// ValuesLeaves is, per Application, the set of scalar values this
	// repository's own values supply to it, rendered as strings. Filled by
	// chart-diff for the rows it rendered, nil everywhere else, and consumed
	// by the object diff to mark the changed fields a reader actually chose;
	// nil means "not known", never "none", which is why the mark carries a
	// checked flag beside it. The map value is whether the leaf may match as
	// a substring, false for the Application's own identity tokens; both
	// kinds match on equality.
	ValuesLeaves map[string]map[string]bool `json:"-"`
}

func (t *Table) Sort() {
	sort.Slice(t.Objects, func(i, j int) bool { return t.Objects[i].ID() < t.Objects[j].ID() })
	sort.Slice(t.Rows, func(i, j int) bool {
		if t.Rows[i].Cluster != t.Rows[j].Cluster {
			return t.Rows[i].Cluster < t.Rows[j].Cluster
		}
		return t.Rows[i].App < t.Rows[j].App
	})
}

// Describe renders a row's source in one human-readable string.
func (r Row) Describe() string {
	switch r.SourceType {
	case RowHelm:
		repo := r.ChartRepo
		if repo != "" && !strings.HasSuffix(repo, "/") {
			repo += "/"
		}
		return fmt.Sprintf("%s%s %s", repo, r.Chart, r.Version)
	case RowPath:
		return fmt.Sprintf("%s (%s)", r.Path, r.SourceType)
	default:
		// A row with no source type at all. It reads as the empty string,
		// which is honest; there is nothing to describe.
		return string(r.SourceType)
	}
}

// RowSource is how one Application in the table gets its manifests.
type RowSource string

const (
	// RowHelm is a chart pulled at a pinned version, the rows chart-diff can
	// render on both sides of a bump.
	RowHelm RowSource = "helm"
	// RowPath is a directory in the repository, rendered as it stands.
	RowPath RowSource = "path"
)
