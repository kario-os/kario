package sdk

import (
	"encoding/json"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// testV1 is a made-up phone.ui/1, as the Shell's tests have it.
var testV1 = ui.ProtoForm{Major: 1, Rules: []ui.ProtoRule{
	{T: ui.MsgToast, As: "notice", Fields: map[string]string{"text": "body"}},
	{T: ui.MsgResync, As: "resend"},
	{Fields: map[string]string{"screen_id": "page"}},
}}

// An app that speaks a second major offers both in hello (proto the
// oldest), holds what it sends until start names one, and then speaks it:
// the major start names, the oldest when start names none (a system that
// does not negotiate). Without other majors hello is as it always was and
// nothing is held.
func TestProtoMajors(t *testing.T) {
	forms, _ := json.Marshal([]ui.ProtoForm{testV1})
	for _, tc := range []struct {
		name, env, start string
		hello, toast     string
	}{
		{"one major", "", ui.Proto, `{"t":"hello","app":"com.test.app","proto":"phone.ui/0"}`, ""},
		{"a system of phone.ui/1", string(forms), "phone.ui/1",
			`{"t":"hello","app":"com.test.app","proto":"phone.ui/0","protos":["phone.ui/1","phone.ui/0"]}`, `{"body":"early","t":"notice"}`},
		{"a system of phone.ui/0", string(forms), ui.Proto,
			`{"t":"hello","app":"com.test.app","proto":"phone.ui/0","protos":["phone.ui/1","phone.ui/0"]}`, `{"t":"toast","text":"early"}`},
		{"a system that does not negotiate", string(forms), "",
			`{"t":"hello","app":"com.test.app","proto":"phone.ui/0","protos":["phone.ui/1","phone.ui/0"]}`, `{"t":"toast","text":"early"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			shell := wire.New(os.NewFile(uintptr(fds[1]), "shell"))
			defer shell.Close()
			t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
			t.Setenv("PHONE_APP_ID", "com.test.app")
			t.Setenv(ui.EnvTestForms, tc.env)
			a, err := New()
			if err != nil {
				t.Fatal(err)
			}
			a.OnStart = func(string) { a.Reset(ui.Screen{ID: "main", Title: "started"}) }
			a.OnAction = func(e ActionEvent) { a.Update(ui.Screen{ID: "main", Title: "action on " + e.Screen}) }
			a.OnResync = func() { a.Update(ui.Screen{ID: "main", Title: "resynced"}) }
			if tc.toast != "" {
				a.Toast("early")
			}
			go func() { _ = a.Run() }()
			line := func() string {
				t.Helper()
				var raw json.RawMessage
				if err := shell.Recv(&raw); err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			if got := line(); got != tc.hello {
				t.Fatalf("hello %s, want %s", got, tc.hello)
			}
			start := map[string]any{"t": ui.MsgStart}
			if tc.start != "" {
				start["proto"] = tc.start
			}
			_ = shell.Send(start)
			if got := tc.toast; got != "" {
				if got = line(); got != tc.toast {
					t.Fatalf("the toast held for start: %s, want %s", got, tc.toast)
				}
			}
			title := func(want string) {
				t.Helper()
				var m ui.Msg
				if err := json.Unmarshal([]byte(line()), &m); err != nil || m.Screen == nil || m.Screen.Title != want {
					t.Fatalf("%+v %v, want the title %q", m, err, want)
				}
			}
			title("started")
			v1 := tc.start == "phone.ui/1"
			action, resync := map[string]any{"t": ui.MsgAction, "screen_id": "main"}, map[string]any{"t": ui.MsgResync}
			if v1 {
				action, resync = map[string]any{"t": ui.MsgAction, "page": "main"}, map[string]any{"t": "resend"}
			}
			_ = shell.Send(action)
			title("action on main")
			_ = shell.Send(resync)
			title("action on main")
			title("resynced")
		})
	}
}
