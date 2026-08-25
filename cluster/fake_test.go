package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// A fake is a claim about the real thing, and every test that uses one
// inherits the claim. These pin the two properties that make this fake safe to
// build on: it satisfies the interface it stands in for, and its DEFAULTS are
// the pessimistic answer rather than the convenient one.

var _ Reader = (*Fake)(nil)

// The whole point of the fake's design: an absent key answers the way an
// unlisted API group does in production -- not permitted -- rather than zero.
// Defaulting to zero would let a brief that prints unknowns as "0 live
// objects" pass its tests, and "nothing is using this" is the sentence that
// ends a conversation.
func TestTheFakeDefaultsToNotPermittedRatherThanZero(t *testing.T) {
	f := &Fake{}
	ctx := context.Background()

	c := f.CountLive(ctx, "apps", "v1", "deployments")
	if c.Known {
		t.Error("an unlisted group must not read as a known zero")
	}
	if c.N != 0 || !strings.Contains(c.Note, "not permitted") {
		t.Errorf("got %+v", c)
	}
	// And the note is what String prints, so the number never reaches a
	// reader on its own.
	if got := c.String(); got == "0" {
		t.Errorf("an unknown count must not render as %q", got)
	}

	if h := f.AppHealth(ctx, "podinfo"); h.Known || !strings.Contains(h.Note, "not permitted") {
		t.Errorf("got %+v", h)
	}
	if crd := f.CRD(ctx, "widgets.example.io"); crd.Known || !strings.Contains(crd.Note, "not permitted") {
		t.Errorf("got %+v", crd)
	}
}

// A configured answer is returned as configured, including a known zero --
// which is a different fact from "nobody checked" and the one the whole
// package exists to keep apart.
func TestTheFakeReturnsAKnownZeroWhenToldTo(t *testing.T) {
	f := &Fake{
		Counts: map[string]Count{"example.io/v1/widgets": {N: 0, Known: true}},
		Apps:   map[string]Health{"podinfo": {Status: "Degraded", Sync: "Synced", Known: true}},
		CRDs:   map[string]CRD{"widgets.example.io": {Versions: []string{"v1"}, Known: true}},
	}
	ctx := context.Background()

	c := f.CountLive(ctx, "example.io", "v1", "widgets")
	if !c.Known || c.N != 0 {
		t.Errorf("a known zero must survive the fake: %+v", c)
	}
	if got := c.String(); got != "0" {
		t.Errorf("a known zero prints as a number, got %q", got)
	}
	if h := f.AppHealth(ctx, "podinfo"); h.String() != "Degraded / Synced" {
		t.Errorf("got %q", h.String())
	}
	if crd := f.CRD(ctx, "widgets.example.io"); len(crd.Versions) != 1 {
		t.Errorf("got %+v", crd)
	}
}

// The call records exist so a test can assert the agent did NOT read the
// cluster on a path that must not.
func TestTheFakeRecordsWhatWasAsked(t *testing.T) {
	f := &Fake{}
	ctx := context.Background()
	f.CountLive(ctx, "example.io", "v1", "widgets")
	f.CountLive(ctx, "", "v1", "pods")
	f.AppHealth(ctx, "podinfo")
	f.CRD(ctx, "widgets.example.io")

	if len(f.CountCalls) != 2 || f.CountCalls[0] != "example.io/v1/widgets" {
		t.Errorf("got %v", f.CountCalls)
	}
	// The core group has no group name, and the key has to say so the same
	// way the real reader's does.
	if f.CountCalls[1] != "v1/pods" {
		t.Errorf("got %q", f.CountCalls[1])
	}
	if len(f.AppCalls) != 1 || len(f.CRDCalls) != 1 {
		t.Errorf("got %v / %v", f.AppCalls, f.CRDCalls)
	}
}

// A nil inventory is an ERROR, not an empty one: a fake that silently handed
// back nothing would let a gate that renders against no clusters pass.
func TestTheFakeRefusesToInventNothing(t *testing.T) {
	if _, err := (&Fake{}).ClusterInventory(context.Background()); err == nil {
		t.Fatal("an unset inventory must not read as a cluster with no Secrets")
	}
	inv, err := (&Fake{Inventory: &gate.Inventory{Clusters: []gate.Cluster{{Name: "hub"}}}}).
		ClusterInventory(context.Background())
	if err != nil || len(inv.Clusters) != 1 {
		t.Errorf("got %+v / %v", inv, err)
	}
}

func TestTheFakeIsNamedSoALogSaysItWasUsed(t *testing.T) {
	if got := (&Fake{}).Name(); !strings.Contains(got, "fake") {
		t.Errorf("a log line must make a fake reader obvious, got %q", got)
	}
}
