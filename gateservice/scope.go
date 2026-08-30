package gateservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// configNames are the config filenames, newest first.
//
// Two names for one file is a cost, paid deliberately: the alternative is a
// flag day on every install. Finding both is an error rather than a precedence
// rule, because a silent precedence is how a repository ends up maintaining
// the file the gate is not reading.
var configNames = []string{".bosun.yaml", ".gitops-gate.yaml"}

// readConfig finds and parses the config at one revision.
//
// A missing file is not an error. Since ADR 0012 the common install has none:
// ArgoCD says what this repository deploys, and the file exists for the
// remainder, which for most repositories is nothing.
func readConfig(dir string) (*gate.Config, string, error) {
	var found []string
	for _, name := range configNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return nil, "", nil
	case 1:
	default:
		return nil, "", fmt.Errorf(
			"both %s are present, and they configure the same gate.\n\n"+
				"Keep one. `%s` is the current name; `%s` is still read so that an "+
				"install does not have to move on the same day it upgrades. Which of "+
				"the two is in force is not something to leave to a precedence rule",
			strings.Join(found, " and "), configNames[0], configNames[1])
	}

	name := found[0]
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, name, fmt.Errorf("reading %s: %w", name, err)
	}
	cfg, err := gate.ParseConfig(raw, name)
	if err != nil {
		return nil, name, err
	}
	return cfg, name, nil
}

// plan is the assembled answer to "what does this pull request deploy?", built
// from what ArgoCD serves and what the repository says, in that order.
type plan struct {
	cfg *gate.Config
	// name is the config file in force, empty when there is none.
	name string
	// scope is what the report says about how this was arrived at.
	scope []gate.Markdown
}

// buildPlan merges a derivation with a config file.
//
// The order is ADR 0012's, and both halves of it matter. Live supplies the
// pointers, because a hand-maintained copy of them drifts. Head supplies the
// content and wins wherever both can answer, because the applied spec is the
// previous answer and the question is what this change does.
//
// head is the checkout the roots are looked for in. derived may be nil, which
// is what a deployment with no ArgoCD reads configured looks like; the file is
// then the whole scope, exactly as before.
func buildPlan(head string, cfg *gate.Config, name string, derived *gate.Derivation) (*plan, error) {
	if cfg == nil {
		// Parsed from nothing rather than built as a literal, so a repository
		// with no file gets exactly the defaulting one whose file leaves the
		// same keys unset gets, from the one function that does it.
		var err error
		cfg, err = gate.ParseConfig([]byte("{}"), configNames[0])
		if err != nil {
			return nil, err
		}
	}
	p := &plan{cfg: cfg, name: name}

	// The file's own sources come first, so a derived source rendering the
	// same path is dropped rather than the other way round.
	claimed := map[string]bool{}
	for _, s := range cfg.Sources {
		claimed[renderTarget(s)] = true
	}

	// Roots named in the file are rendered from this checkout, always. This is
	// the case derivation cannot reach: a root this pull request introduces
	// has no live object to be found by, so nothing would render it at all.
	rootIdentities := map[string]bool{}
	for _, path := range cfg.Roots {
		ids, err := identitiesIn(head, path)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			rootIdentities[id] = true
		}
		if claimed[string(gate.SourceManifests)+"\x00"+path] {
			continue
		}
		claimed[string(gate.SourceManifests)+"\x00"+path] = true
		cfg.Sources = append(cfg.Sources, gate.Source{
			Name:  "root/" + path,
			Type:  gate.SourceManifests,
			Paths: []string{path},
		})
	}

	if derived == nil {
		if len(cfg.Sources) == 0 {
			return nil, errNothingToRender(name, nil)
		}
		return p, nil
	}

	for _, s := range derived.Sources {
		if claimed[renderTarget(s)] {
			continue
		}
		claimed[renderTarget(s)] = true
		cfg.Sources = append(cfg.Sources, s)
	}

	var fromLive []string
	for _, r := range derived.Roots {
		if rootIdentities[r.Identity()] {
			// Named in the file, so it is already rendered from head, and
			// expanding the applied spec as well would count it twice.
			continue
		}
		path, found, err := gate.FindManifest(head, r.Kind, r.Namespace, r.Name)
		if err != nil {
			return nil, err
		}
		if found {
			if !claimed[string(gate.SourceManifests)+"\x00"+path] {
				claimed[string(gate.SourceManifests)+"\x00"+path] = true
				cfg.Sources = append(cfg.Sources, gate.Source{
					Name:  "root/" + path,
					Type:  gate.SourceManifests,
					Paths: []string{path},
				})
			}
			continue
		}
		cfg.Sources = append(cfg.Sources, gate.Source{
			Name:    "live/" + r.Name,
			Type:    gate.SourceLive,
			Objects: []map[string]any{r.Object},
		})
		fromLive = append(fromLive, r.Name)
	}

	if len(cfg.Sources) == 0 {
		return nil, errNothingToRender(name, derived)
	}

	p.scope = scopeLines(derived, cfg, name, fromLive)
	return p, nil
}

