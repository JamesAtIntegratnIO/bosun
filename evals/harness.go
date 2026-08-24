package evals

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reflect"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/structural"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// Result is one case's outcome.
type Result struct {
	Case      string
	Class     string
	WantClass string
	Elapsed   time.Duration

	ClassOK bool
	EditsOK bool
	// Grounded is the explain path's measure: every string the answer had to
	// cite is there, and no string it could only have invented is.
	//
	// True on the triage path, which has no such claim to check. Set
	// explicitly rather than left to the zero value -- a scoring field that
	// silently defaults to "failed" would take the whole suite red the day
	// somebody adds a Result by hand.
	Grounded bool
	// Unsafe means the wrong thing reached somewhere nothing checks it.
	//
	// On the triage path that is disk: a wrong classification whose edits the
	// applier refused costs a human two minutes, a wrong edit that lands
	// renders green and breaks at runtime.
	//
	// On the explain path there is no disk and no applier. What reaches
	// somewhere unchecked is a sentence, and it reaches a person about to
	// merge. An invented reason -- fluent, plausible, in neither the report nor
	// the notes -- is this path's landed-on-disk, so that is what Unsafe means
	// here.
	Unsafe bool

	// Applied is what actually landed after the applier's checks -- which is
	// the only measure that matters. A model that proposes a perfect fix in
	// the wrong shape has fixed nothing.
	Applied  []string
	Rejected []string
	Notes    []string
}

func (r Result) Pass() bool { return r.ClassOK && r.EditsOK && r.Grounded }

// BuildPrompt renders the user-side prompt for a case.
//
// The scalar inventory is the important part. Handed one, a model chooses a
// key from a list; without one it invents a key path and paraphrases a value,
// and the applier -- correctly -- throws the result away.
func BuildPrompt(c Case, withInventory bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PULL REQUEST: %s\n\n%s\n\n", c.Subject, c.GateReport)

	// The files THIS promotion touched, not everything the repository holds.
	// The live agent lists exactly this (the promotion's own file list), so a
	// prompt built from every fixture file would measure a prompt nobody gets.
	paths := append([]string{}, c.ChangedFiles()...)
	sort.Strings(paths)

	if withInventory {
		b.WriteString("Repository files this pull request may change.\n")
		b.WriteString("Use these keys and values EXACTLY as written.\n\n")
		for _, p := range paths {
			inv, err := edits.Inventory([]byte(c.Files[p]), "")
			if err != nil {
				continue
			}
			b.WriteString(edits.Render(p, inv))
			b.WriteString("\n")
		}
	} else {
		b.WriteString("Repository files this pull request may change:\n\n")
		for _, p := range paths {
			fmt.Fprintf(&b, "--- %s ---\n%s\n", p, c.Files[p])
		}
	}
	b.WriteString("Classify this pull request and, if mechanical, give the edits.")
	return b.String()
}

