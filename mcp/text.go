package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// Origin says who wrote a piece of free text.
//
// The rule this exists to make checkable: instructions in a result are bosun's
// own or absent. Bosun's results land in agents holding tools bosun refuses
// for itself -- a shell, a file-edit tool, a path to somebody's repository --
// so a hostile chart name or upstream release note does not need to jailbreak
// bosun's model. It only needs to be delivered by it to a better-armed one.
//
// Tagging does not make hostile text harmless and is not offered as doing so.
// Text sanitised to harmlessness does not exist. What a tag buys is that a
// careful client can fence what bosun did not author, and that the fields it
// must never fence -- a remedy -- are distinguishable by type rather than by
// convention.
type Origin string

const (
	// OriginBosun: every byte was written by this process, from its own
	// literals and from values it validated. The only origin a client should
	// ever treat as instructions.
	OriginBosun Origin = "bosun"

	// OriginCluster: composed by bosun, quoting text read from the cluster --
	// a Kargo condition message, a promotion's failure reason, an
	// AnalysisRun's last word. Bosun wrote the sentence; it did not write
	// every phrase in it.
	OriginCluster Origin = "bosun-quoting-cluster"

	// OriginRepository: composed by bosun, quoting text read from the
	// repository -- a file path, a promotion target's key.
	OriginRepository Origin = "bosun-quoting-repository"

	// OriginChart: a name or a value a rendered chart chose. Object names,
	// Application names, values keys, chart versions.
	//
	// The origin this surface is most careful about, and the one that
	// justifies tagging identities rather than only prose. A Kargo Stage name
	// reached an apiserver, so it is an RFC1123 subdomain and there is no
	// sentence in it for anything to hide inside. A chart-rendered name never
	// reached one: `helm template` does not apply, so `metadata.name` is
	// whatever the template wrote, newlines and backticks included. The gate
	// already publishes its migration contract twice for exactly this reason
	// -- once as prose a person reads, once as a machine-readable block a
	// repair executes -- because half of the human bullet is a name a chart
	// chose, and a chart must not be able to write an instruction.
	OriginChart Origin = "bosun-quoting-chart"

	// OriginHelm: what helm said when it would not render a chart, verbatim.
	//
	// Its own origin rather than OriginChart because bosun composed no part
	// of it: a rendered name arrives inside a sentence this process wrote,
	// and this is a whole string somebody else's program produced, quoted
	// because it is the only evidence there is. A chart that will not render
	// leaves nothing to diff, so the reader cannot look this up anywhere else.
	OriginHelm Origin = "bosun-quoting-helm"

	// OriginValidator: what the schema validator said about one rejected
	// manifest, verbatim.
	OriginValidator Origin = "bosun-quoting-validator"

	// OriginRender: composed by bosun, quoting whatever refused a read the
	// gate wanted -- helm at the revision the change starts from, a
	// repository scan that failed, an ApplicationSet that would not expand.
	//
	// The coverage the run lost, in other words, which is a different claim
	// from a finding: a finding is "we looked and this is wrong", these are
	// "we did not look here".
	OriginRender Origin = "bosun-quoting-render"

	// OriginLabel: a label standing on a pull request, as the git host
	// reports it.
	//
	// Not folded into OriginAuthor, and not published as bosun's own even
	// though bosun writes some of them: an attempt label is bosun's, a
	// `needs-human` is a maintainer's, and a repository where anybody can
	// label is a repository where these bytes are anybody's. One origin for
	// the field, saying the weakest true thing about it, rather than a
	// per-label guess a hostile label could imitate by choosing bosun's
	// prefix.
	OriginLabel Origin = "bosun-quoting-pull-request-label"

	// OriginAuthor: written by whoever opened the pull request. Its title,
	// today.
	//
	// Worth its own origin even though it is the most obviously untrusted
	// string on the surface, because it is also the one a client is most
	// tempted to render as a heading. Anybody who can open a pull request
	// against the gated repository can choose these bytes.
	OriginAuthor Origin = "bosun-quoting-pull-request-author"

	// OriginModel: a whole sentence bosun's own model wrote -- its account
	// of why it stopped short of a mechanical fix and asked for a human.
	//
	// The sharpest edge on this surface, and its own origin for that reason.
	// Every other untrusted string here is an identifier or a program's
	// output: a name a chart rendered, helm's refusal, a validator's verdict,
	// a title somebody typed. This one is prose, written by a model,
	// explaining something -- the shape an injected instruction wants to
	// wear, and the shape a client is most likely to render as though a
	// colleague had written it.
	//
	// Not OriginBosun: bosun did not write it, and the origin claiming every
	// byte is bosun's is the one a client is told it may treat as
	// instructions. Not OriginCluster, OriginRepository or OriginChart: each
	// of those is bosun's own sentence with somebody's identifier quoted
	// inside it, and this is somebody else's sentence entire. Not OriginHelm
	// or OriginValidator either, close as they look -- a program that refuses
	// a chart produces the same string every time and a reader can go and
	// reproduce it, where this was written once, by a model, under whatever
	// influence reached its prompt.
	OriginModel Origin = "bosun-quoting-model"
)

