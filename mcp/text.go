package mcp

import (
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
// Text sanitized to harmlessness does not exist. What a tag buys is that a
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
)

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
const (
	maxSummary = 500
	maxDetail  = 4000
	maxNote    = 1000
	maxCommand = 4000
)

// say builds a Text, capped and trimmed.
//
// Every free-text field on this surface goes through here, so a field added
// later without a cap is a field that does not compile into a Text at all.
func say(s string, origin Origin, cap int) Text {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= cap {
		return Text{Text: s, Origin: origin}
	}
	// Cut on a rune boundary. Cutting on a byte would produce invalid UTF-8,
	// which encoding/json silently replaces with U+FFFD -- a corruption that
	// looks like the upstream text was already broken.
	runes := []rune(s)
	return Text{Text: strings.TrimSpace(string(runes[:cap])), Origin: origin, Truncated: true}
}

// redactTree removes this process's credentials from every string in a decoded
// JSON tree, in place where it can and by rebuilding where it cannot.
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
		return redact.Text(t)
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
			out[redact.Text(k)] = redactTree(val)
		}
		return out
	default:
		return v
	}
}