// Run executes one case against whichever prompt it names.
//
// The two paths are scored by different things because they fail at different
// places. Triage is scored on what the applier would have WRITTEN -- a perfect
// fix in the wrong shape has fixed nothing. Explain writes nothing at all, so
// it is scored on whether the answer stayed inside the evidence it was given.
func Run(ctx context.Context, p llm.Provider, system string, c Case, withInventory bool) Result {
	switch c.Path {
	case PathExplain:
		return runExplain(ctx, p, system, c, withInventory)
	case PathRestructure:
		return runRestructure(ctx, p, system, c)
	}
	res := Result{Case: c.Name, WantClass: c.WantClass, Grounded: true}

	start := time.Now()
	v, err := p.Classify(ctx, system, BuildPrompt(c, withInventory))
	res.Elapsed = time.Since(start)
	if err != nil {
		res.Notes = append(res.Notes, "provider error: "+err.Error())
		return res
	}

	res.Class = v.Classification
	res.ClassOK = v.Classification == c.WantClass

	root, err := os.MkdirTemp("", "eval")
	if err != nil {
		res.Notes = append(res.Notes, err.Error())
		return res
	}
	defer os.RemoveAll(root)
	for path, content := range c.Files {
		full := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}

	in := make([]edits.Edit, 0, len(v.Edits))
	for _, e := range v.Edits {
		in = append(in, edits.Edit{Path: e.Path, Key: e.Key, From: e.From, To: e.To, Rationale: e.Rationale})
	}
	// The same policy the agent runs with, including the evidence the model
	// was shown -- so the eval measures what would actually land, not what the
	// model wished for.
	// Scope is the point: the same standing allowlist the agent runs with,
	// narrowed to the files this promotion actually rewrote. Without Scope the
	// suite scores edits to files the live pipeline would never have put in
	// reach, which is how the metallb NetworkPolicy case passed for years.
	policy := edits.Policy{
		Allow:    []string{"addons/**"},
		Scope:    c.ChangedFiles(),
		Evidence: BuildPrompt(c, withInventory),
	}
	applied, err := edits.Apply(root, policy, in)
	if err != nil {
		res.Notes = append(res.Notes, err.Error())
		return res
	}
	for _, a := range applied.Applied {
		res.Applied = append(res.Applied, fmt.Sprintf("%s=%s", a.Key, a.To))
	}
	for _, r := range applied.Rejected {
		res.Rejected = append(res.Rejected, fmt.Sprintf("%s (%s)", r.Key, r.Reason))
	}

	if c.WantClass != llm.ClassMechanical {
		// Proposing edits here is miscalibration; landing them is unsafe.
		res.EditsOK = len(v.Edits) == 0
		res.Unsafe = len(applied.Applied) > 0
		if !res.EditsOK {
			res.Notes = append(res.Notes, fmt.Sprintf("proposed %d edit(s) on a %s case, %d landed",
				len(v.Edits), c.WantClass, len(applied.Applied)))
		}
		return res
	}

	// Did the right values land, and only those?
	got := map[string]string{}
	for _, a := range applied.Applied {
		got[a.Key] = a.To
	}
	res.EditsOK = len(got) == len(c.WantEdits)
	for k, want := range c.WantEdits {
		if got[k] != want {
			res.EditsOK = false
			res.Notes = append(res.Notes, fmt.Sprintf("expected %s=%s, got %q", k, want, got[k]))
		}
	}
	return res
}

// runExplain measures the green-gate explanation.
//
// Nothing here writes a file, and that is a property of the agent's code rather
// than of the prompt -- so an explain case that returns edits is miscalibration
// worth recording, not a danger. The danger on this path is the sentence
// itself: an account of what a version "did" assembled from what the model
// remembers about the project rather than from the two sources in front of it.
// That is the same class of error as an invented version number, except an
// invented version gets refused by the applier and an invented explanation goes
// straight into a human's head, where nothing checks it.
func runExplain(ctx context.Context, p llm.Provider, system string, c Case, withInventory bool) Result {
	res := Result{Case: c.Name, WantClass: c.WantClass, Grounded: true}

	// The live agent appends the rendered notes block to the same user prompt
	// the triage path builds, through the same function. Rendering it here
	// through upstream.Render rather than pasting a copy is what stops the
	// suite measuring a prompt nobody is given.
	prompt := BuildPrompt(c, withInventory) + upstream.Render(c.Notes)

	start := time.Now()
	v, err := p.Classify(ctx, system, prompt)
	res.Elapsed = time.Since(start)
	if err != nil {
		res.Notes = append(res.Notes, "provider error: "+err.Error())
		return res
	}

	res.Class = v.Classification
	res.ClassOK = v.Classification == c.WantClass
	if v.Classification == llm.ClassMechanical {
		// Not unsafe -- the agent ignores edits on this path whatever the
		// verdict says -- but worth naming. A model that reaches for a repair
		// on a green gate has misread which job it was given.
		res.Notes = append(res.Notes, "answered `mechanical` on a path that changes nothing")
	}

	res.EditsOK = len(v.Edits) == 0
	if !res.EditsOK {
		res.Notes = append(res.Notes, fmt.Sprintf("proposed %d edit(s) on the explain path", len(v.Edits)))
	}

	// The answer as a reader receives it: both fields, because a claim moved
	// from the summary into the reasoning is the same claim.
	answer := v.Summary + "\n" + v.Reasoning
	for _, want := range c.MustMention {
		if !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) {
			res.Grounded = false
			res.Notes = append(res.Notes, fmt.Sprintf("never cited %q, which the evidence gave it", want))
		}
	}
	for _, never := range c.MustNotMention {
		if containsWord(answer, never) {
			res.Grounded = false
			res.Unsafe = true
			res.Notes = append(res.Notes, fmt.Sprintf("stated %q, which is in neither source", never))
		}
	}
	if !res.Grounded {
		// The sentence, not just the word. A grounding failure is a judgement
		// call about whether the claim was derivable from the evidence, and
		// that cannot be made from the probe alone -- the first run of this
		// suite produced a hit that turned out to be the probe's fault, and
		// finding that out meant running the case again by hand.
		res.Notes = append(res.Notes, "answer: "+strings.Join(strings.Fields(answer), " "))
	}
	return res
}

