package agent

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/prompt"
	"github.com/JamesAtIntegratnIO/bosun/safepath"
	"github.com/JamesAtIntegratnIO/bosun/structural"
	"github.com/JamesAtIntegratnIO/bosun/valuesmigrate"
)

// The repair for a chart the repository has outgrown.
//
// A bump whose new version refuses the values this repository has been setting
// for two years does not render at all: helm checks `values.schema.json`
// before it templates anything. That is the loudest failure a bump has and the
// one with the most exact evidence — the offending paths are named — and until
// ADR 0013 it was also the one nothing could act on, because removing a key,
// renaming a key and adding a key are three operations `edits` has no way to
// express.
//
// The shape is ADR 0007's, with the write changed. The model is shown the
// values and the chart's new schema and returns a whole document;
// `structural.ValidateValues` decides whether it may be used at all; and then
// the difference between the two documents is turned into a plan of key
// operations and applied one line at a time, so the file keeps its comments
// and a three-key change reads as three lines.
//
// And one guarantee the manifest path cannot have: the chart is rendered with
// the proposal before anything is written. A migrated manifest is judged by a
// schema walk; this is judged by the program that refused it.

// valuesRepair is what one Application's migration did.
type valuesRepair struct {
	App    string
	Chart  string
	From   string
	To     string
	Anchor string
	Notes  string
	Ops    []valuesmigrate.Op
	// Lost are values that did not come across. Named in full, every time.
	// The one that should have been renamed and was not looks exactly like
	// the ones the chart genuinely stopped reading, and a reader who is shown
	// all of them can tell which is which; a reader shown none cannot.
	Lost []string
}

// refusedValues is one Application the harness would not rewrite.
type refusedValues struct {
	App   string
	Chart string
	Why   []string
}

type valuesResult struct {
	Applied    []valuesRepair
	Refused    []refusedValues
	ModelCalls int
}

// repairValues migrates the values of every Application the gate could not
// render, or writes nothing at all.
//
// All or nothing, for the reason repairDropped states about a partial push: a
// repository holding one repaired Application and one broken one, with a gate
// that has gone green because the broken one no longer blocks, is a worse
// outcome than the red gate it started from.
func (t *Triage) repairValues(ctx context.Context, pr *gitprovider.PullRequest,
	root string, targets []gate.Unrenderable, live *liveFacts, attempt int) error {

	// The same scope the edit path uses, and derived the same way: the diff
	// this pull request actually holds, read from git, rather than the file
	// list the promotion body claims. It is the wider of the two candidate
	// lists below and the one a caller could otherwise inflate.
	scope, err := t.scopeFor(ctx, root, pr)
	if err != nil {
		t.say(ctx, pr, "escalated: could not establish which files this pull request changes")
		return t.escalate(ctx, pr, fmt.Sprintf(
			"Nothing was written: %v. The values a migration may rewrite are read from the branch.", err), nil)
	}

	res := &valuesResult{}
	// Written into a copy of the tree's files, not the tree, until every
	// Application has passed. A file is only touched when nothing refused.
	pending := map[string][]byte{}

	for _, u := range targets {
		repair, err := t.migrateOneApplication(ctx, root, u, scope, pending, res)
		if err != nil {
			res.Refused = append(res.Refused, refusedValues{
				App: u.Head.App, Chart: u.Head.Chart, Why: []string{err.Error()},
			})
			continue
		}
		res.Applied = append(res.Applied, *repair)
	}

	if len(res.Refused) > 0 || len(res.Applied) == 0 {
		t.say(ctx, pr, "escalated: the values migration was refused for %s",
			countOf(len(res.Refused), "Application"))
		return t.escalateInformed(ctx, pr,
			renderValuesMigration(t.brand(), t.LLM.Name(), res,
				"**Needs a human.** The chart no longer accepts the values this repository sets, "+
					"and the migration that would fix it was refused, so nothing was pushed."),
			nil, nil, nil, live)
	}

	for rel, data := range pending {
		full, err := safepath.Resolve(root, rel)
		if err != nil {
			return t.escalate(ctx, pr, fmt.Sprintf("could not write %s: %v", rel, err), nil)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return t.escalate(ctx, pr, fmt.Sprintf("could not write %s: %v", rel, err), nil)
		}
	}

	msg := fmt.Sprintf("fix(values): migrate %s to the chart version this change moves to\n\n"+
		"The new chart's values.schema.json refuses settings this repository still makes.\n"+
		"Proposed by %s, validated against that schema, and proved by rendering the chart\n"+
		"with the result before anything was written.\n",
		countOf(len(res.Applied), "Application"), t.LLM.Name())
	if err := t.reserveAttempt(ctx, pr, attempt); err != nil {
		return t.escalate(ctx, pr, err.Error(), nil)
	}
	if err := t.Git.PushFix(ctx, pr, root, msg); err != nil {
		return t.escalate(ctx, pr, fmt.Sprintf("Could not push the values migration: %v", err), nil)
	}
	t.say(ctx, pr, "migrated the values of %s%s", countOf(len(res.Applied), "Application"), t.attemptSuffix(attempt))
	return t.Git.Comment(ctx, pr.Number, renderValuesMigration(t.brand(), t.LLM.Name(), res,
		fmt.Sprintf("Pushed a values migration to `%s`%s. The gate will re-run and re-render.",
			pr.Branch, t.attemptSuffix(attempt))))
}

