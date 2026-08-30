package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// Derive turns what ArgoCD serves into the gate's render plan for one
// repository.
//
// This is ADR 0012's algorithm, and the division of labour in it is the whole
// point: ArgoCD says *what* is deployed and from where, and every byte that
// gets rendered comes out of the pull request's checkout. Nothing here reads a
// file, and the only content it carries is the applied spec of a root the
// caller may find it does not have a copy of.
//
// It lives in this package rather than in `gate` because it is a fact about
// the outside world, and the dependency runs one way. `gate` owns the
// vocabulary, this owns the round trip.
func (a *ArgoCD) Derive(ctx context.Context, repoURL string) (*gate.Derivation, error) {
	apps, err := a.Applications(ctx)
	if err != nil {
		return nil, err
	}
	sets, err := a.ApplicationSets(ctx)
	if err != nil {
		return nil, err
	}
	d := DeriveFrom(apps, sets, repoURL)
	return d, nil
}

// DeriveFrom is Derive with the reads already done, which is what makes the
// algorithm testable against a fixture rather than against a server.
func DeriveFrom(apps []Application, sets []ApplicationSet, repoURL string) *gate.Derivation {
	d := &gate.Derivation{Applications: len(apps), ApplicationSets: len(sets)}

	seen := map[string]bool{}
	for _, app := range apps {
		if !app.PointsAt(repoURL) {
			continue
		}
		srcs, warns := sourcesFor(app, repoURL)
		d.Warnings = append(d.Warnings, warns...)
		for _, s := range srcs {
			// Deduped by resolved shape rather than by Application name. A
			// fleet renders the same path once per cluster, and fifty
			// identical sources is fifty identical renders of one directory.
			key := sourceKey(s)
			if seen[key] {
				continue
			}
			seen[key] = true
			d.Sources = append(d.Sources, s)
		}
	}

	for _, s := range sets {
		// A tracked ApplicationSet is reached through the content that
		// creates it, and expanding the applied spec as well would count it
		// twice, from a spec that is by definition the previous answer.
		if !s.IsRoot() {
			continue
		}
		d.Roots = append(d.Roots, gate.LiveRoot{
			Kind:      "ApplicationSet",
			Name:      s.Name,
			Namespace: s.Namespace,
			Object:    s.Object,
		})
	}

	sort.Slice(d.Sources, func(i, j int) bool { return d.Sources[i].Name < d.Sources[j].Name })
	sort.Slice(d.Roots, func(i, j int) bool { return d.Roots[i].Identity() < d.Roots[j].Identity() })
	sort.Slice(d.Warnings, func(i, j int) bool { return d.Warnings[i] < d.Warnings[j] })
	return d
}

// sourcesFor turns one Application into the sources that render it.
//
// Only the sources pointing at the gated repository become renders. A source
// on somebody else's chart repository is a version this gate already reads
// another way: the chart diff renders both sides of a chart move and reports
// the fields that changed, and re-rendering it here would produce the same
// objects under a second name.
func sourcesFor(app Application, repoURL string) ([]gate.Source, []gate.Markdown) {
	var out []gate.Source
	var warns []gate.Markdown

	// The dry source is where a hydrated Application's manifests are written
	// by hand, so it is the one a pull request changes.
	all := app.Sources
	if app.DrySource != nil {
		all = append(append([]AppSource{}, all...), *app.DrySource)
	}

	for i, s := range all {
		if normaliseRepoURL(s.RepoURL) != normaliseRepoURL(repoURL) {
			continue
		}
		switch {
		case s.Chart != "":
			// A chart pulled by name is an artifact, not a path in this
			// checkout, even when the repository URL happens to match.
			warns = append(warns, gate.Markdown(gate.Inline(fmt.Sprintf(
				"Application %s source %d names chart %q rather than a path, so it is not rendered from this repository.",
				app.Name, i, s.Chart))))

		case s.Path == "":
			// A values-only source: it exists to be addressed by `ref` from a
			// sibling, and has nothing of its own to render.
			if s.Ref == "" {
				warns = append(warns, gate.Markdown(gate.Inline(fmt.Sprintf(
					"Application %s source %d has neither a path nor a chart, and is not rendered.", app.Name, i))))
			}

		case s.Helm != nil:
			files, missing := resolveValueFiles(app, s, repoURL)
			warns = append(warns, missing...)
			out = append(out, gate.Source{
				Name:         "app/" + app.Name,
				Type:         gate.SourceHelm,
				Chart:        s.Path,
				ValueFiles:   files,
				ValuesInline: s.Helm.ValuesObject,
			})

		default:
			dir := s.Directory
			if dir == nil {
				dir = &AppDirectory{}
			}
			out = append(out, gate.Source{
				Name:    "app/" + app.Name,
				Type:    gate.SourceDirectory,
				Path:    s.Path,
				Recurse: dir.Recurse,
				Include: dir.Include,
				Exclude: dir.Exclude,
			})
		}
	}
	return out, warns
}

// resolveValueFiles turns an Application's `valueFiles` into paths in this
// checkout, resolving `$ref/…` through the sibling source that named itself.
//
// This is what retires the repository-wide `valuesRef` setting. That key was a
// single guess applied to every Application at once, and it was wrong the
// moment two of them used different names; the Application says which of its
// own sources holds its values, so the answer is per-Application and exact.
//
// A `$ref/` whose sibling points at another repository resolves to a file this
// checkout does not have. It is dropped with a warning rather than silently,
// because the render then happens with one values layer missing and the diff
// would attribute the difference to the pull request.
func resolveValueFiles(app Application, s AppSource, repoURL string) ([]string, []gate.Markdown) {
	var files []string
	var warns []gate.Markdown
	for _, vf := range s.Helm.ValueFiles {
		if !strings.HasPrefix(vf, "$") {
			files = append(files, vf)
			continue
		}
		name, rest, ok := strings.Cut(strings.TrimPrefix(vf, "$"), "/")
		if !ok {
			warns = append(warns, gate.Markdown(gate.Inline(fmt.Sprintf(
				"Application %s: value file %q names no path after its ref.", app.Name, vf))))
			continue
		}
		ref := refSource(app, name)
		switch {
		case ref == nil:
			warns = append(warns, gate.Markdown(gate.Inline(fmt.Sprintf(
				"Application %s: value file %q refers to $%s, and no source of that Application is named %s.",
				app.Name, vf, name, name))))
		case normaliseRepoURL(ref.RepoURL) != normaliseRepoURL(repoURL):
			warns = append(warns, gate.Markdown(gate.Inline(fmt.Sprintf(
				"Application %s: value file %q resolves into %s, which is not the repository being gated, so it is rendered without that layer.",
				app.Name, vf, ref.RepoURL))))
		default:
			files = append(files, rest)
		}
	}
	return files, warns
}

// refSource finds the source an Application named, for `$name/…`.
func refSource(app Application, name string) *AppSource {
	for i := range app.Sources {
		if app.Sources[i].Ref == name {
			return &app.Sources[i]
		}
	}
	return nil
}

// sourceKey is a derived source's identity for deduplication: everything that
// changes what renders, and nothing that does not.
//
// The name is deliberately excluded. Fifty clusters produce fifty Applications
// with different names off one path, and keying on the name would render that
// path fifty times to reach one answer.
func sourceKey(s gate.Source) string {
	return strings.Join([]string{
		string(s.Type), s.Path, s.Chart,
		strings.Join(s.ValueFiles, ","),
		fmt.Sprint(s.Recurse), s.Include, s.Exclude,
		fmt.Sprint(s.ValuesInline),
	}, "\x00")
}
