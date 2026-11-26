package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The Shell's event array, finished or cut short, becomes Chrome's JSON
// object format; the runner and the Shell are asked for the profile.
func TestFinishTrace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "trace.json")
	for _, body := range []string{"[\n{\"name\":\"a\",\"ph\":\"i\",\"ts\":1,\"pid\":1,\"tid\":1},\n{\"name\":\"b\",\"ph\":\"i\",\"ts\":2,\"pid\":1,\"tid\":1}\n]\n",
		"[\n{\"name\":\"a\",\"ph\":\"i\",\"ts\":1,\"pid\":1,\"tid\":1},\n{\"name\":\"b\",\"ph\":\"i\",\"ts\":2,\"pid\":1,\"tid\":1}"} {
		_ = os.WriteFile(rawTrace(file), []byte(body), 0o644)
		n, err := finishTrace(rawTrace(file), file, map[string]string{"app": "x"})
		raw, _ := os.ReadFile(file)
		var tf traceFile
		if err != nil || n != 2 || json.Unmarshal(raw, &tf) != nil || len(tf.TraceEvents) != 2 || tf.OtherData["app"] != "x" || tf.DisplayTimeUnit != "ms" {
			t.Fatalf("%d %v %s", n, err, raw)
		}
		if _, err := os.Stat(rawTrace(file)); err == nil {
			t.Fatal("the Shell's file was left")
		}
	}
	if _, err := finishTrace(rawTrace(file), file, nil); err == nil {
		t.Fatal("no timeline")
	}
	o := runOptions{profile: "/w/out/trace.json"}
	if r := strings.Join(nodeRunner("/n/node", "/rt", o), " "); r != "/n/node --enable-source-maps --cpu-prof --cpu-prof-dir=/w/out/trace.cpuprofiles /rt/run.mjs" {
		t.Fatalf("runner %q", r)
	}
	if a := strings.Join(shellArgs("/w/a/build", nil, "", o), " "); a != "--dev-app /w/a/build --profile /w/out/trace.json.part" {
		t.Fatalf("args %q", a)
	}
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// kario run -profile on the developer machine's Shell: keys pressed in the
// preview, the run stopped as Ctrl-C stops it, then a trace with every kind
// of event (phone-paint's frames among them), and the app's JavaScript CPU
// profile.
func TestRunProfile(t *testing.T) {
	n := node(t)
	shell, paint := stagedShell(t)
	dir := newTestProject(t)
	replace(t, dir, "src/main.ts", `import { Button, Text, act, app, page, state, tr } from "@kario/ui";`,
		`import { Button, Text, act, app, call, page, state, tr } from "@kario/ui";`)
	replace(t, dir, "src/main.ts", `() => count.update((n) => n + 1)`, `async () => { count.update((n) => n + 1); await call("clipboard.set", { text: String(count.value) }); }`)
	file := filepath.Join(t.TempDir(), "out", "trace.json")
	addr := freeAddr(t)
	done := make(chan error, 1)
	go func() {
		done <- runApp(dir, runOptions{engine: "node", node: n, shell: shell, gui: paint, preview: addr, profile: file, device: "bar-t9"})
	}()
	up := false
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end) && !up; time.Sleep(100 * time.Millisecond) {
		if r, err := http.Get("http://" + addr + "/"); err == nil {
			r.Body.Close()
			up = true
		}
	}
	if !up {
		t.Fatal("the preview did not come up")
	}
	time.Sleep(3 * time.Second)
	for i := 0; i < 3; i++ {
		if r, err := http.PostForm("http://"+addr+"/key", url.Values{"key": {"enter"}}); err == nil {
			r.Body.Close()
		}
		time.Sleep(400 * time.Millisecond)
	}
	time.Sleep(time.Second)
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("kario run did not stop")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var tf struct {
		TraceEvents []struct {
			Name, Cat, Ph string
			Args          map[string]any
		} `json:"traceEvents"`
	}
	if err := json.Unmarshal(raw, &tf); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range tf.TraceEvents {
		kinds[e.Ph+" "+e.Cat+" "+strings.Fields(e.Name + " x")[0]]++
	}
	for _, want := range []string{"C sample CPU", "C sample memory", "X page reset", "b input action", "e input action",
		"b capability clipboard.set", "e capability clipboard.set", "X anr ping", "i lifecycle start",
		"X frame frame", "X frame layout", "X frame draw"} {
		if kinds[want] == 0 {
			t.Errorf("no %q in the trace: %v", want, kinds)
		}
	}
	drawn := false
	for _, e := range tf.TraceEvents {
		if e.Ph == "X" && e.Cat == "frame" && strings.HasPrefix(e.Name, "frame ") {
			if n, _ := e.Args["draw_steps"].(float64); n > 0 && e.Args["screen"] != "" {
				drawn = true
			}
		}
	}
	if !drawn {
		t.Error("no renderer frame with its drawing steps and screen")
	}
	if profiles := cpuProfiles(cpuProfileDir(file), time.Time{}); len(profiles) == 0 {
		t.Error("no CPU profile from Node.js")
	} else if raw, _ := os.ReadFile(profiles[0]); !strings.Contains(string(raw), `"nodes"`) {
		t.Errorf("%s: %.100s", profiles[0], raw)
	}
	fmt.Fprintln(os.Stderr, "trace kinds:", kinds)
}