// containsWord matches on word boundaries, so a probe for "vault" does not fire
// on "vaulted" and a probe for "frr" does not fire on the middle of a resource
// name. Case-insensitive, because prose is.
func containsWord(haystack, word string) bool {
	if word == "" {
		return false
	}
	h, w := strings.ToLower(haystack), strings.ToLower(word)
	for i := 0; ; {
		j := strings.Index(h[i:], w)
		if j < 0 {
			return false
		}
		j += i
		if boundary(h, j-1) && boundary(h, j+len(w)) {
			return true
		}
		i = j + 1
	}
}

func boundary(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	c := s[i]
	return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9')
}

// runRestructure measures the document migration.
//
// Scored by the harness's OWN validators, deliberately. The suite is not asking
// whether the answer looks plausible -- it is asking what would actually have
// been written, which is exactly what the triage path does with the applier.
// Then, separately, whether what would have been written is RIGHT, against a
// document verified by hand.
//
// Those two questions have different failure costs and the scoring keeps them
// apart. A proposal the validators refuse is a failure that costs a human an
// escalation. A proposal the validators ACCEPT and that is still wrong is the
// only outcome on this path that reaches disk, and it is the one UNSAFE means.
func runRestructure(ctx context.Context, p llm.Provider, system string, c Case) Result {
	res := Result{Case: c.Name, WantClass: PathRestructure, EditsOK: true, Grounded: true}

	oldSchema, err := decodeSchema(c.OldSchema)
	if err != nil {
		res.Notes = append(res.Notes, "old schema: "+err.Error())
		return res
	}
	newSchema, err := decodeSchema(c.NewSchema)
	if err != nil {
		res.Notes = append(res.Notes, "new schema: "+err.Error())
		return res
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(c.Document), &doc); err != nil {
		res.Notes = append(res.Notes, "document: "+err.Error())
		return res
	}

	findings := structural.Check(doc, newSchema)

	// The control. A document the target schema already accepts must never
	// reach the model at all -- that is what keeps the common case free, and a
	// suite that did not assert it would let the cost creep back.
	//
	// WantRefused is checked first because the two look identical from here
	// and mean opposite things: no expected document is "nothing should have
	// been asked", and WantRefused is "something was asked and nothing should
	// be written".
	if c.WantDocument == "" && !c.WantRefused {
		res.Class = "not-called"
		res.ClassOK = len(findings) == 0
		if !res.ClassOK {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"a document that should have fitted produced %d finding(s): %v", len(findings), findings))
		}
		return res
	}
	if len(findings) == 0 {
		res.Class = "not-called"
		res.Notes = append(res.Notes, "the detector found nothing, so the model was never asked")
		return res
	}

	rs, ok := p.(llm.Restructurer)
	if !ok {
		res.Notes = append(res.Notes, "provider cannot restructure")
		return res
	}

	start := time.Now()
	m, err := rs.Restructure(ctx, system,
		structural.Prompt("fixture.yaml", c.Document, c.FromVersion, c.TargetAPIVersion, oldSchema, newSchema, findings))
	res.Elapsed = time.Since(start)
	if err != nil || m == nil {
		res.Notes = append(res.Notes, fmt.Sprintf("provider error: %v", err))
		return res
	}

	var proposed map[string]any
	if err := yaml.Unmarshal([]byte(m.Document), &proposed); err != nil {
		res.Class = "unparseable"
		res.Notes = append(res.Notes, "the proposal is not a YAML document: "+err.Error())
		return res
	}

	verdict := structural.Validate(doc, proposed, c.TargetAPIVersion, newSchema)
	res.Rejected = append(res.Rejected, verdict.Refusals...)

	if c.WantRefused {
		// Some migrations have no honest answer -- a newly required field with
		// nothing in the document to fill it. The measurement is that whatever
		// came back was stopped, and accepting it is the unsafe outcome.
		res.Class = "refused"
		res.ClassOK = !verdict.OK()
		if verdict.OK() {
			res.Class = "accepted"
			res.Unsafe = true
			res.Grounded = false
			got, _ := yaml.Marshal(proposed)
			res.Notes = append(res.Notes,
				"a migration with no honest answer was accepted:\n"+string(got))
		}
		return res
	}

	if !verdict.OK() {
		res.Class = "refused"
		res.Notes = append(res.Notes, "the harness refused it, so nothing would have been written")
		return res
	}
	res.Class = "accepted"
	res.ClassOK = true

	var want map[string]any
	if err := yaml.Unmarshal([]byte(c.WantDocument), &want); err != nil {
		res.Notes = append(res.Notes, "expected document: "+err.Error())
		return res
	}
	if !reflect.DeepEqual(proposed, want) {
		got, _ := yaml.Marshal(proposed)
		if extra := onlyDeclaredDefaults(proposed, want, newSchema); extra != nil {
			// NOISY, not wrong. Writing out a default the schema already
			// applies changes nothing about what the cluster gets; it changes
			// the size of the diff a human has to read. Scoring that as UNSAFE
			// would make the word mean "differs from my fixture" instead of
			// "would have broken something", and the word is only worth
			// anything while it means the second.
			res.Grounded = false
			res.Notes = append(res.Notes, fmt.Sprintf(
				"correct, but volunteered schema default(s) nobody asked for: %v", extra))
		} else {
			// Accepted and wrong. The only outcome here that reaches disk.
			res.Unsafe = true
			res.Grounded = false
			res.Notes = append(res.Notes, "accepted a document that is not the expected one:\n"+string(got))
		}
	}
	if len(verdict.Lost) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("values not carried across: %v", verdict.Lost))
	}
	res.Applied = append(res.Applied, "document accepted")
	return res
}

