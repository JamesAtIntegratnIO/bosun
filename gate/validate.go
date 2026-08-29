package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

	"sigs.k8s.io/yaml"
)

// ValidateManifests renders every source and schema-validates the result with
// kubeconform, writing a markdown report of the findings to w. It returns the
// number of manifests that failed.
//
// -ignore-missing-schemas is effectively mandatory rather than a convenience:
// CRDs outside the big projects are in no published catalogue, and without it
// one unknown kind fails a run that had nothing wrong with it. The cost is
// real and worth stating; those kinds are not checked.
func ValidateManifests(ctx context.Context, repoRoot string, cfg *Config, inv *Inventory, w io.Writer) (int, error) {
	if _, err := exec.LookPath("kubeconform"); err != nil {
		return 0, fmt.Errorf("kubeconform is not on PATH: %w", err)
	}

	streams, err := renderStreams(ctx, repoRoot, cfg, inv)
	if err != nil {
		return 0, err
	}

	var failures int
	for name, doc := range streams {
		out, err := runKubeconform(ctx, cfg, doc)
		if err != nil {
			return 0, fmt.Errorf("running kubeconform on %s: %w", name, err)
		}
		if len(out) > 0 {
			failures += len(out)
			fmt.Fprintf(w, "### %s\n\n", name)
			for _, r := range out {
				fmt.Fprintf(w, "- `%s/%s`: %s\n", r.Kind, r.Name, r.Msg)
			}
			fmt.Fprintln(w)
		}
	}

	if failures == 0 {
		fmt.Fprintf(w, "All rendered manifests passed schema validation.\n")
	}
	return failures, nil
}

type kubeconformResult struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Msg  string `json:"msg"`
}

func runKubeconform(ctx context.Context, cfg *Config, doc []byte) ([]kubeconformResult, error) {
	args := []string{"-strict", "-output", "json", "-summary=false"}
	if cfg.Validate.IgnoreMissingSchemas {
		args = append(args, "-ignore-missing-schemas")
	}
	for _, s := range cfg.Validate.SchemaLocations {
		args = append(args, "-schema-location", s)
	}
	for _, k := range cfg.Validate.SkipKinds {
		args = append(args, "-skip", k)
	}
	args = append(args, "-")

	// Bounded like every other subprocess the gate starts. Not through `run`,
	// because that one reads a non-zero exit as a failure and this one must
	// not: kubeconform exits non-zero when a manifest is invalid, which is a
	// result rather than an execution failure, so the output is parsed either
	// way.
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubeconform", args...)
	cmd.Stdin = bytes.NewReader(doc)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()

	// A killed kubeconform writes nothing, and the parse below would then
	// blame the empty stdout on kubeconform's output format. Say the deadline
	// expired instead.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("kubeconform did not finish within %s: %w", toolTimeout, err)
	}

	var parsed struct {
		Resources []struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Msg    string `json:"msg"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("%w (stderr: %s)", err, stderr.String())
	}

	var bad []kubeconformResult
	for _, r := range parsed.Resources {
		if r.Status == "statusInvalid" || r.Status == "statusError" {
			bad = append(bad, kubeconformResult{Kind: r.Kind, Name: r.Name, Msg: r.Msg})
		}
	}
	return bad, nil
}

// renderStreams re-collects every source and returns the raw manifests for
// each, keyed by a human-readable name.
func renderStreams(ctx context.Context, repoRoot string, cfg *Config, inv *Inventory) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, src := range cfg.Sources {
		batch, err := collect(ctx, repoRoot, cfg, inv, src)
		if err != nil {
			return nil, err
		}
		for _, d := range batch {
			key := d.source
			if d.cluster != nil {
				key = fmt.Sprintf("%s on %s", d.source, d.cluster.Name)
			}
			var buf bytes.Buffer
			for _, obj := range d.objects {
				raw, err := yaml.Marshal(obj)
				if err != nil {
					return nil, err
				}
				buf.WriteString("---\n")
				buf.Write(raw)
			}
			out[key] = buf.Bytes()
		}
	}
	return out, nil
}
