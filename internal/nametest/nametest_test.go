package nametest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// legal is the shapes that must land inside the grammar; every other shape
// must land outside it, and absence from this map is what says so.
//
// The generator's own self-check, and not optional. Nothing that uses this
// package asks a shape whether its names are legal -- that is [Valid]'s job --
// so a shape that quietly started producing lawful names would make every
// property test built on it pass while asserting nothing. This is the one
// place the two are compared.
var legal = map[Shape]bool{
	ShapeLabel:     true,
	ShapeSubdomain: true,
	ShapeMaxLabel:  true,
}

func TestEveryShapeLandsOnTheSideOfTheGrammarItClaims(t *testing.T) {
	for _, c := range Corpus(1, 40, 4) {
		for _, name := range c.Names {
			if got := Valid(name); got != legal[c.Shape] {
				t.Fatalf("a %s name is Valid=%v, want %v: %q", c.Shape, got, legal[c.Shape], name)
			}
		}
	}
}

// A corpus reaches every shape and never repeats a name inside a case.
func TestACorpusCoversEveryShapeWithDistinctNames(t *testing.T) {
	seen := map[Shape]int{}
	for _, c := range Corpus(2, 3, 12) {
		seen[c.Shape]++
		if len(c.Names) != 12 {
			t.Fatalf("a %s case carries %d names, want 12", c.Shape, len(c.Names))
		}
		uniq := map[string]bool{}
		for _, n := range c.Names {
			if uniq[n] {
				t.Fatalf("a %s case repeats %q; a fixture that names two Stages the same "+
					"has one Stage in it", c.Shape, n)
			}
			uniq[n] = true
		}
	}
	for _, s := range Shapes {
		if seen[s] != 3 {
			t.Errorf("the corpus drew %d %s cases, want 3", seen[s], s)
		}
	}
}

// The same seed is the same corpus.
//
// The whole reason a counterexample is worth printing: a reader who reruns the
// test has to get the name that failed, not a fresh draw that passes.
func TestACorpusIsDeterministic(t *testing.T) {
	a, b := Corpus(7, 2, 5), Corpus(7, 2, 5)
	if len(a) != len(b) {
		t.Fatalf("two draws from one seed differ in length: %d and %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Shape != b[i].Shape || strings.Join(a[i].Names, "\x00") != strings.Join(b[i].Names, "\x00") {
			t.Fatalf("case %d differs between two draws from seed 7:\n%q\n%q", i, a[i], b[i])
		}
	}
	if c := Corpus(8, 2, 5); strings.Join(c[0].Names, "\x00") == strings.Join(a[0].Names, "\x00") {
		t.Error("two different seeds drew the same names, so the seed is not reaching the generator")
	}
}

// Mixed produces cases holding legal and illegal names at once, which is the
// only thing it is for.
func TestMixedHoldsBothSidesOfTheGrammarAtOnce(t *testing.T) {
	mixed := 0
	for _, names := range Mixed(3, 60, 8) {
		var good, bad int
		for _, n := range names {
			if Valid(n) {
				good++
			} else {
				bad++
			}
		}
		if good > 0 && bad > 0 {
			mixed++
		}
	}
	if mixed < 30 {
		t.Fatalf("only %d of 60 mixed cases held both a legal and an illegal name; a corpus "+
			"that never mixes cannot catch a builder that checks one piece and interpolates another", mixed)
	}
}

// Shapes is every Shape constant this file declares.
//
// Derived from the source rather than compared against a second written list,
// because a hand-written mirror of a const block is five entries with nothing
// forcing a sixth. Go cannot enumerate a type's constants at run time, so the
// enumeration is a walk of this package's own syntax tree -- the same move
// mcp/imports_test.go and redaction_test.go make for the same reason.
//
// A shape declared and left out of Shapes is never drawn. Nothing fails: the
// corpus is simply smaller, and a suite that quietly stopped covering newlines
// passes exactly like one that covers them.
func TestShapesNamesEveryShapeThisPackageDeclares(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "nametest.go", nil, 0)
	if err != nil {
		t.Fatalf("could not parse nametest.go: %v", err)
	}

	declared := map[Shape]string{}
	for _, d := range file.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Shape" {
				continue
			}
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not declared as a string literal, so this walk cannot read it", name.Name)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: unreadable value %s", name.Name, lit.Value)
				}
				declared[Shape(v)] = name.Name
			}
		}
	}

	// The self-check, and not optional: a walk that found nothing compares two
	// empty sets and reads exactly like a pass.
	if len(declared) < 10 {
		t.Fatalf("the walk found only %d Shape constants; it is reading the wrong file and is "+
			"proving nothing", len(declared))
	}

	listed := map[Shape]bool{}
	for _, s := range Shapes {
		if listed[s] {
			t.Errorf("Shapes names %q twice, so a corpus draws it twice as often as the rest", s)
		}
		listed[s] = true
		if _, ok := declared[s]; !ok {
			t.Errorf("Shapes names %q, which this package does not declare", s)
		}
	}
	for shape, constName := range declared {
		if !listed[shape] {
			t.Errorf("%s is declared and missing from Shapes, so no corpus ever draws it. "+
				"A shape nothing draws is a case the grammar is not covered against, and "+
				"nothing else fails when it goes missing.", constName)
		}
	}
}
