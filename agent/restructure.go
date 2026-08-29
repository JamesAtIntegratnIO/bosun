package agent

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
	"github.com/JamesAtIntegratnIO/bosun/prompt"
	"github.com/JamesAtIntegratnIO/bosun/safepath"
	"github.com/JamesAtIntegratnIO/bosun/structural"
)

// The second half of a migration, for the bumps where swapping the apiVersion
// line is not the whole job.
//
// `migrate` rewrites `apiVersion: g/v1beta1` to `apiVersion: g/v1` and touches
// nothing else. That is exactly right when the two versions are compatible, and
// a silent corruption when they are not: a chart that moved `spec.store` to
// `spec.secretStoreRef.name` leaves a document that parses, applies, and has a
// field quietly pruned by the apiserver on the way in. The render is fine. The
// gate is green. The value is gone.
//
// Enumerating every upstream's structural changes is not possible, so this
// shows the model BOTH schemas and the document and asks it to translate. What
// makes that safe is not the prompt -- it is `structural.Validate`, which
// checks the OUTPUT: identity preserved, valid against the target schema, and
// every value present in the original or dictated by the schema itself.
//
// The model is a translator between two schemas it is shown. The harness is
// what makes that true.

// restructured is one document the model reshaped and the harness accepted.
type restructured struct {
	Path      string
	Kind      string
	Name      string
	Notes     string
	Diff      string
	Lost      []string
	Respelled []string
	Reasons   []structural.Finding
}

// refused is one document the harness would not write.
type refused struct {
	Path     string
	Kind     string
	Name     string
	Why      []string
	Findings []structural.Finding
}

// restructureResult is what a whole pass did.
type restructureResult struct {
	Applied []restructured
	Refused []refused
	// Skipped explains, per definition, why a document was not analysed at
	// all: no schema pair, no model that can restructure, the cap. Never
	// silent -- a structural change nobody looked for is the failure this
	// exists to end.
	Skipped []string
	// Provenance says where a schema came from when it was not the obvious
	// place. ADR 0007 promises the live-CRD fallback is "labelled as one in the
	// comment", and it was not: the note was attached to the pair and then only
	// surfaced when the pair was INCOMPLETE -- which is exactly when the
	// fallback had not been used. A fallback that works is silent, which is the
	// wrong way round.
	Provenance []string
	// ModelCalls counts model calls, so a comment can say honestly whether a
	// model was involved at all. Named for what it counts: `Called` read as a
	// boolean beside Applied, Refused and Skipped.
	ModelCalls int
}

func (r *restructureResult) touched() bool {
	return r != nil && (len(r.Applied) > 0 || len(r.Refused) > 0)
}

