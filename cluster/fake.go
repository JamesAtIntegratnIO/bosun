package cluster

import (
	"context"
	"fmt"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// Fake is an in-memory Reader.
//
// Outside _test.go for the same reason gitprovider.Fake is: the caller under
// test lives in package main, and giving the triage tests a cluster they can
// set up needs a seam something other than the cluster package can construct.
type Fake struct {
	// Counts are keyed "group/version/plural". A key that is absent answers
	// the way an unlisted API group does in production -- not permitted --
	// rather than zero, because a fake whose default is the safest possible
	// answer would let a brief that prints unknowns as "0" pass its tests.
	Counts map[string]Count
	// Apps are keyed by Application name, same rule.
	Apps map[string]Health
	// CRDs are keyed by <plural>.<group>, same rule.
	CRDs map[string]CRD

	// Inventory is what ClusterInventory answers. Nil answers the way a
	// cluster with no ArgoCD Secrets does -- an error -- because a fake that
	// silently handed back an empty inventory would let a gate that renders
	// against nothing pass its tests.
	Inventory    *gate.Inventory
	InventoryErr error

	// CountCalls and AppCalls record what was asked, so a test can assert the
	// agent did not read the cluster on a path that must not.
	CountCalls []string
	AppCalls   []string
	CRDCalls   []string
}

func (f *Fake) ClusterInventory(_ context.Context) (*gate.Inventory, error) {
	if f.InventoryErr != nil {
		return nil, f.InventoryErr
	}
	if f.Inventory == nil {
		return nil, fmt.Errorf("no ArgoCD cluster Secrets in namespace %q -- the gate cannot expand a generator against an empty inventory", "argocd")
	}
	return f.Inventory, nil
}

func (f *Fake) Name() string { return "fake-cluster" }

func (f *Fake) CountLive(_ context.Context, group, version, plural string) Count {
	key := gvk(group, version, plural)
	f.CountCalls = append(f.CountCalls, key)
	if c, ok := f.Counts[key]; ok {
		return c
	}
	return Count{Note: "not permitted to check " + key}
}

func (f *Fake) CRD(_ context.Context, name string) CRD {
	f.CRDCalls = append(f.CRDCalls, name)
	if c, ok := f.CRDs[name]; ok {
		return c
	}
	return CRD{Note: "not permitted to read CustomResourceDefinitions"}
}

func (f *Fake) AppHealth(_ context.Context, name string) Health {
	f.AppCalls = append(f.AppCalls, name)
	if h, ok := f.Apps[name]; ok {
		return h
	}
	return Health{Note: "not permitted to read Applications"}
}
