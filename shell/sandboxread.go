package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sandboxRead is the step sandbox-read <app-id> <path> allowed|denied: a
// shell started in the app's sandbox (its policy, uid and namespaces, as
// appCommand gives them) reads path. Where Landlock is not in force (an old
// kernel on a developer machine) the step says so and passes: the rules it
// checks are Landlock's.
func (r *Runner) sandboxRead(arg string) error {
	f := strings.Fields(arg)
	if len(f) != 3 || f[2] != "allowed" && f[2] != "denied" {
		return errors.New("usage: sandbox-read <app-id> <path> allowed|denied")
	}
	a := r.C.appByID(f[0])
	if a == nil {
		return fmt.Errorf("no app %s", f[0])
	}
	launcher := filepath.Join(r.C.cfg.AppBinDir, "phone-sandbox")
	if _, err := os.Stat(launcher); err != nil {
		fmt.Fprintf(r.Out, "  sandbox-read: no phone-sandbox, skipped\n")
		return nil
	}
	const sh = "/bin/sh"
	p, attr, err := r.C.appPolicy(a, filepath.Join(r.C.cfg.StateDir, "apps", a.M.ID), sh, nil)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	script := `case ",$PHONE_SANDBOX," in *,landlock,*) ;; *) echo no-landlock; exit 0;; esac; cat "$1" >/dev/null && echo read`
	cmd := exec.Command(launcher, string(raw), sh, "-c", script, "sh", f[1])
	cmd.SysProcAttr = attr
	out, _ := cmd.CombinedOutput()
	said := strings.TrimSpace(string(out))
	switch {
	case strings.HasSuffix(said, "no-landlock"):
		fmt.Fprintf(r.Out, "  sandbox-read: no Landlock in %s's sandbox, skipped\n", f[0])
		return nil
	case strings.HasSuffix(said, "read") != (f[2] == "allowed"):
		return fmt.Errorf("%s in %s's sandbox: %q, want %s", f[1], f[0], said, f[2])
	}
	return nil
}
