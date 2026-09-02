package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
)

// No field path from any MCP tool result reaches a credential.
//
// This is the primary control on that surface and it is a structural one, for
// a reason the wire cannot supply: a behavioural test samples what a handler
// happened to produce for the world a fixture set up, and it cannot prove a
// negative about the paths no request exercised. Walking the types proves it
// for every path, exercised or not.
//
// It lives in this package because this is where Config is. mcp imports the
// result types and never the configuration type, which is what makes the rule
// hold, and Go's own dependency rule is most of the enforcement -- package
// main cannot be imported at all. What this adds is the part that is not
// automatic: a result type could still reach a credential by holding a
// cluster client, a map assembled from the environment, or a struct from a
// package that has one, and none of those would need an import of main.
//
// Both sides are derived. The types come from mcp.Tools(), so a second tool is
// covered the moment it is registered; the credential names come from
// config.go's own syntax tree, so a credential added to Config is covered the
// moment it is loaded.
func TestNoToolResultCanReachACredential(t *testing.T) {
	tools := mcp.Tools()
	if len(tools) == 0 {
		t.Fatal("mcp registers no tools, so this walk has nothing to check and reads " +
			"exactly like a pass")
	}

	// The package whose shapes a result is built from, taken from a type
	// rather than written out, so a move or a rename cannot leave this test
	// silently allowing the wrong package.
	own := reflect.TypeOf(mcp.Text{}).PkgPath()

	// What else a field may be.
	//
	// An allowlist, and a short one. time holds the sweep timestamp; the empty
	// path is every builtin. Anything else is a type from a package that has
	// collaborators of its own, and "does that package's type reach a
	// credential" is a question this test would then have to answer
	// transitively for every future version of it.
	//
	// An entry here is a package this test STOPS at. That is the whole meaning
	// of the allowlist: time.Time's unexported fields are not a finding, they
	// are the reason the vetting is per-package rather than per-field.
	allowed := map[string]bool{"time": true}

	credentials := configCredentialFields(t)
	fields := 0

	var walk func(typ reflect.Type, path string, seen map[reflect.Type]bool)
	walk = func(typ reflect.Type, path string, seen map[reflect.Type]bool) {
		if seen[typ] {
			return
		}
		seen[typ] = true

		if pkg := typ.PkgPath(); pkg != "" && pkg != own {
			if !allowed[pkg] {
				t.Errorf("%s reaches %s.%s.\n"+
					"An MCP result may only be built from mcp's own shapes. A type from anywhere "+
					"else brings that package's collaborators with it, and whether one of them "+
					"holds a credential is a question that has to be re-answered on every "+
					"upgrade. Copy the fields you need onto a type in mcp instead -- that is "+
					"what web.GateStatus does, and its comment says why.", path, pkg, typ.Name())
			}
			// Vetted, and therefore a leaf: descending into time.Time would
			// report its unexported clock fields as a finding, which is
			// neither true nor actionable.
			return
		}

		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(typ.Elem(), path+"[]", seen)
		case reflect.Map:
			walk(typ.Key(), path+"{key}", seen)
			walk(typ.Elem(), path+"{}", seen)
		case reflect.Interface:
			// An interface field is a hole in this walk: what it holds is
			// decided at run time, and nothing here can see it.
			t.Errorf("%s is an interface.\n"+
				"A result type cannot carry one: this walk can see a struct's fields and it "+
				"cannot see what a caller put in an interface, so the guarantee would end "+
				"exactly where the field begins.", path)
		case reflect.Chan, reflect.Func, reflect.UnsafePointer:
			t.Errorf("%s is a %s, which cannot be serialised and has no business on a "+
				"published result type.", path, typ.Kind())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				fields++
				if !f.IsExported() {
					t.Errorf("%s.%s is unexported, so it is invisible on the wire and "+
						"invisible to a reviewer reading the schema. Result types carry only "+
						"what they publish.", path, f.Name)
					continue
				}
				if credentials[f.Name] {
					t.Errorf("%s.%s has the name of a credential config.go loads.\n"+
						"Either it is one, or it is a field that will read as one to the next "+
						"person auditing this surface. Rename it.", path, f.Name)
				}
				walk(f.Type, path+"."+f.Name, seen)
			}
		}
	}

	for _, tool := range tools {
		if tool.Result == nil {
			t.Errorf("the tool %s carries no Result, so nothing can walk what it returns.\n"+
				"Set Result to the zero value of the type Call returns; it is the only way "+
				"this guarantee covers a tool at all.", tool.Name)
			continue
		}
		before := fields
		walk(reflect.TypeOf(tool.Result), tool.Name, map[reflect.Type]bool{})
		// Per tool, and not only in total. The enumeration means a new tool is
		// covered the moment it is registered, and the failure mode of that is
		// silence: a result type this walk stops descending into -- because it
		// became an alias of one already seen, say -- leaves the total healthy
		// while one tool goes unread. A guard that quietly stops covering a
		// tool is worse than no guard, because it is still cited.
		if fields == before {
			t.Errorf("walking %s's result read no fields at all, so nothing above checked "+
				"it. This guarantee is cited per tool; fix the walk rather than trusting "+
				"the total.", tool.Name)
		}
	}

	// The self-check, and not optional. A walk that stops descending -- a
	// result type replaced by a map[string]any, say -- would visit almost
	// nothing and report a clean pass over a surface it never read.
	if fields < 15 {
		t.Fatalf("this walk saw only %d fields across %d tool result types. The results are "+
			"shaped differently now and it is no longer reading them. Fix the walk rather "+
			"than lowering this number.", fields, len(tools))
	}
}

