package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// oldKernel answers the probes as a kernel of the given version would.
func oldKernel(major, minor int) kernelCalls {
	at := func(ma, mi int) bool { return major > ma || major == ma && minor >= mi }
	errIf := func(missing bool, err error) error {
		if missing {
			return err
		}
		return nil
	}
	return kernelCalls{
		landlock: func() (int, error) {
			switch {
			case at(6, 12):
				return 6, nil
			case at(6, 7):
				return 4, nil
			case at(5, 13):
				return 1, nil
			}
			return 0, unix.ENOSYS
		},
		prctl: func(option int, arg2 uintptr) error {
			switch {
			case option == unix.PR_SET_SECCOMP && at(3, 5):
				return unix.EFAULT
			case option == unix.PR_GET_NO_NEW_PRIVS && at(3, 5):
				return nil
			}
			return unix.EINVAL
		},
		seccomp: func(op, flags uint, args unsafe.Pointer) error {
			switch {
			case at(4, 14):
				return nil
			case at(3, 17):
				return unix.EINVAL
			}
			return unix.ENOSYS
		},
		pidfd:     func() error { return errIf(!at(5, 3), unix.ENOSYS) },
		memfd:     func() error { return errIf(!at(3, 17), unix.ENOSYS) },
		namespace: func(string) bool { return true },
		status:    func() string { return "Name:\tphone-shell\nSeccomp:\t0\n" },
	}
}

func TestProbeKernel(t *testing.T) {
	for _, c := range []struct {
		major, minor int
		want         Kernel
	}{
		{3, 10, Kernel{SeccompFilter: true, NoNewPrivs: true}},
		{4, 9, Kernel{SeccompFilter: true, SeccompTSync: true, NoNewPrivs: true, MemfdSeal: true}},
		{5, 4, Kernel{SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, Pidfd: true, MemfdSeal: true}},
		{5, 15, Kernel{Landlock: 1, SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, Pidfd: true, MemfdSeal: true}},
		{6, 18, Kernel{Landlock: 6, SeccompFilter: true, SeccompTSync: true, KillProcess: true, NoNewPrivs: true, Pidfd: true, MemfdSeal: true}},
	} {
		if got := probeKernel(oldKernel(c.major, c.minor)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Linux %d.%d: %+v, want %+v", c.major, c.minor, got, c.want)
		}
	}
	// No filters at all (CONFIG_SECCOMP_FILTER off), Landlock built but
	// not in lsm=, a namespace missing.
	k := oldKernel(6, 1)
	k.prctl = func(option int, _ uintptr) error {
		if option == unix.PR_SET_SECCOMP {
			return unix.EINVAL
		}
		return nil
	}
	k.landlock = func() (int, error) { return 0, unix.EOPNOTSUPP }
	k.namespace = func(kind string) bool { return kind != "pid" }
	got := probeKernel(k)
	if got.SeccompFilter || got.SeccompTSync || got.Landlock != 0 || !got.LandlockOff || !reflect.DeepEqual(got.NoNamespaces, []string{"pid"}) {
		t.Errorf("no filters, Landlock off, no pid namespace: %+v", got)
	}
	// A shell under its service manager's filter: prctl may be denied, the
	// filter mode shows in its status.
	k.prctl = func(int, uintptr) error { return unix.EINVAL }
	k.status = func() string { return "Name:\tphone-shell\nSeccomp:\t2\nSeccomp_filters:\t1\n" }
	if !probeKernel(k).SeccompFilter {
		t.Error("filter mode in /proc/self/status not taken for filter support")
	}
	// The real kernel: whatever it has, probing changes nothing.
	real := ProbeKernel()
	t.Logf("this kernel: %+v", real)
	if v, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0); err == nil && v != 0 && !strings.Contains(realCalls.status(), "Seccomp:\t2") {
		t.Errorf("probing left seccomp mode %d", v)
	}
}

// Without seccomp(2) (before Linux 3.17) the launcher installs its filter
// with prctl, and the program it execs runs under it, on every thread.
func TestSeccompWithoutTSync(t *testing.T) {
	var tsync, prctl int
	saved, savedPrctl := seccompTSync, prctlFilter
	defer func() { seccompTSync, prctlFilter = saved, savedPrctl }()
	seccompTSync = func(*unix.SockFprog) error { tsync++; return unix.ENOSYS }
	prctlFilter = func(p *unix.SockFprog) error {
		prctl++
		if p.Len == 0 || p.Filter == nil {
			return unix.EINVAL
		}
		return nil
	}
	if err := seccomp(); err != nil || tsync != 1 || prctl != 1 {
		t.Fatalf("seccomp() = %v after %d seccomp(2), %d prctl", err, tsync, prctl)
	}
	// Any other failure is not papered over.
	seccompTSync = func(*unix.SockFprog) error { return unix.EACCES }
	if err := seccomp(); !errors.Is(err, unix.EACCES) || prctl != 1 {
		t.Fatalf("EACCES from seccomp(2): %v, %d prctl", err, prctl)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SANDBOX_TEST_PRCTL="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if s := string(out); !strings.Contains(s, "applied=") || !strings.Contains(s, "keyctl=EPERM×8") || !strings.Contains(s, "unshare=EPERM") {
		t.Fatalf("after exec:\n%s", s)
	}
}

// prctlChild applies the sandbox as on a kernel without seccomp(2), then
// execs itself as the launcher execs an app.
func prctlChild(dir string) {
	seccompTSync = func(*unix.SockFprog) error { return unix.ENOSYS }
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o700)
	exe, _ := os.Executable()
	applied, err := Apply(Policy{DataDir: data, Code: []string{"/usr", "/lib", "/lib64", filepath.Dir(exe)}, ReadOnly: []string{"/etc", "/proc"}})
	if err != nil {
		fmt.Println("apply:", err)
		os.Exit(1)
	}
	fmt.Println("applied=" + strings.Join(applied, ","))
	env := slices.DeleteFunc(append(os.Environ(), "SANDBOX_TEST_EXECED=1"), func(e string) bool {
		return strings.HasPrefix(e, "SANDBOX_TEST_PRCTL=")
	})
	fmt.Println("exec:", syscall.Exec(exe, []string{exe, "-test.run=^$"}, env))
	os.Exit(1)
}

// execedChild runs in the program the filtered thread exec'd: every
// thread the runtime starts has the filter.
func execedChild() {
	var wg sync.WaitGroup
	denied := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := unix.KeyctlInt(unix.KEYCTL_GET_KEYRING_ID, unix.KEY_SPEC_SESSION_KEYRING, 0, 0, 0)
			denied <- errors.Is(err, unix.EPERM)
		}()
	}
	wg.Wait()
	close(denied)
	n := 0
	for d := range denied {
		if d {
			n++
		}
	}
	fmt.Printf("keyctl=EPERM×%d\n", n)
	if errors.Is(unix.Unshare(unix.CLONE_NEWUSER), unix.EPERM) {
		fmt.Println("unshare=EPERM")
	}
}
