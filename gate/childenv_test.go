package gate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/childenv"
)

// Neither of the gate's subprocesses is handed a credential this process
// loaded.
//
// These are the children with the least business holding one and the most
// exposure to somebody else's code: `helm template` renders a chart from a
// registry over somebody else's network, and a chart's own helm plugin runs
// here, as this process's child, with this process's environment. cmd.Env was
// nil at both call sites, so both ran with GIT_TOKEN, ARGOCD_TOKEN, the model
// key and the rest.
//
// Redaction cannot reach this. It filters what a child prints; a child that
// writes its environment to a file, or posts it, has published a credential
// without printing a byte.
func TestTheGatesSubprocessesInheritNoCredential(t *testing.T) {
	const credential = "BOSUN_TEST_CREDENTIAL"
	const value = "sentinel-must-not-be-inherited"

	for _, tc := range []struct {
		name string
		// the binary to put on PATH, and what it must print on stdout for the
		// caller to get as far as reading it
		tool, stdout string
		run          func(t *testing.T, dir string)
	}{
		{
			name: "the renderer",
			tool: "renderer",
			run: func(t *testing.T, dir string) {
				if _, err := run(context.Background(), dir, filepath.Join(dir, "renderer")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "kubeconform",
			tool:   "kubeconform",
			stdout: `{"resources":[]}`,
			run: func(t *testing.T, dir string) {
				if _, err := runKubeconform(context.Background(), &Config{}, []byte("kind: ConfigMap\n")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(dir, "record")
			// Writes its own environment where the test can read it, which is
			// what any child could do with what it was handed.
			script := "#!/bin/sh\nenv > \"" + record + "\"\n"
			if tc.stdout != "" {
				script += "echo '" + tc.stdout + "'\n"
			}
			if err := os.WriteFile(filepath.Join(dir, tc.tool), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(credential, value)
			// Set here rather than assumed. HOME and the XDG variables are
			// what this claim is really about, and asserting on the ambient
			// ones would make the test fail in a container that exports none
			// -- which says nothing about the filter.
			t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
			t.Setenv("HELM_REGISTRY_CONFIG", filepath.Join(dir, "registry.json"))
			t.Cleanup(func() { childenv.Prime() })
			childenv.Prime(credential)

			tc.run(t, dir)

			raw, err := os.ReadFile(record)
			if err != nil {
				// The self-check: no record means the tool never ran, and
				// every assertion below would hold vacuously.
				t.Fatalf("the tool wrote no environment, so it did not run: %v", err)
			}
			env := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, credential+"=") }) {
				t.Errorf("%s ran holding a credential it has no use for.\n"+
					"It renders from a registry over somebody else's network, and a plugin, a "+
					"hook or a debug flag reads its environment for free.", tc.tool)
			}
			// And it is still an environment. A denylist that took one
			// variable too many breaks a render in a deployment nobody here
			// can see, with a symptom that says nothing about the environment.
			for _, want := range []string{"PATH=", "XDG_CACHE_HOME=", "HELM_REGISTRY_CONFIG="} {
				if !slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, want) }) {
					t.Errorf("%s did not reach %s", strings.TrimSuffix(want, "="), tc.tool)
				}
			}
		})
	}
}