// And no tool result is a loosely-typed bag.
//
// A map[string]any would satisfy every check above -- it reaches no package,
// it holds no credential-named field -- while being able to carry absolutely
// anything at run time. The typed-facts rule is what makes the structural
// guarantee mean something, so it is asserted rather than assumed.
func TestEveryToolResultIsAStruct(t *testing.T) {
	for _, tool := range mcp.Tools() {
		if tool.Result == nil {
			continue // named by the test above
		}
		if k := reflect.TypeOf(tool.Result).Kind(); k != reflect.Struct {
			t.Errorf("the tool %s returns a %s.\n"+
				"A result has to be a struct: it is a published schema, and a map or a slice "+
				"is one whose fields no reviewer can see and no walk can check.", tool.Name, k)
		}
	}
}

// configCredentialFields is every Config field named by Config.Secrets, read
// from config.go's syntax tree.
//
// Derived, for the same reason redaction_test.go derives its list: a credential
// added to Config and named in Secrets is one this test should be checking
// against the moment it is written, and a copy of the list here would be a
// second thing to remember.
func configCredentialFields(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(helmtest.Root(t), "config.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", path, err)
	}

	out := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Secrets" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				out[sel.Sel.Name] = true
			}
			return true
		})
	}

	// The self-check. Secrets returns five credentials today; a walk that
	// found none would leave the assertion above comparing every field name
	// against an empty set.
	if len(out) < 5 {
		t.Fatalf("found only %d credential fields in Config.Secrets (%v); it is written "+
			"differently now and this walk no longer reads it. Fix the walk rather than "+
			"lowering this number.", len(out), sortedNames(out))
	}
	return out
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// And the description of the surface a reviewer reads is the one it serves.
//
// Nothing else in this repository could notice a tool whose published
// description stopped describing it, and "what does this disclose" is the
// question an operator asks before routing anything to this port.
func TestEveryToolDescribesItselfInFullSentences(t *testing.T) {
	for _, tool := range mcp.Tools() {
		if len(tool.Description) < 80 {
			t.Errorf("the tool %s describes itself in %d characters.\n"+
				"An operator deciding whether to publish this port, and a model deciding "+
				"whether to call it, both read this and nothing else.", tool.Name, len(tool.Description))
		}
		if !strings.HasSuffix(strings.TrimSpace(tool.Description), ".") {
			t.Errorf("the tool %s's description is not a sentence", tool.Name)
		}
		if len(tool.Params) == 0 {
			t.Errorf("the tool %s publishes no input schema, so a client cannot tell a "+
				"tool that takes no arguments from one whose arguments it does not know",
				tool.Name)
		}
	}
}
