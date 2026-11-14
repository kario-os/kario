package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The checks run in a child process: a sandbox cannot be lifted again.
func TestMain(m *testing.M) {
	if dir := os.Getenv("SANDBOX_TEST_CHILD"); dir != "" {
		runtime.LockOSThread()
		child(dir)
		return
	}
	os.Exit(m.Run())
}

func child(dir string) {
	data := filepath.Join(dir, "data")
	outside := filepath.Join(dir, "outside")
	_ = os.MkdirAll(data, 0o700)
	_ = os.MkdirAll(outside, 0o700)
	exe, _ := os.Executable()
	applied, err := Apply(Policy{DataDir: data, Code: []string{"/usr", "/lib", "/lib64", "/bin", filepath.Dir(exe)}, ReadOnly: []string{"/etc", "/proc"}})
	if err != nil {
		fmt.Println("apply:", err)
		os.Exit(1)
	}
	fmt.Println("applied=" + strings.Join(applied, ","))
	report := func(name string, err error) {
		switch {
		case err == nil:
			fmt.Println(name + "=ok")
		case errors.Is(err, unix.EPERM):
			fmt.Println(name + "=EPERM")
		case errors.Is(err, unix.EACCES):
			fmt.Println(name + "=EACCES")
		default:
			fmt.Println(name + "=" + err.Error())
		}
	}
	_, err = unix.KeyctlInt(unix.KEYCTL_GET_KEYRING_ID, unix.KEY_SPEC_SESSION_KEYRING, 0, 0, 0)
	report("keyctl", err)
	report("unshare", unix.Unshare(unix.CLONE_NEWUSER))
	_, err = unix.Mmap(-1, 0, 4096, unix.PROT_READ, unix.MAP_ANON|unix.MAP_PRIVATE)
	report("mmap", err)
	report("write-data", os.WriteFile(filepath.Join(data, "f"), []byte("x"), 0o600))
	report("write-outside", os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o600))
	script := filepath.Join(data, "run.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	report("exec-data", exec.Command(script).Run())
	report("exec-system", exec.Command("/bin/true").Run())
}

func TestApply(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SANDBOX_TEST_CHILD="+t.TempDir())
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
	t.Logf("applied: %s", got["applied"])
	want := map[string]string{"keyctl": "EPERM", "unshare": "EPERM", "mmap": "ok", "write-data": "ok", "exec-system": "ok"}
	if ParseApplied(got["applied"])["landlock"] {
		want["write-outside"] = "EACCES"
		want["exec-data"] = "EACCES"
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q\n%s", k, got[k], v, out)
		}
	}
}
