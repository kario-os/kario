package jsrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// The Shell holds the user's next keys until the app answers the ping it
// sends after an action (shell/input.go), so the answer comes only after
// the action's handler and the jobs it queued, even when the action and the
// ping are read together; otherwise a key pressed next (OK after # in the
// calendar) acts on the page the action is replacing.
func TestPingAnsweredAfterTheAction(t *testing.T) {
	bin := binary(t)
	dir := t.TempDir()
	main := filepath.Join(dir, DefaultMain)
	src := `import { on, toast } from "kario";
on("action", (e) => {
  const t0 = Date.now();
  while (Date.now() - t0 < 300) {}
  Promise.resolve().then(() => toast("handled " + e.action));
});
`
	if err := os.WriteFile(main, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := launch(t, dir, "en", bin, "--no-bytecode", main)
	sh.hello("", nil)
	// While the first action's handler runs, the second and its ping both
	// arrive, and the runtime reads them together.
	sh.send(ui.Msg{T: ui.MsgAction, Action: "busy"})
	time.Sleep(50 * time.Millisecond)
	sh.send(ui.Msg{T: ui.MsgAction, Action: "next"})
	sh.send(ui.Msg{T: ui.MsgPing, Value: "7"})
	var order []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-sh.msgs:
			if !ok {
				t.Fatalf("the app exited; log:\n%s", sh.log())
			}
			switch m.T {
			case ui.MsgToast:
				order = append(order, m.Text)
			case ui.MsgPong:
				order = append(order, "pong "+m.Value)
				if want := "handled busy,handled next,pong 7"; strings.Join(order, ",") != want {
					t.Fatalf("got %s, want %s", strings.Join(order, ","), want)
				}
				return
			}
		case <-timeout:
			t.Fatalf("no pong; got %v; log:\n%s", order, sh.log())
		}
	}
}
