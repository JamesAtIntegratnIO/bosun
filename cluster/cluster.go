// Package cluster answers "what is actually running?".
//
// The gate renders a repository and compares. Everything it knows is a
// property of the text: which manifests declare an API version, which
// Applications target which clusters, what a chart's output looks like at two
// versions. It cannot see the cluster, and CI structurally cannot -- which is
// the argument ADR 0002 made for putting triage in the cluster in the first
// place, and which ADR 0008 finished: the gate itself now runs here too by
// default, reading its inventory through this package instead of from a
// checked-in snapshot.
//
// This is that argument's other half, finally built. A gate report saying "3
// manifests in this repository still declare a version this chart stops
// serving" is a fact about the repository. "And 0 objects are live on it" is a
// fact about the cluster, and it is frequently the one that decides whether a
// human needs to be woken up.
//
// Three rules shape everything here.
//
// READ-ONLY, structurally. Only GET, only LIST. The chart's ClusterRole has no
// create, update, patch or delete verb anywhere and says a feature needing one
// should be reconsidered; this package is the first Go code to use that role
// and it does not change that sentence.
//
// NEVER AN ERROR FOR "COULD NOT LOOK". A denied group, an unreachable
// apiserver, a CRD the cluster does not serve -- all ordinary, all reported as
// a note that says which. Cluster facts inform a brief; losing them must never
// be the reason a pull request goes unattended.
//
// NO client-go. This package is four endpoints and a bearer token, and the
// house rule -- the git client and the App JWT are both hand-rolled with an
// ADR-style comment saying why -- is that a vendored SDK should not become the
// largest dependency in a service whose whole point is being small enough to
// audit. client-go would be roughly forty times the size of everything here.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Count is how many live objects were found, and whether anybody was allowed
// to look.
//
// Known is the field that matters. A zero with Known false is "nobody
// checked", and a brief that printed it as "0 live objects" would be stating
// the safest possible fact on no evidence -- which is worse than saying
// nothing, because a reader acts on it.
type Count struct {
	N     int
	Known bool
	// AtLeast is set when the walk hit its own bound. The number is then a
	// floor rather than a total, and the brief has to say so.
	AtLeast bool
	// Note explains an unknown or partial answer in one clause.
	Note string
}

// String renders the count the way a brief says it.
//
// The note wins whenever there is one, and a note is only ever set when the
// bare number would mislead. "The cluster does not serve this API" is a KNOWN
// answer whose number is zero, and printing that zero on its own would say
// "nothing is using it" when what happened is that the question does not apply
// here.
func (c Count) String() string {
	switch {
	case c.Note != "":
		return c.Note
	case c.AtLeast:
		return fmt.Sprintf("at least %d", c.N)
	default:
		return fmt.Sprintf("%d", c.N)
	}
}

// Health is an ArgoCD Application's live status.
type Health struct {
	Status string
	Sync   string
	Known  bool
	Note   string
}

func (h Health) String() string {
	if h.Note != "" {
		return h.Note
	}
	return fmt.Sprintf("%s / %s", h.Status, h.Sync)
}

// CRD is what the cluster currently serves for one CustomResourceDefinition.
//
// Read for the finding the report cannot make countable on its own: a chart
// that stops shipping a CRD ENTIRELY names the object but not its versions, and
// a collection cannot be listed without one. Pre-merge the definition is still
// installed, so asking the cluster is both possible and the only honest source
// -- the versions a repository's manifests happen to declare are not the
// versions objects are stored under.
type CRD struct {
	Versions []string
	// Schemas are the OpenAPI schemas the cluster serves, keyed by version
	// name. Populated for the same reason the versions are: when a chart moves
	// a field between versions, the OLD shape is only knowable from the
	// definition that is installed right now, and after the merge it is gone.
	//
	// Decoded maps rather than a typed struct -- these carry vendor extensions
	// no generic OpenAPI struct models, and a field this code does not
	// understand must survive rather than be dropped.
	Schemas map[string]map[string]any
	Known   bool
	Note    string
}

// Reader is the read-only view of the cluster the agent is allowed.
//
// Deliberately a fixed set of named questions rather than a generic client. A
// generic client is an invitation to ask a new question from anywhere; a named
// method per question makes every cluster read something a reviewer can find
// by searching for it. Each one below states what it answers and why it is
// worth an apiserver round trip.
//
// It mirrors upstream.Resolver's contract exactly: no error means "could not
// look", ever.
type Reader interface {
	// CountLive counts the objects live on one group/version/plural.
	//
	// The coordinates come from the GATE'S REPORT, not from a cluster lookup.
	// That matters precisely when it is most wanted: for a CustomResourceDefinition
	// removed outright, an apiextensions GET would 404 at the moment the
	// question "is anything still using this" is most worth answering.
	CountLive(ctx context.Context, group, version, plural string) Count

	// AppHealth reads one ArgoCD Application's live health and sync state.
	//
	// The promotion has carried `verifyApps` on the wire since the first
	// version of this service and nothing has ever read it. "This Application
	// was already Degraded before your bump" is the single most useful thing
	// that can be said to somebody looking at a red gate, and it was one field
	// away the whole time.
	AppHealth(ctx context.Context, name string) Health

	// CRD reports the versions a CustomResourceDefinition currently serves.
	//
	// Its name is <plural>.<group>, which is the shape the API machinery
	// guarantees and the shape the gate's report already prints.
	CRD(ctx context.Context, name string) CRD

	// Name identifies the reader in logs.
	Name() string
}

// listPath builds the collection URL for a group/version/plural. The core
// group lives under /api/v1 rather than /apis, which is the one irregularity
// in the whole API surface and the one thing a hand-rolled client gets wrong.
func listPath(group, version, plural string) string {
	if group == "" {
		return fmt.Sprintf("/api/%s/%s", version, plural)
	}
	return fmt.Sprintf("/apis/%s/%s/%s", group, version, plural)
}

// list is the shape of any collection response, reduced to what is counted.
//
// metadata.remainingItemCount is deliberately NOT used. The apiserver sets it
// only for lists served from etcd -- not the default watch-cache path -- and
// documents it as best-effort, so treating its absence as "no more items"
// silently under-counts and presents the result as a fact. The pages are
// walked instead.
type list struct {
	Items    []json.RawMessage `json:"items"`
	Metadata struct {
		Continue string `json:"continue"`
	} `json:"metadata"`
}

// gvk renders coordinates for a note, so every message names the same thing
// the same way.
func gvk(group, version, plural string) string {
	if group == "" {
		return version + "/" + plural
	}
	return strings.Join([]string{group, version, plural}, "/")
}
