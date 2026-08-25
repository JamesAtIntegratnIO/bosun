package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
	"github.com/JamesAtIntegratnIO/bosun/structural"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// Where the two schemas come from, and why it is not one place.
//
// A structural migration needs the shape a document is LEAVING and the shape it
// is arriving at. They live in different places and neither is optional:
//
//   - The OLD shape is only knowable from the CustomResourceDefinition
//     installed right now. After this pull request merges it is gone, which is
//     the entire reason this runs pre-merge in the cluster rather than in CI.
//   - The NEW shape belongs to the chart version being promoted TO, and the
//     cluster has not seen it yet. It comes from rendering that chart.
//
// The live CRD's own copy of the target version is a fallback and is marked as
// one. It is usually right -- a chart dropping v1beta1 while keeping v1 rarely
// reshapes v1 in the same release -- and "usually right" is exactly the sort of
// claim that has to be visible in a comment rather than assumed.

// schemaPair is both shapes for one kind, with provenance.
type schemaPair struct {
	// From is the version the documents are leaving; To is the target.
	From, To string
	Old, New structural.Schema
	// Note says where the target schema came from when it was not the chart,
	// or why there is none.
	Note string
}

// Complete reports whether structural analysis can be attempted. Either schema
// missing means the plain apiVersion swap ships as it did before -- correct as
// far as it goes, and honest about going no further.
func (s schemaPair) Complete() bool { return s.Old != nil && s.New != nil }

// helmTimeout bounds the chart render. Generous, because it pulls from a
// registry; bounded, because nothing here is worth a stuck triage.
const helmTimeout = 3 * time.Minute

// schemasFor gathers the two shapes for each dropped kind.
//
// Soft in every direction. No cluster reader, no helm on PATH, an artifact that
// is not a chart, a chart that ships no CRDs -- all produce an incomplete pair
// and a note, and the caller falls back to the swap alone.
func (t *Triage) schemasFor(ctx context.Context, p Promotion, drops []migrate.Dropped) map[string]schemaPair {
	out := map[string]schemaPair{}
	if t.Cluster == nil {
		return out
	}

	// Rendered once for the whole promotion: one helm invocation can answer
	// for every CRD the chart ships, and running it per kind would multiply a
	// registry pull by the number of findings.
	rendered, renderNote := t.renderTargetCRDs(ctx, p)

	for _, d := range drops {
		live := t.Cluster.CRD(ctx, d.CRD)
		pair := schemaPair{To: d.Target}
		// The version being left. Several may be dropped at once; take the
		// newest, which is the one a document is most likely to be written
		// against.
		for _, v := range migrate.PreferredOrder(d.Versions) {
			if sch, ok := live.Schemas[v]; ok {
				pair.From, pair.Old = v, structural.Schema(sch)
				break
			}
		}
		if pair.Old == nil {
			pair.Note = firstNonEmpty(live.Note,
				fmt.Sprintf("the cluster serves no schema for %s at %s", d.CRD, strings.Join(d.Versions, ", ")))
			out[d.CRD] = pair
			continue
		}

		if sch, ok := rendered[d.CRD][d.Target]; ok {
			pair.New = sch
		} else if sch, ok := live.Schemas[d.Target]; ok {
			pair.New = structural.Schema(sch)
			pair.Note = firstNonEmpty(renderNote, "") +
				fmt.Sprintf(" Target schema taken from the %s the cluster serves today, which predates this bump.", d.Target)
			pair.Note = strings.TrimSpace(pair.Note)
		} else {
			pair.Note = firstNonEmpty(renderNote,
				fmt.Sprintf("no schema for %s at %s could be found", d.CRD, d.Target))
		}
		out[d.CRD] = pair
	}
	return out
}

// renderTargetCRDs renders the chart at the version being promoted TO and
// returns every CRD schema in it, keyed by definition name then version.
//
// The value is structural.Schema rather than a third raw map: the runtime
// representation is identical, but the name says which of the three nesting
// levels is the schema and which two are keys, and it removes the cast every
// caller was making anyway.
//
// `helm template` rather than a registry client, and helm rather than a Go
// library, for the reason the gate's image already states: rendering has to
// match what the cluster's own Helm does, and the only thing guaranteed to do
// that is Helm.
func (t *Triage) renderTargetCRDs(ctx context.Context, p Promotion) (map[string]map[string]structural.Schema, string) {
	if strings.TrimSpace(p.Artifact) == "" || strings.TrimSpace(p.To) == "" {
		return nil, "the promotion names no chart to render"
	}
	if _, err := exec.LookPath("helm"); err != nil {
		return nil, "helm is not on PATH in this image, so the target schema could not be rendered"
	}

	// The artifact is parsed by the upstream resolver's parser and turned into
	// helm arguments by the gate's -- one owner each for "what shape is this
	// artifact" and "how does that become a helm invocation". This file used to
	// answer the second question itself and got it wrong for classic Helm
	// repositories; gate.HelmChartArgs carries the account of that.
	ref, chart := upstream.ParseArtifact(p.Artifact)
	chartArgs, err := gate.HelmChartArgs(ref, chart)
	if err != nil {
		return nil, err.Error()
	}
	args := append([]string{"template", "schema-probe"}, chartArgs...)
	args = append(args, "--version", p.To, "--include-crds", "--skip-tests")

	// helm is a SUBPROCESS. The egress transport cannot see inside it, so the
	// destination is checked and recorded here instead -- otherwise the one
	// outbound path that downloads an archive would be the one path with no
	// record.
	//
	// Only the repository is known in advance; helm follows the index to
	// wherever the archive is served, and that hop is invisible from here. The
	// log says so rather than implying this is the whole story.
	if host := hostOf(ref); host != "" {
		if rule, denied := t.Egress.Denied(host); denied {
			return nil, fmt.Sprintf("egress to %s is denied by policy (rule %q), so the target schema was not read", host, rule)
		}
		t.logf("outbound helm template %s (chart %s %s; it will follow the index to wherever the archive is served)",
			host, firstNonEmpty(chart, ref), p.To)
	}

	ctx, cancel := context.WithTimeout(ctx, helmTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "helm", args...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Sprintf("could not render %s %s (%s)", ref, p.To,
			firstLineOf(strings.TrimSpace(errb.String())))
	}
	return crdSchemasFromStream(out.String()), ""
}

// crdSchemasFromStream picks the CustomResourceDefinitions out of a rendered
// manifest stream.
func crdSchemasFromStream(stream string) map[string]map[string]structural.Schema {
	found := map[string]map[string]structural.Schema{}
	for _, part := range strings.Split(stream, "\n---") {
		if !strings.Contains(part, "CustomResourceDefinition") {
			continue
		}
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Versions []struct {
					Name   string `json:"name"`
					Schema struct {
						OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
					} `json:"schema"`
				} `json:"versions"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(part), &obj); err != nil {
			// A document that does not parse is skipped, not fatal: a rendered
			// stream can carry test hooks and comments that are not objects.
			continue
		}
		if obj.Kind != "CustomResourceDefinition" || obj.Metadata.Name == "" {
			continue
		}
		byVersion := map[string]structural.Schema{}
		for _, v := range obj.Spec.Versions {
			if len(v.Schema.OpenAPIV3Schema) > 0 {
				byVersion[v.Name] = v.Schema.OpenAPIV3Schema
			}
		}
		if len(byVersion) > 0 {
			found[obj.Metadata.Name] = byVersion
		}
	}
	return found
}

// hostOf is the host of a chart reference, however it is written.
func hostOf(ref string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(ref, "oci://"), "https://"), "http://")
	if i := strings.IndexAny(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
