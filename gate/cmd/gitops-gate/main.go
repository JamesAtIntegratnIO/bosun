// gitops-gate answers one question about a pull request: does this change what
// actually gets deployed, and is what it produces still valid?
//
// It is CI-agnostic on purpose. Run the binary, read the exit code:
//
//	0  no blocking change
//	1  blocking change -- targeting moved, or validation failed
//	2  the gate could not run
//
// Exit 2 is deliberately distinct from 1. "This change is bad" and "the gate is
// broken" want opposite reactions, and a CI system that shows them identically
// teaches people to ignore the check.
//
// This package is the command-line face only. The rendering, diffing and
// validation live in the gate package one level up, where the agent imports
// them directly -- the CLI and the in-cluster gate answer with the same code.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

const (
	exitOK       = 0
	exitBlocking = 1
	exitBroken   = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitBroken)
	}
	var err error
	var blocking bool

	switch os.Args[1] {
	case "render":
		err = cmdRender(os.Args[2:])
	case "diff":
		blocking, err = cmdDiff(os.Args[2:])
	case "validate":
		blocking, err = cmdValidate(os.Args[2:])
	case "clusters":
		err = cmdClusters(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(exitBroken)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "gitops-gate: %v\n", err)
		os.Exit(exitBroken)
	}
	if blocking {
		os.Exit(exitBlocking)
	}
	os.Exit(exitOK)
}

func usage() {
	fmt.Fprint(os.Stderr, `gitops-gate -- what does this pull request actually change?

  render    Expand every ApplicationSet into the Applications it generates.
  diff      Compare two renders. Fails when cluster targeting changed.
  validate  Schema-validate rendered manifests.
  clusters  export -- regenerate the cluster inventory from live ArgoCD.

Run a command with -h for its flags.
`)
}

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	root := fs.String("repo", ".", "path to the repository worktree to render")
	cfgPath := fs.String("config", "", "path to .gitops-gate.yaml (default: <repo>/.gitops-gate.yaml)")
	out := fs.String("out", "", "write the target table here (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, inv, err := load(*root, *cfgPath)
	if err != nil {
		return err
	}

	table, err := gate.Render(*root, cfg, inv)
	if err != nil {
		return err
	}

	if *out == "" {
		return table.WriteJSON(os.Stdout)
	}
	if err := gate.WriteTableFile(*out, table); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "rendered %d Applications across %d clusters -> %s\n",
		len(table.Rows), len(inv.Clusters), *out)
	for _, w := range table.Warnings {
		fmt.Fprintf(os.Stderr, "  not covered: %s\n", w)
	}
	return nil
}

func cmdDiff(args []string) (bool, error) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	basePath := fs.String("base", "", "target table for the base revision (required)")
	headPath := fs.String("head", "", "target table for the head revision (required)")
	repo := fs.String("repo", "", "repository worktree; enables chart-diff -- renders every chart whose version moved, at BOTH versions, and diffs the resources")
	cfgPath := fs.String("config", "", "path to .gitops-gate.yaml (default: <repo>/.gitops-gate.yaml)")
	reportPath := fs.String("report", "", "write a markdown report here (default: stdout)")
	jsonOut := fs.String("json", "", "write the machine-readable diff here")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if *basePath == "" || *headPath == "" {
		return false, fmt.Errorf("-base and -head are both required")
	}

	base, err := gate.ReadTableFile(*basePath)
	if err != nil {
		return false, err
	}
	head, err := gate.ReadTableFile(*headPath)
	if err != nil {
		return false, err
	}

	// Chart-diff turns "the version moved" into "here is what the version
	// moving does". It needs the repository for the value files, so it is
	// opt-in via -repo rather than assumed -- gate.Assemble skips the two
	// worktree-dependent steps when the root is empty.
	var cfg *gate.Config
	if *repo != "" {
		var err error
		cfg, _, err = load(*repo, *cfgPath)
		if err != nil {
			return false, err
		}
	}
	res := gate.Assemble(*repo, cfg, base, head)

	// Rendered whole, then written once. A report streamed straight at a file
	// discards every write error on the way and then discards the Close, which
	// is where a full disk actually surfaces -- so a truncated report was
	// presented as a complete one, from the tool whose entire job is not doing
	// that.
	var report strings.Builder
	res.Report(&report)
	if err := writeReport(*reportPath, report.String()); err != nil {
		return false, err
	}

	if *jsonOut != "" {
		if err := writeJSONFile(*jsonOut, res); err != nil {
			return false, err
		}
	}

	// The same headline the report leads with and the in-cluster service puts
	// on its commit status. Counting targeting and source changes here was
	// wrong in the way that matters: a run blocked only by a dropped API
	// version or a dropped setting printed "0 targeting change(s), 0 other
	// source change(s) -- blocking", which reads as the gate contradicting
	// itself.
	blocking, headline := res.Verdict()
	fmt.Fprintf(os.Stderr, "\ngitops-gate: %s\n", headline)
	return blocking, nil
}