// migrateOneApplication is the whole harness for one Application, in the order
// the checks get cheaper to be wrong about.
//
// pending accumulates the files this pass would write, so a second Application
// whose values live in the same file sees the first one's work; nothing
// reaches the tree until every Application has passed.
func (t *Triage) migrateOneApplication(ctx context.Context, root string, u gate.Unrenderable,
	scope []string, pending map[string][]byte, res *valuesResult) (*valuesRepair, error) {

	rs, ok := t.LLM.(llm.Restructurer)
	if !ok {
		return nil, fmt.Errorf("%s cannot propose a values migration", t.LLM.Name())
	}
	if res.ModelCalls >= t.maxRestructured() {
		return nil, fmt.Errorf("the per-pull-request limit of %d migrations was reached", t.maxRestructured())
	}

	work, err := os.MkdirTemp("", "values-migration-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()

	cfg := &gate.Config{Egress: t.Egress, Log: t.logf}
	target, err := gate.ChartValuesSchema(ctx, cfg, work, u.Head)
	if err != nil {
		return nil, fmt.Errorf("could not read the values schema %s %s declares: %v", u.Head.Chart, u.Head.Version, err)
	}
	if target == nil {
		// The render failed for something other than the values. There is no
		// schema to migrate towards, and inventing the shape of one is the
		// opposite of what this path is for.
		return nil, fmt.Errorf("%s %s ships no values.schema.json, so nothing here can say which settings "+
			"it stopped accepting; helm refused the render for another reason", u.Head.Chart, u.Head.Version)
	}

	original, err := gate.ApplicationValues(root, u.Head)
	if err != nil {
		return nil, fmt.Errorf("could not read the values this Application renders with: %v", err)
	}
	findings := structural.Check(original, structural.Schema(target))
	if len(findings) == 0 {
		return nil, fmt.Errorf("the chart refused the render for a reason its own values schema does not "+
			"explain, so there is no migration to make: %s", firstLineOf(u.Reason))
	}
	// The boundary, decided before a model is asked anything. A required key
	// the schema does not dictate a value for has an answer only a person
	// holds, and asking for one is asking for an invention.
	if needs := structural.NeedsAnAuthor(original, structural.Schema(target)); len(needs) > 0 {
		return nil, fmt.Errorf("%s requires %s, and neither this repository's values nor the chart's own "+
			"schema says what it should be — that needs a person, not a migration",
			u.Head.Chart, strings.Join(quoteAll(needs), ", "))
	}

	// Best effort, and often absent: the version being left is exactly the one
	// that was permissive enough for these values to work, so it frequently
	// shipped no schema at all. When it is there it is what makes a rename
	// recognisable rather than guessable.
	oldRow := u.Head
	oldRow.Version = u.From
	oldWork, err := os.MkdirTemp("", "values-schema-old-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(oldWork) }()
	oldSchema, _ := gate.ChartValuesSchema(ctx, cfg, oldWork, oldRow)

	res.ModelCalls++
	m, err := rs.Restructure(ctx, prompt.ValuesMigration, structural.ValuesPrompt(
		u.Head.Chart, u.From, u.Head.Version, original,
		structural.Schema(oldSchema), structural.Schema(target), findings))
	if err != nil || m == nil {
		return nil, fmt.Errorf("the model could not be reached (%v)", err)
	}
	var proposed map[string]any
	if err := yaml.Unmarshal([]byte(m.Document), &proposed); err != nil {
		return nil, fmt.Errorf("the proposal is not a YAML document (%v)", err)
	}

	verdict := structural.ValidateValues(original, proposed, structural.Schema(target))
	if !verdict.OK() {
		return nil, fmt.Errorf("the proposal was refused: %s", strings.Join(verdict.Refusals, "; "))
	}

	// The proof. Not "these values fit a schema" but "this chart renders with
	// them", from the program that refused the ones in the tree.
	if err := gate.RendersWith(ctx, cfg, work, u.Head, proposed); err != nil {
		return nil, fmt.Errorf("the chart still does not render with the proposed values: %s",
			firstLineOf(err.Error()))
	}

	ops := valuesmigrate.Plan(original, proposed)
	if len(ops) == 0 {
		return nil, fmt.Errorf("the proposal changes nothing, and the chart refuses the values as they are")
	}

	files, err := t.valuesCandidates(root, u, scope, pending)
	if err != nil {
		return nil, err
	}
	anchor, err := valuesmigrate.Locate(files, ops, original)
	if err != nil {
		return nil, err
	}
	// The anchor has to be the whole of what this chart reads, or the check
	// below cannot mean anything. Values merged from several places would
	// leave this rewriting one of them and comparing the result against the
	// merge of all of them.
	held, err := subtreeOf(files[anchor.Path], anchor.Prefix)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(held, original) {
		return nil, fmt.Errorf("the values %s renders with are merged from more than %s, and a migration "+
			"that rewrites one of several sources cannot be checked against what helm would then read",
			u.Head.Chart, anchor)
	}

	updated, err := valuesmigrate.Apply(files[anchor.Path], anchor.Prefix, ops)
	if err != nil {
		return nil, err
	}
	// Read back rather than assumed. Every operation was a line edit, and the
	// question this answers is not "did each edit succeed" but "does the file
	// now hold the document the harness accepted", which is the only form of
	// the question a reviewer cares about.
	after, err := subtreeOf(updated, anchor.Prefix)
	if err != nil {
		return nil, fmt.Errorf("the migrated file no longer parses: %v", err)
	}
	if !reflect.DeepEqual(after, proposed) {
		return nil, fmt.Errorf("applying the migration to %s did not reproduce the values the harness "+
			"accepted, so nothing was written", anchor)
	}

	pending[anchor.Path] = updated
	return &valuesRepair{
		App: u.Head.App, Chart: u.Head.Chart, From: u.From, To: u.Head.Version,
		Anchor: anchor.String(), Notes: m.Notes, Ops: ops, Lost: verdict.Lost,
	}, nil
}

// valuesCandidates is every file this migration may be anchored in: the value
// files the gate itself named, and the files the promotion rewrote.
//
// Both, because the two repository shapes put the values in different places.
// An Application with `helm.valueFiles` names them and the gate carries the
// list; an addon inside a chart-of-charts has them inline on a generated
// Application, and the file they came from is one the promotion touched.
//
// The scope check is deliberately absent for the first list, the same
// exception repairDropped makes and for the same reason: those paths were
// named by the gate rather than by a model, and by definition the promotion
// did not touch them. The deny-list and the allowlist still hold.
func (t *Triage) valuesCandidates(root string, u gate.Unrenderable, scope []string,
	pending map[string][]byte) (map[string][]byte, error) {

	// Scope cleared on the policy and passed in beside it, because the two
	// lists answer to different rules here: a value file the gate named is in
	// by the gate's authority, and everything else is in only because this
	// pull request changed it.
	policy := t.Policy
	policy.Scope = nil

	seen := map[string]bool{}
	out := map[string][]byte{}
	add := func(rel string) {
		if rel == "" || seen[rel] {
			return
		}
		seen[rel] = true
		if reason := policy.Check(rel); reason != "" {
			return
		}
		if data, ok := pending[rel]; ok {
			out[rel] = data
			return
		}
		full, err := safepath.Resolve(root, rel)
		if err != nil {
			return
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return
		}
		out[rel] = data
	}
	for _, vf := range u.Head.ValueFiles {
		add(gate.StripValuesRef(vf))
	}
	for _, rel := range scope {
		add(rel)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no file this change may write to holds values for %s", u.Head.Chart)
	}
	return out, nil
}

// subtreeOf decodes a file and returns the mapping at a dotted prefix.
func subtreeOf(data []byte, prefix string) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if prefix == "" {
		return doc, nil
	}
	cur := doc
	for _, seg := range strings.Split(prefix, ".") {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is not a mapping in this file", prefix)
		}
		cur = next
	}
	return cur, nil
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, "`"+s+"`")
	}
	sort.Strings(out)
	return out
}