// There is deliberately no origin for an upstream release note.
//
// Not an oversight: no tool registered here can carry one. Release notes are
// read on the explain path, which turns a red gate into a sentence a person
// reads on the pull request, and nothing on that path is published as a tool
// result yet. The tag lands with the tool that carries one, because a constant
// declared for a field nothing fills is a promise to a client that this
// surface cannot keep.
//
// What a client fences on is the shape rather than the list -- `bosun`, or
// `bosun-quoting-` something -- precisely so that the vocabulary can grow with
// the tools without a client's default branch quietly swallowing a kind of
// text it has never seen.

// Text is one free-text field and where its content came from.
//
// A struct rather than a string with a sibling field, because the two must
// never be separable: a client that picked up the text and dropped the origin
// would be back where this started, and a schema where that is possible is one
// where it will happen.
type Text struct {
	Text   string `json:"text"`
	Origin Origin `json:"origin"`
	// Truncated says the text was cut to fit a cap.
	//
	// On the wire rather than implied by a trailing ellipsis, because a client
	// deciding whether to fetch the rest from somewhere else needs to know,
	// and because an upstream note that happens to end in "..." would
	// otherwise be indistinguishable from one bosun cut.
	Truncated bool `json:"truncated,omitempty"`
}

// The caps, one per role, and every one of them generous.
//
// A free-text field that can carry third-party content is a field a large
// upstream note can flood a client's context with, and the client's context is
// the resource this surface can spend without ever seeing the bill. The
// numbers are set where a real value has never come close: the longest detail
// this repository's own sweeps produce is a few hundred characters.
//
// A remedy has none, and that is not an omission. It is composed by bosun from
// segments each bounded by their own grammar, so it cannot run away; and a cap
// on it would have to either truncate -- producing half a command somebody
// could run -- or drop it, which on this surface means something specific.
// An absent remedy says "no remedy exists for this finding", and a cap that
// quietly produced one would make that sentence untrue in a case no client
// could detect.
//
// Lists are deliberately not capped by length, only their entries. A verdict's
// consumer files are the manifests a repair has to move, so a list cut to
// twenty would understate a migration to the one reader that acts on it, and
// would do so in the case that needs the number most. The bound on those is
// upstream and real: they are the files a repository scan found, and a
// repository with ten thousand of them has a problem this surface should
// report rather than hide. If a real install ever floods a client from here,
// the answer is a count beside a truncated list rather than a quiet cut.
const (
	maxSummary = 500
	maxDetail  = 4000
	maxNote    = 1000
	// maxName bounds an identity: an object name, a values key, a file path,
	// a chart version. 253 is the longest legal Kubernetes object name, and
	// the gate's own subjects wrap one in a sentence -- `Kind/name in
	// namespace` -- so the ceiling is a small multiple of it rather than the
	// number itself. Anything longer is not a name.
	maxName = 1024
	// maxTitle bounds a pull request's title, generously: every git host caps
	// one far below this, and the cap is here because none of them promises to.
	maxTitle = 500
	// maxReason bounds a model's account of why it stopped. The real ones are a
	// sentence or two, and the cap is here because nothing upstream makes them
	// so: a model's output has no grammar bounding its length, and this is the
	// one field on the surface whose bytes a model chose freely.
	maxReason = 1000
)

// say builds a Text, capped and trimmed.
//
// Every free-text field on this surface goes through here, so a field added
// later without a cap is a field that does not compile into a Text at all.
func say(s string, origin Origin, limit int) Text {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= limit {
		return Text{Text: s, Origin: origin}
	}
	// Cut on a rune boundary. Cutting on a byte would produce invalid UTF-8,
	// which encoding/json silently replaces with U+FFFD -- a corruption that
	// looks like the upstream text was already broken.
	runes := []rune(s)
	return Text{Text: strings.TrimSpace(string(runes[:limit])), Origin: origin, Truncated: true}
}