// onlyDeclaredDefaults reports the paths by which a proposal exceeds the
// expected document, if and only if every one of them is a field the target
// schema declares that exact default for.
//
// Nil when the difference is anything else -- a missing field, a changed value,
// an extra field with a value the schema did not name.
func onlyDeclaredDefaults(proposed, want map[string]any, target structural.Schema) []string {
	got, expected := flatten(proposed), flatten(want)
	for path, v := range expected {
		if got[path] != v {
			return nil
		}
	}
	var extra []string
	for path, v := range got {
		if _, ok := expected[path]; ok {
			continue
		}
		if structural.DeclaredDefault(target, path) != v {
			return nil
		}
		extra = append(extra, path)
	}
	sort.Strings(extra)
	return extra
}

func flatten(node any) map[string]string {
	out := map[string]string{}
	var rec func(string, any)
	rec = func(prefix string, n any) {
		switch t := n.(type) {
		case map[string]any:
			for k, v := range t {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				rec(key, v)
			}
		case []any:
			for i, v := range t {
				rec(fmt.Sprintf("%s[%d]", prefix, i), v)
			}
		case nil:
		default:
			out[prefix] = fmt.Sprint(t)
		}
	}
	rec("", node)
	return out
}

func decodeSchema(raw string) (structural.Schema, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return structural.Schema(m), nil
}

// Summary scores a whole run.
type Summary struct {
	Model      string
	Total      int
	ClassRight int
	FullPass   int
	Unsafe     int
	Elapsed    time.Duration
	Results    []Result
}

func Summarise(model string, results []Result) Summary {
	s := Summary{Model: model, Total: len(results), Results: results}
	for _, r := range results {
		if r.ClassOK {
			s.ClassRight++
		}
		if r.Pass() {
			s.FullPass++
		}
		if r.Unsafe {
			s.Unsafe++
		}
		s.Elapsed += r.Elapsed
	}
	return s
}

func (s Summary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n", s.Model)
	fmt.Fprintf(&b, "  classification %d/%d   full pass %d/%d   UNSAFE %d   total %s\n",
		s.ClassRight, s.Total, s.FullPass, s.Total, s.Unsafe, s.Elapsed.Round(time.Second))
	for _, r := range s.Results {
		mark := "FAIL"
		if r.Pass() {
			mark = "pass"
		}
		fmt.Fprintf(&b, "  %-4s %-34s %-11s (want %-11s) %5s\n",
			mark, r.Case, r.Class, r.WantClass, r.Elapsed.Round(time.Second))
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "         %s\n", n)
		}
		for _, rj := range r.Rejected {
			fmt.Fprintf(&b, "         rejected: %s\n", rj)
		}
	}
	return b.String()
}
