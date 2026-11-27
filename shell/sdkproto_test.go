package shell

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// sdkGoApp is an app of the Go SDK for TestSDKProtoMajors, as the C one
// (testdata/sdkproto/app.c) and the TypeScript one (main.js) are.
func sdkGoApp() int {
	app, err := sdk.New()
	if err != nil {
		return 2
	}
	app.OnStart = func(string) {
		app.Reset(ui.Screen{ID: "main", Title: "started"})
		app.Toast("hello from go")
	}
	app.OnAction = func(e sdk.ActionEvent) { app.Update(ui.Screen{ID: "main", Title: "action on " + e.Screen}) }
	app.OnResync = func() { app.Update(ui.Screen{ID: "main", Title: "resynced"}) }
	_ = app.Run()
	return 0
}

// sdkApps writes an app of each SDK that this machine can run into apps
// and bin: Go (this test binary), C (built here from sdk/c), TypeScript on
// kario-jsrt and on Node.js; the rest are logged and left out.
func sdkApps(t *testing.T, apps, bin string) []string {
	t.Helper()
	write := func(id, manifest string, files map[string]string) {
		dir := filepath.Join(apps, id)
		_ = os.MkdirAll(dir, 0o755)
		files["manifest.json"] = manifest
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	program := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src, _ := filepath.Abs("..")
	mainJS, err := os.ReadFile(filepath.Join("testdata", "sdkproto", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	program(fakeAppExec, "#!/bin/sh\nexec "+strconv.Quote(os.Args[0])+" -test.run='^TestFakeApp$'\n")
	write("test.sdkgo", `{"id":"test.sdkgo","name":"go","exec":"`+fakeAppExec+`"}`, map[string]string{})
	ids = append(ids, "test.sdkgo")
	if cc, err := exec.LookPath("cc"); err != nil {
		t.Log("no C compiler: the C SDK is left out")
	} else if out, err := exec.Command(cc, "-std=c11", "-I", filepath.Join(src, "sdk", "c"), "-o", filepath.Join(bin, "sdk-c-app"),
		filepath.Join("testdata", "sdkproto", "app.c"), filepath.Join(src, "sdk", "c", "phoneui.c")).CombinedOutput(); err != nil {
		t.Fatalf("building the C app: %v\n%s", err, out)
	} else {
		write("test.sdkc", `{"id":"test.sdkc","name":"c","exec":"sdk-c-app"}`, map[string]string{})
		ids = append(ids, "test.sdkc")
	}
	if rt, _ := filepath.Abs(filepath.Join("..", "build", "jsrt", jsrt.Program)); fileExists(rt) {
		_ = os.Symlink(rt, filepath.Join(bin, jsrt.Program))
		write("test.sdkjsrt", `{"id":"test.sdkjsrt","name":"jsrt","runtime":"js"}`, map[string]string{jsrt.DefaultMain: string(mainJS)})
		ids = append(ids, "test.sdkjsrt")
	} else {
		t.Log("no kario-jsrt (make jsrt): it is left out")
	}
	if node, err := exec.LookPath("node"); err != nil || exec.Command(node, "-e", `process.exit(typeof require("node:module").registerHooks === "function" ? 0 : 1)`).Run() != nil {
		t.Log("no Node.js with module.registerHooks: it is left out")
	} else {
		dir := filepath.Join(apps, "test.sdknode")
		program("sdk-node-app", "#!/bin/sh\nexec "+strconv.Quote(node)+" "+strconv.Quote(filepath.Join(src, "sdk", "ts", "node", "run.mjs"))+" "+
			strconv.Quote(filepath.Join(dir, jsrt.DefaultMain))+"\n")
		write("test.sdknode", `{"id":"test.sdknode","name":"node","exec":"sdk-node-app"}`, map[string]string{jsrt.DefaultMain: string(mainJS)})
		ids = append(ids, "test.sdknode")
	}
	return ids
}

// An app whose SDK speaks two majors (its own and the made-up phone.ui/1,
// given as ui.EnvTestForms) talks to a Shell of either: one that speaks
// phone.ui/0 only gets phone.ui/0, one in the transition the newer
// phone.ui/1, and either way its toast arrives, it reads the page of an
// action and answers a resync. The Go, C and both TypeScript SDKs (the
// ones this machine can run).
func TestSDKProtoMajors(t *testing.T) {
	if !alone(t) {
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("the apps run without phone-sandbox, which root refuses")
	}
	forms, _ := json.Marshal([]ui.ProtoForm{fakeV1Form})
	for _, tc := range []struct {
		name   string
		protos []protoVersion
		want   string
	}{
		{"a Shell of phone.ui/0", nil, ui.Proto},
		{"a Shell in the transition to phone.ui/1", transition, "phone.ui/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			apps, bin := filepath.Join(root, "apps"), filepath.Join(root, "bin")
			_ = os.MkdirAll(bin, 0o755)
			ids := sdkApps(t, apps, bin)
			run, err := os.MkdirTemp("", "sdkproto-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(run) })
			c, err := New(Config{W: 40, H: 24, AppsDir: apps, AppBinDir: bin, RunDir: run, StateDir: filepath.Join(root, "state"),
				Log: io.Discard, FreezeAfter: -1, AppEnv: []string{fakeAppEnv + "=1", ui.EnvTestForms + "=" + string(forms)}})
			if err != nil {
				t.Fatal(err)
			}
			stop := make(chan struct{})
			go func() {
				for {
					select {
					case f := <-c.Events():
						f()
					case <-stop:
						return
					}
				}
			}()
			t.Cleanup(func() {
				_ = c.onLoop(func() error { c.Shutdown(); return nil })
				close(stop)
			})
			c.protos = tc.protos
			for _, id := range ids {
				var a *App
				_ = c.onLoop(func() error { a = c.appByID(id); c.Launch(a, ""); return nil })
				until(t, c, id+"'s page", func() bool { return title(a) == "started" })
				_ = c.onLoop(func() error {
					if got := c.spoken(a); got != tc.want {
						t.Errorf("%s speaks %s, want %s", id, got, tc.want)
					}
					return nil
				})
				lang := strings.TrimPrefix(id, "test.sdk")
				if lang == "jsrt" || lang == "node" {
					lang = "js"
				}
				until(t, c, id+"'s toast", func() bool { return c.toast != nil && strings.Contains(c.toast.body, "hello from "+lang) })
				_ = c.onLoop(func() error { a.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "open"}); return nil })
				until(t, c, id+"'s answer to an action", func() bool { return title(a) == "action on main" })
				_ = c.onLoop(func() error { a.send(ui.Msg{T: ui.MsgResync}); return nil })
				until(t, c, id+"'s answer to resync", func() bool { return title(a) == "resynced" })
				_ = c.onLoop(func() error {
					if a.guard.bad != 0 {
						t.Errorf("%s: %d malformed messages", id, a.guard.bad)
					}
					c.kill(a)
					return nil
				})
				until(t, c, id+" ended", func() bool { return !a.running })
			}
		})
	}
}