// redacted is a value with every string in it run through the process
// redactor, ready to be encoded.
//
// The round trip through JSON is what makes it total: a reflection walk would
// have to rebuild every struct field by field, and a walk that missed one
// would be a control with a gap in exactly the place nobody looked. Encoding
// once and redacting the decoded tree covers whatever the type happens to be,
// today and after somebody adds a field.
//
// It is called before a result is embedded anywhere, not only on the way out.
// See callTool for why: a value that is encoded into a string and then encoded
// again is escaped twice, and a credential with a newline in it no longer
// matches its own bytes by the time it reaches the wire.
func redacted(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var tree any
	dec := json.NewDecoder(bytes.NewReader(raw))
	// json.Number keeps the round trip honest: without it every number becomes
	// a float64, and an age in seconds comes back in exponential notation once
	// it passes a million.
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	return redactTree(tree), nil
}

// redactTree scours every string in a decoded JSON tree -- this process's
// credentials out, HTML comment delimiters broken -- in place where it can and
// by rebuilding where it cannot.
//
// Keys as well as values. Nothing on this surface puts a credential in a key
// today and nothing enumerates what a future result type might; a walk that
// covered half the tree would be a control with a documented gap in it.
//
// json.Number is passed through untouched: it is a string type, and running a
// number through a text substitution is how a report says a Stage has been
// wedged for ***4 seconds.
func redactTree(v any) any {
	switch t := v.(type) {
	case string:
		return scour(t)
	case json.Number:
		return t
	case []any:
		for i := range t {
			t[i] = redactTree(t[i])
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[scour(k)] = redactTree(val)
		}
		return out
	default:
		return v
	}
}

// scour is the two things done to every string on the way out: this process's
// credentials removed, and the delimiters of an HTML comment broken.
//
// Deliberately not called anything built on "sanitise". Neither pass makes
// hostile text harmless, and this surface's whole claim about such text is
// that it is labelled rather than cleaned; a name promising otherwise would be
// read by the next person as the guarantee this package explicitly does not
// offer.
func scour(s string) string { return declaw(redact.Text(s)) }

// declaw breaks the HTML comment delimiters in a string.
//
// # What this stops
//
// The gate keeps its memory inside its own pull-request comment. There is no
// database, and the only per-pull-request storage a git host offers is the
// comment, so the last verdict, the head commit it judged, and the migration a
// repair is to perform all travel as HTML comments -- invisible to a reader,
// read back by the next gate run, and acted on.
//
// Which makes them forgeable by anything that can get bytes into that comment.
// A client of this surface is one of those things: it reads a verdict here and
// writes prose onto the pull request, and if a chart-rendered object name in
// what it read carried `<!-- gitops-gate:verdict 0 ... -->`, the client
// publishes a verdict the gate never reached, on a commit it never judged.
// Nothing in the chain is compromised; each link does its job.
//
// So the delimiters are broken here, at the one place where a byte reaches the
// wire, and a client that relays bosun's text cannot become the relay.
//
// # Why the delimiters and not the stamps
//
// A list of today's stamps -- head, verdict, was, dropped, blockers, and the
// report marker -- is a list that goes stale the first time somebody adds a
// seventh, and the symptom of a stale one is nothing at all. The grammar under
// all of them is the HTML comment, there is exactly one spelling of its
// opener, and breaking that cannot fall behind a stamp nobody told this file
// about. mcp_stamps_test.go is what proves the coverage, and it derives the
// stamps from the packages that publish them rather than listing them here.
//
// # Why it is loud
//
// Replaced with a sentence rather than deleted, because a chart or a title
// containing an HTML comment is worth a reader's eyes -- there is no innocent
// reason for one to be in an object's name -- and a silently trimmed string is
// one nobody can look up in the chart that produced it. The same argument
// gate.Inline makes where it escapes a backtick visibly.
func declaw(s string) string {
	if !strings.Contains(s, "<!--") && !strings.Contains(s, "-->") {
		return s
	}
	s = strings.ReplaceAll(s, "<!--", commentOpener)
	return strings.ReplaceAll(s, "-->", commentCloser)
}

// What a broken delimiter is replaced with. Neither is a delimiter, and both
// say what happened.
const (
	commentOpener = "[bosun removed an html comment opener]"
	commentCloser = "[bosun removed an html comment closer]"
)