// restructureAll analyses every file the deterministic swap rewrote.
//
// TOP-LEVEL DOCUMENTS ONLY, and that is a real limit rather than an oversight.
// `migrate` deliberately reaches further -- a manifest nested in an
// `extraObjects:` list or embedded in a block scalar renders into a real object
// and breaks at apply exactly like a document does, and 13 of 27 declaring
// files in the incident this was built from held the declaration somewhere
// other than the top level. Swapping a value on one line inside such a file is
// safe. REPLACING a document inside one is not: it means re-serialising a
// values file whose every remaining line would move. Those are reported as
// skipped and reach a human.
func (t *Triage) restructureAll(ctx context.Context, root string, drops []migrate.Dropped,
	pairs map[string]schemaPair, files []string, maxDocs int) *restructureResult {

	res := &restructureResult{}
	rs, ok := t.LLM.(llm.Restructurer)

	// Index the findings by the apiVersion a swapped document now carries, so
	// a file can be judged without re-deriving which migration it belonged to.
	type target struct {
		crd        string
		kind       string
		pair       schemaPair
		apiVersion string
	}
	byAPIKind := map[string]target{}
	for _, d := range drops {
		pair := pairs[d.CRD]
		av := d.Group + "/" + d.Target
		byAPIKind[av+"/"+d.Kind] = target{crd: d.CRD, kind: d.Kind, pair: pair, apiVersion: av}
		switch {
		case !pair.Complete():
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"`%s`: no structural check -- %s", d.CRD,
				firstNonEmpty(pair.Note, "both schemas were needed and one could not be read")))
		case pair.Note != "":
			// Complete, but not from where it would normally come. Worth saying
			// out loud: a target schema taken from what the cluster serves
			// TODAY predates the bump, so it can miss a field the new chart
			// version added -- and a check that silently used the old shape
			// would report a clean document with more confidence than it earned.
			res.Provenance = append(res.Provenance, fmt.Sprintf("`%s`: %s", d.CRD, pair.Note))
		}
	}
	if !ok {
		// Named once rather than per document: a provider without the
		// capability is a deployment fact, not a finding about this bump.
		res.Skipped = append(res.Skipped, fmt.Sprintf(
			"`%s` cannot propose a document migration, so only the apiVersion was swapped", t.LLM.Name()))
	}

	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		// Contained even though these paths came from the migration rather
		// than from a model: the migration got them from a walk of the
		// checkout, and a walk reports links as readily as it reports files.
		full, err := safepath.Resolve(root, rel)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("`%s` was refused: %v", rel, err))
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			// Never silent: the struct has a channel for exactly this, and a
			// document nobody looked at is the failure this whole pass exists
			// to end.
			res.Skipped = append(res.Skipped, fmt.Sprintf("`%s` could not be read: %v", rel, err))
			continue
		}
		chunks := splitDocuments(string(data))
		changed := false

		for i, chunk := range chunks {
			var head struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
				Metadata   struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(chunk.body), &head); err != nil || head.Kind == "" {
				continue
			}
			tgt, want := byAPIKind[head.APIVersion+"/"+head.Kind]
			if !want || !tgt.pair.Complete() {
				continue
			}

			var doc map[string]any
			if err := yaml.Unmarshal([]byte(chunk.body), &doc); err != nil {
				continue
			}
			findings := structural.Check(doc, tgt.pair.New)
			// Every refusal below names the same document and carries the same
			// findings; only the reason differs. Written out six times as a
			// positional literal, one of them had already lost its Findings.
			refuse := func(why string, a ...any) {
				res.Refused = append(res.Refused, refused{
					Path: rel, Kind: head.Kind, Name: head.Metadata.Name,
					Why: []string{fmt.Sprintf(why, a...)}, Findings: findings,
				})
			}
			if len(findings) == 0 {
				// The swap was the whole job. No model call, which is the
				// common case and the one that must stay free.
				continue
			}
			if !ok {
				refuse("no model available to propose a migration")
				continue
			}
			if res.ModelCalls >= maxDocs {
				refuse("the per-pull-request limit of %d document migrations was reached", maxDocs)
				continue
			}

			res.ModelCalls++
			userPrompt := structural.Prompt(rel, chunk.body,
				tgt.pair.From, tgt.pair.To, tgt.pair.Old, tgt.pair.New, findings)
			m, err := rs.Restructure(ctx, prompt.Restructure, userPrompt)
			if err != nil || m == nil {
				refuse("the model could not be reached (%v)", err)
				continue
			}

			var proposed map[string]any
			if err := yaml.Unmarshal([]byte(m.Document), &proposed); err != nil {
				refuse("the proposal is not a YAML document (%v)", err)
				continue
			}
			verdict := structural.Validate(doc, proposed, tgt.apiVersion, tgt.pair.New)
			if !verdict.OK() {
				// The only one with several reasons: the validator reports
				// every way the proposal failed, not the first.
				res.Refused = append(res.Refused, refused{
					Path: rel, Kind: head.Kind, Name: head.Metadata.Name,
					Why: verdict.Refusals, Findings: findings,
				})
				continue
			}

			// Re-serialised from the validated map rather than written back as
			// the model's own text. The bytes that land are then a function of
			// a structure the harness checked, not of a string it was handed.
			out, err := yaml.Marshal(proposed)
			if err != nil {
				refuse("could not serialise the validated document (%v)", err)
				continue
			}
			chunks[i].body = string(out)
			changed = true
			res.Applied = append(res.Applied, restructured{
				Path: rel, Kind: head.Kind, Name: head.Metadata.Name,
				Notes: m.Notes, Diff: structural.Diff(chunk.body, string(out)),
				Lost: verdict.Lost, Respelled: verdict.Respelled, Reasons: findings,
			})
		}

		if changed {
			if err := os.WriteFile(full, []byte(joinDocuments(chunks)), 0o644); err != nil {
				res.Refused = append(res.Refused, refused{Path: rel,
					Why: []string{fmt.Sprintf("could not write the file (%v)", err)}})
			}
		}
	}
	return res
}

// document is one top-level YAML document plus the separator that preceded it,
// so a file round-trips byte for byte when nothing changed.
type document struct {
	sep  string
	body string
}

func splitDocuments(s string) []document {
	lines := strings.Split(s, "\n")
	var out []document
	cur := document{}
	var body []string
	flush := func() {
		cur.body = strings.Join(body, "\n")
		out = append(out, cur)
		body = nil
	}
	for _, l := range lines {
		if strings.TrimRight(l, " \t") == "---" || strings.HasPrefix(l, "--- ") {
			flush()
			cur = document{sep: l}
			continue
		}
		body = append(body, l)
	}
	flush()
	return out
}

func joinDocuments(docs []document) string {
	var b strings.Builder
	for i, d := range docs {
		if i > 0 || d.sep != "" {
			if i > 0 {
				b.WriteString("\n")
			}
			if d.sep != "" {
				b.WriteString(d.sep)
				b.WriteString("\n")
			}
		}
		b.WriteString(d.body)
	}
	return b.String()
}
