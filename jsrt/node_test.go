package jsrt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/jsrt/nativetest"
	sdkts "github.com/kario-os/kario/sdk/ts"
	"github.com/kario-os/kario/ui"
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

// Storage under Node.js writes in the background and coalesces as
// kario-jsrt does, with the same waits before leaving and the same errors.
func TestNodeStorageSlowWrite(t *testing.T)        { testStorageSlowWrite(t, nodeArgv(t)) }
func TestNodeStorageTimerDuringWrite(t *testing.T) { testStorageTimerDuringWrite(t, nodeArgv(t)) }
func TestNodeStorageCoalesces(t *testing.T)        { testStorageCoalesces(t, nodeArgv(t)) }
func TestNodeStorageDurable(t *testing.T)          { testStorageDurable(t, nodeArgv(t)) }
func TestNodeStorageWriteFails(t *testing.T)       { testStorageWriteFails(t, nodeArgv(t)) }

func nodeArgv(t *testing.T) []string {
	node, run := nodeRunner(t)
	return []string{node, run}
}

// nativeNodeEngine is nodeEngine with the runtime written out of the tree
// beside a kario-native.node built for it, as kario run does: the same
// native modules load under Node.js, with the same refusals.
func nativeNodeEngine(t *testing.T) engine {
	node, _ := nodeRunner(t)
	rt := t.TempDir()
	files, err := sdkts.Node()
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(rt, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := sdkts.BuildAddon(rt, nativetest.Compiler(t)); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(rt, "run.mjs")
	return func(t *testing.T, dataDir, lang, main string) *shell {
		return launch(t, dataDir, lang, node, run, main)
	}
}

func TestNodeNative(t *testing.T)        { testNative(t, nativeNodeEngine(t)) }
func TestNodeNativeRefused(t *testing.T) { testNativeRefused(t, nativeNodeEngine(t)) }

// Under kario run the reply's files come through kario-native.node's reader
// (Node's sockets drop them); a file the app drops is closed when the
// garbage collector gets to it, which the test does not wait for.
func TestNodeReplyFiles(t *testing.T) { testReplyFiles(t, nativeNodeEngine(t), false) }

// Without kario-native.node (no C compiler) a reply's files cannot reach
// Node.js: the call fails and says why.
func TestNodeReplyFilesWithoutAddon(t *testing.T) {
	s := nodeEngine(t)(t, t.TempDir(), "en", filepath.Join(appCopy(t, "files"), "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	s.send(action("pick"))
	req := s.expect(ui.MsgReq)
	s.sendFiles(ui.Msg{T: ui.MsgRes, Req: req.Req, OK: true, Data: json.RawMessage(`{"id":"media:p1"}`)}, fileWith(t, "x"))
	s.screenText("err files from replies reach Node.js through kario-native.node")
}

func TestNodeNotUnderShell(t *testing.T) {
	node, run := nodeRunner(t)
	cmd := exec.Command(node, run, filepath.Join(appCopy(t, "app"), "main.js"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "not started by phone-shell") {
		t.Fatalf("%v %s", err, out)
	}
}
