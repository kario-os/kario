//go:build kariocover

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The coverage build's child: started before TestMain runs, as the
// helper's.
func init() {
	if dir := os.Getenv("SANDBOX_TEST_COVER"); dir != "" {
		runtime.LockOSThread()
		coverChild(dir)
		os.Exit(0)
	}
}

func coverChild(dir string) {
	data, cover := filepath.Join(dir, "data"), filepath.Join(dir, "cover")
	exe, _ := os.Executable()
	applied, err := Apply(Policy{DataDir: data, Code: []string{"/usr", "/lib", "/lib64", "/bin", filepath.Dir(exe)}})
	if err != nil {
		fmt.Println("apply:", err)
		os.Exit(1)
	}
	fmt.Println("applied=" + strings.Join(applied, ","))
	script := filepath.Join(cover, "run.sh")
	fmt.Println("write-cover=" + errName(os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700)))
	fmt.Println("exec-cover=" + errName(exec.Command(script).Run()))
}

// Under Landlock a coverage build's app writes into $GOCOVERDIR and runs
// nothing from there.
func TestCoverDir(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"data", "cover"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SANDBOX_TEST_COVER="+dir, "GOCOVERDIR="+filepath.Join(dir, "cover"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	if !ParseApplied(got["applied"])["landlock"] {
		t.Skipf("no Landlock here (%s): nothing to let through", got["applied"])
	}
	for k, v := range map[string]string{"write-cover": "ok", "exec-cover": "EACCES"} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q\n%s", k, got[k], v, out)
		}
	}
}
