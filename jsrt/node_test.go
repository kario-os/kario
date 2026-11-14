package jsrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nodeRunner is Node.js and the runtime of sdk/ts/node (what kario run
// starts). Without a node that has synchronous module hooks the cases are
// skipped.
func nodeRunner(t *testing.T) (node, run string) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	if out, err := exec.Command(node, "-e", `process.exit(typeof require("node:module").registerHooks === "function" ? 0 : 1)`).CombinedOutput(); err != nil {
		t.Skipf("node without module.registerHooks (22.15 or newer): %v %s", err, out)
	}
	if run, err = filepath.Abs(filepath.Join("..", "sdk", "ts", "node", "run.mjs")); err != nil {
		t.Fatal(err)
	}
	return node, run
}

func nodeEngine(t *testing.T) engine {
	node, run := nodeRunner(t)
	return func(t *testing.T, dataDir, lang, main string) *shell {
		return launch(t, dataDir, lang, node, run, main)
	}
}

// The runtime's cases hold on Node.js as on kario-jsrt (29-app-development.md §6:
// an app runs on both engines). Node.js is not a sandbox, so the escape case
// stays kario-jsrt's; a static import of another module still keeps the app
// from starting.
func TestNodeRuntime(t *testing.T)       { testRuntime(t, nodeEngine(t)) }
func TestNodeTranslations(t *testing.T)  { testTranslations(t, nodeEngine(t)) }
func TestNodeStaticImports(t *testing.T) { testStaticImports(t, nodeEngine(t)) }

func TestNodeNotUnderShell(t *testing.T) {
	node, run := nodeRunner(t)
	cmd := exec.Command(node, run, filepath.Join(appCopy(t, "app"), "main.js"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "not started by phone-shell") {
		t.Fatalf("%v %s", err, out)
	}
}
