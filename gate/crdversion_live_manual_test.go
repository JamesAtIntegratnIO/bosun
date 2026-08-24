package main

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestLiveCRDVersionDetection feeds the served-version comparison two REAL
// CustomResourceDefinitions and checks it finds the removal.
//
// cert-manager v1.5.5 served v1alpha2, v1alpha3, v1beta1 and v1; v1.6.0 serves
// only v1. (v1.7 later deleted the old versions from the CRD entirely, which is
// the tidier-sounding bump and the wrong one: by then they were already
// unserved, so nothing changes at apply time. This gate is precise about that
// distinction, which is how a demo pointing at 1.6->1.7 was found to be
// pointing at the wrong pair.)
//
// Render the fixtures with:
//
//	helm template cm cert-manager --repo https://charts.jetstack.io \
//	  --version v1.5.5 --include-crds --set installCRDs=true > old.yaml
//
//	PROBE_OLD=old.yaml PROBE_NEW=new.yaml go test ./gate -run LiveCRDVersion -v
func TestLiveCRDVersionDetection(t *testing.T) {
	oldPath, newPath := os.Getenv("PROBE_OLD"), os.Getenv("PROBE_NEW")
	if oldPath == "" {
		t.Skip("set PROBE_OLD/PROBE_NEW")
	}
	load := func(p string) Object {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range strings.Split(string(raw), "\n---") {
			if !strings.Contains(d, "certificates.cert-manager.io") || !strings.Contains(d, "kind: CustomResourceDefinition") {
				continue
			}
			var m map[string]any
			if err := yaml.Unmarshal([]byte(d), &m); err != nil {
				t.Fatal(err)
			}
			o, ok := objectFrom("app", "local", "cert-manager", m)
			if !ok {
				t.Fatal("objectFrom refused it")
			}
			return o
		}
		t.Fatal("no Certificate CRD in " + p)
		return Object{}
	}
	a, b := load(oldPath), load(newPath)
	t.Logf("base served: %v", servedVersions(a))
	t.Logf("head served: %v", servedVersions(b))

	gone := droppedVersions(a, b)
	if len(gone) == 0 {
		t.Fatal("no dropped versions found between two CRDs that demonstrably drop three")
	}
	t.Logf("dropped: %v  consumers declare %q  survivor %q", gone, crdConsumerKind(b), survivingVersion(b))
	if k := crdConsumerKind(b); k != "Certificate" {
		t.Errorf("consumer kind = %q, want Certificate -- the repair contract needs it", k)
	}
	if v := survivingVersion(b); v != "v1" {
		t.Errorf("survivor = %q, want v1", v)
	}
}
