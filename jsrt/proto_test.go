package jsrt

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

const protoApp = `import { on, reset, update, toast } from "kario";
toast("early");
on("start", () => reset({ id: "main", title: "started", body: [] }));
on("action", (e) => update({ id: "main", title: "action on " + e.screen, body: [] }));
on("resync", () => update({ id: "main", title: "resynced", body: [] }));
`

// TestProtoMajors: an app given a second major (a made-up phone.ui/1,
// ui.EnvTestForms) offers both in hello, holds what it sends until start
// names one, then speaks it both ways: phone.ui/1's form (a toast is a
// notice, the page of an action is "page", resync is resend), or its own
// with phone.ui/0 or a start that names none.
func TestProtoMajors(t *testing.T) { testProtoMajors(t, []string{binary(t)}) }

func TestNodeProtoMajors(t *testing.T) { testProtoMajors(t, nodeArgv(t)) }

func testProtoMajors(t *testing.T, argv []string) {
	v1 := ui.ProtoForm{Major: 1, Rules: []ui.ProtoRule{
		{T: ui.MsgToast, As: "notice", Fields: map[string]string{"text": "body"}},
		{T: ui.MsgResync, As: "resend"},
		{Fields: map[string]string{"screen_id": "page"}},
	}}
	forms, _ := json.Marshal([]ui.ProtoForm{v1})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultMain), []byte(protoApp), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, proto := range []string{"phone.ui/1", ui.Proto, ""} {
		t.Run("start "+proto, func(t *testing.T) {
			parent, child := socketPair(t)
			cmd := exec.Command(argv[0], append(argv[1:], filepath.Join(dir, DefaultMain))...)
			cmd.ExtraFiles = []*os.File{child}
			cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=com.example.test", "PHONE_LANG=en", "HOME=" + dir, "PATH=" + os.Getenv("PATH"),
				ui.EnvTestForms + "=" + string(forms)}
			cmd.Stderr = os.Stderr
			sanitize(t, cmd, nil)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			child.Close()
			t.Cleanup(func() {
				// Close alone waits for the reader below to return.
				_ = syscall.Shutdown(int(parent.Fd()), syscall.SHUT_RDWR)
				parent.Close()
				gone := make(chan struct{})
				go func() { _ = cmd.Wait(); close(gone) }()
				leave(t, cmd, gone)
				<-gone
			})
			lines := make(chan map[string]any, 16)
			go func() {
				r := bufio.NewReader(parent)
				for {
					l, err := r.ReadBytes('\n')
					if err != nil {
						close(lines)
						return
					}
					var m map[string]any
					_ = json.Unmarshal(l, &m)
					lines <- m
				}
			}()
			next := func() map[string]any {
				t.Helper()
				select {
				case m, ok := <-lines:
					if !ok {
						t.Fatal("the app closed its connection")
					}
					return m
				case <-time.After(10 * time.Second):
					t.Fatal("nothing from the app")
				}
				return nil
			}
			send := func(m map[string]any) {
				raw, _ := json.Marshal(m)
				if _, err := parent.Write(append(raw, '\n')); err != nil {
					t.Fatal(err)
				}
			}
			title := func(t1, want string) {
				t.Helper()
				m := next()
				s, _ := m["screen"].(map[string]any)
				if m["t"] != t1 || s == nil || s["title"] != want {
					t.Fatalf("%v, want %s with the title %q", m, t1, want)
				}
			}
			hello := next()
			if hello["t"] != ui.MsgHello || hello["proto"] != ui.Proto || !reflect.DeepEqual(hello["protos"], []any{"phone.ui/1", ui.Proto}) {
				t.Fatalf("hello %v", hello)
			}
			start := map[string]any{"t": ui.MsgStart}
			if proto != "" {
				start["proto"] = proto
			}
			send(start)
			isV1 := proto == "phone.ui/1"
			toast, action, resync := map[string]any{"t": "toast", "text": "early"}, map[string]any{"t": "action", "screen_id": "main"}, map[string]any{"t": "resync"}
			if isV1 {
				toast, action, resync = map[string]any{"t": "notice", "body": "early"}, map[string]any{"t": "action", "page": "main"}, map[string]any{"t": "resend"}
			}
			if m := next(); !reflect.DeepEqual(m, toast) {
				t.Fatalf("the toast held for start: %v, want %v", m, toast)
			}
			title(ui.MsgReset, "started")
			send(action)
			title(ui.MsgUpdate, "action on main")
			send(resync)
			title(ui.MsgReset, "action on main")
			title(ui.MsgUpdate, "resynced")
		})
	}
}