// cmdValidate schema-validates every rendered manifest with kubeconform.
//
// -ignore-missing-schemas is effectively mandatory rather than a convenience:
// CRDs outside the big projects are in no published catalogue, and without it
// one unknown kind fails a run that had nothing wrong with it. The cost is
// real and worth stating -- those kinds are simply not checked.
func cmdValidate(args []string) (bool, error) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	root := fs.String("repo", ".", "path to the repository worktree")
	cfgPath := fs.String("config", "", "path to .gitops-gate.yaml (default: <repo>/.gitops-gate.yaml)")
	reportPath := fs.String("report", "", "write a markdown report here (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return false, err
	}

	cfg, inv, err := load(*root, *cfgPath)
	if err != nil {
		return false, err
	}
	if !cfg.Validate.Enabled {
		fmt.Fprintln(os.Stderr, "validation is disabled in .gitops-gate.yaml")
		return false, nil
	}

	var report strings.Builder
	failures, err := gate.ValidateManifests(*root, cfg, inv, &report)
	if err != nil {
		return false, err
	}
	if err := writeReport(*reportPath, report.String()); err != nil {
		return false, err
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "gitops-gate: %d manifest(s) failed schema validation\n", failures)
		return true, nil
	}
	return false, nil
}

// cmdClusters regenerates the inventory from the live ArgoCD cluster Secrets.
//
// The inventory has to be checked in, because CI cannot reach the cluster. That
// makes it a snapshot, and snapshots go stale silently. Running this somewhere
// with cluster access and diffing the result is the only way that drift ever
// surfaces -- so `export` is built to be run in a check, not just by hand.
func cmdClusters(args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return fmt.Errorf("usage: gitops-gate clusters export [-out FILE] [-context CTX] [-namespace NS]")
	}
	fs := flag.NewFlagSet("clusters export", flag.ExitOnError)
	out := fs.String("out", "", "write the inventory here (default: stdout)")
	kubeContext := fs.String("context", "", "kubectl context to read from")
	namespace := fs.String("namespace", "argocd", "namespace holding the ArgoCD cluster Secrets")
	check := fs.Bool("check", false, "compare against an existing inventory and exit non-zero if it has drifted")
	configPath := fs.String("config", ".gitops-gate.yaml", "config to read clustersExport.ignoreKeys from")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	// Pick up site-specific ignore keys if a config is reachable. Export is
	// deliberately usable without one, so a missing config is not an error.
	filter := gate.NewExportFilter("", nil)
	if cfg, err := gate.LoadConfig(*configPath); err == nil {
		filter = gate.NewExportFilter(filepath.Dir(*configPath), cfg)
	}

	inv, err := gate.ExportClusters(*kubeContext, *namespace, filter)
	if err != nil {
		return err
	}

	rendered, err := yaml.Marshal(inv)
	if err != nil {
		return err
	}

	if *check {
		if *out == "" {
			return fmt.Errorf("-check needs -out to name the inventory to compare against")
		}
		existing, err := os.ReadFile(*out)
		if err != nil {
			return fmt.Errorf("reading %s to compare: %w", *out, err)
		}
		if gate.NormaliseInventory(existing) != gate.NormaliseInventory(rendered) {
			fmt.Fprintf(os.Stderr, "cluster inventory has drifted from the live cluster.\n"+
				"The gate's targeting check is only as good as this file.\n"+
				"Refresh it with: gitops-gate clusters export -out %s\n", *out)
			return fmt.Errorf("inventory is stale")
		}
		fmt.Fprintln(os.Stderr, "cluster inventory matches the live cluster")
		return nil
	}

	if *out == "" {
		_, err = os.Stdout.Write(rendered)
		return err
	}
	return os.WriteFile(*out, rendered, 0o644)
}

func load(root, cfgPath string) (*gate.Config, *gate.Inventory, error) {
	if cfgPath == "" {
		cfgPath = filepath.Join(root, ".gitops-gate.yaml")
	}
	cfg, err := gate.LoadConfig(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	// The CLI reads the inventory from a checked-in snapshot, so the config
	// must say where it is. The in-cluster gate reads the inventory live and
	// has no use for this key -- which is why the requirement lives here and
	// not in the parser.
	if cfg.Clusters == "" {
		return nil, nil, fmt.Errorf("%s: `clusters` is required -- run `gitops-gate clusters export` to create one", cfgPath)
	}
	inv, err := gate.LoadInventory(filepath.Join(root, cfg.Clusters))
	if err != nil {
		return nil, nil, err
	}
	return cfg, inv, nil
}

func writeJSONFile(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Checked, not deferred: this is where a short write reports itself, and
	// render-diff.json is read by whatever consumes the gate's verdict.
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}

// writeReport puts a rendered report where the caller asked for it: a file when
// one was named, stdout otherwise.
//
// Rendered whole and written once, on purpose. Streaming into a file discards
// every write error on the way there and then discards the Close -- which is
// where a full disk actually surfaces -- so a truncated report was handed back
// as a complete one by the tool whose entire job is not doing that.
func writeReport(path, body string) error {
	if path == "" {
		_, err := io.WriteString(os.Stdout, body)
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, body); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Checked, not deferred: on a written file this is where a short write
	// finally reports itself.
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}