// renderTarget is what a source renders, used to stop one path being rendered
// twice under two names.
func renderTarget(s gate.Source) string {
	switch s.Type {
	case gate.SourceHelm:
		return string(s.Type) + "\x00" + s.Chart
	case gate.SourceManifests, gate.SourceRendered:
		return string(s.Type) + "\x00" + strings.Join(s.Paths, ",")
	default:
		return string(s.Type) + "\x00" + s.Path
	}
}

// identitiesIn reads a manifest named under `roots:` and returns the objects it
// declares, so a live root with the same identity is not also expanded.
//
// A path that does not resolve is an error rather than a skip. It is the one
// thing the file is for, and a typo that silently fell back to the applied
// spec would produce a green gate on the exact change the entry exists to see.
func identitiesIn(head, path string) ([]string, error) {
	full := filepath.Join(head, filepath.FromSlash(path))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("`roots` names %s, and the head revision does not have it: %w", path, err)
	}
	var out []string
	for _, doc := range strings.Split(string(raw), "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var o struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			continue
		}
		if o.Kind == "" || o.Metadata.Name == "" {
			continue
		}
		out = append(out, gate.LiveRoot{
			Kind: o.Kind, Name: o.Metadata.Name, Namespace: o.Metadata.Namespace,
		}.Identity())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("`roots` names %s, and nothing in it declares a kind and a name", path)
	}
	return out, nil
}

// scopeLines is what the report says about how the scope was arrived at.
//
// These are gate.Markdown: the backticks in them are deliberate and the report
// prints them as written, so every value the gate did not choose — the config
// file's name, the root names ArgoCD served — goes through gate.Inline here,
// where the line still knows which of its parts are values.
func scopeLines(d *gate.Derivation, cfg *gate.Config, name string, fromLive []string) []gate.Markdown {
	out := []gate.Markdown{gate.Markdown(fmt.Sprintf(
		"Derived %s from %s and %s that ArgoCD serves, and rendered %s in total.",
		plural(len(d.Sources), "source"), plural(d.Applications, "Application"),
		plural(d.ApplicationSets, "ApplicationSet"), plural(len(cfg.Sources), "source")))}

	if name != "" {
		out = append(out, gate.Markdown(fmt.Sprintf(
			"`%s` is present, and its sources take precedence over derived ones.", gate.Inline(name))))
	}
	if len(fromLive) > 0 {
		sort.Strings(fromLive)
		escaped := make([]string, len(fromLive))
		for i, r := range fromLive {
			escaped[i] = gate.Inline(r)
		}
		out = append(out, gate.Markdown(fmt.Sprintf(
			"%s rendered from the spec ArgoCD has applied, not from this repository, "+
				"because no manifest here declares them: `%s`. An edit to one of these is invisible to "+
				"this gate until it applies; naming its file under `roots:` in `%s` fixes that.",
			plural(len(fromLive), "root"), strings.Join(escaped, "`, `"), configNames[0])))
	}
	out = append(out, d.Warnings...)
	return out
}

// errNothingToRender refuses rather than reporting no change.
//
// The same rule as an unreadable inventory, for the same reason: a render
// against a world the gate could not see finds no targeting change and waves
// everything through. An empty scope is that world.
func errNothingToRender(name string, d *gate.Derivation) error {
	var b strings.Builder
	b.WriteString("nothing to render: no source was derived and none is configured.\n\n")
	if d != nil {
		fmt.Fprintf(&b, "ArgoCD served %s and %s, and none of them points at this repository.\n\n",
			plural(d.Applications, "Application"), plural(d.ApplicationSets, "ApplicationSet"))
	}
	fmt.Fprintf(&b,
		"This is refused rather than reported as no change, because a comparison\n"+
			"between two empty sets finds no difference and would pass every pull\n"+
			"request. Check that the repository URL the gate was given matches the one\n"+
			"the Applications carry, or list what to render under `sources` in `%s`",
		configNames[0])
	if name != "" {
		fmt.Fprintf(&b, " (`%s` is present and lists none)", name)
	}
	b.WriteString(".")
	return errors.New(b.String())
}
