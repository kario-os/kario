package jsrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// With BudgetEnv set, a turn that runs on is interrupted (no catch sees
// it) and said so on stderr; the next turn runs again. Unset, nothing is
// interrupted.
func TestBudget(t *testing.T) {
	bin := binary(t)
	dir := t.TempDir()
	main := filepath.Join(dir, DefaultMain)
	src := `import { on, toast } from "kario";
on("action", (e) => {
  if (e.action === "spin") {
    const t0 = Date.now();
    try { while (Date.now() - t0 < 2000) {} } catch (x) { toast("caught"); }
    toast("spun");
  } else {
    toast("alive");
  }
});
`
	if err := os.WriteFile(main, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []string{"100", ""} {
		sh := launch(t, dir, "en", "/usr/bin/env", BudgetEnv+"="+budget, bin, "--no-bytecode", main)
		sh.hello("", nil)
		t0 := time.Now()
		sh.send(ui.Msg{T: ui.MsgAction, Action: "spin"})
		sh.send(ui.Msg{T: ui.MsgAction, Action: "ping"})
		var got []string
		for len(got) == 0 || got[len(got)-1] != "alive" {
			got = append(got, sh.expect(ui.MsgToast).Text)
		}
		took := time.Since(t0)
		// The interruption is said on stderr, the toasts come over the
		// socket: the log line may come a little after them.
		for end := time.Now().Add(2 * time.Second); budget != "" && !strings.Contains(sh.log(), Interrupted) && time.Now().Before(end); {
			time.Sleep(10 * time.Millisecond)
		}
		if budget != "" {
			if len(got) != 1 || took > time.Second || !strings.Contains(sh.log(), Interrupted) {
				t.Fatalf("budget %s ms: toasts %v after %v; log:\n%s", budget, got, took, sh.log())
			}
		} else if strings.Join(got, ",") != "spun,alive" || strings.Contains(sh.log(), Interrupted) {
			t.Fatalf("no budget: toasts %v; log:\n%s", got, sh.log())
		}
	}
}
