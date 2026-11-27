package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// listenChild runs the TCP check in a network namespace of its own, as an
// app without the network permission has, under the layers each line
// names.
func listenChild() {
	for _, applied := range []string{"ns,nonet,seccomp", "ns,nonet,landlock,landlock-v4,seccomp"} {
		_ = os.Setenv(Env, applied)
		for _, ch := range SelfChecks(os.TempDir()) {
			if ch.Name != tr.T("Listen on a TCP port") {
				continue
			}
			got := "allowed"
			if err := ch.Try(); err != nil {
				got = "denied by " + DeniedBy(err)
			}
			fmt.Printf("%s=%s\n", applied, got)
		}
	}
}

// Listening on a TCP port without the network: binding needs no interface
// up, so without Landlock's network rules the bind goes through (Nura's
// kernel, the developer machine's); the app's own namespace, its loopback
// down, is what keeps everyone from the port, and the check says so. Where
// the launcher applied Landlock ABI 4, the bind must be refused, so one
// that goes through fails whatever the namespace makes of it. A port that
// can be reached is allowed.
func TestListenTCPCheck(t *testing.T) {
	if err := listenTCP(false); err != nil {
		t.Errorf("a port on a loopback that is up: %v, want it allowed", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SANDBOX_TEST_LISTEN=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err != nil && !errors.As(err, &exit):
		t.Skipf("no network namespace for the test: %v", err)
	case err != nil:
		t.Fatalf("child: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	for applied, want := range map[string]string{"ns,nonet,seccomp": "denied by nonet", "ns,nonet,landlock,landlock-v4,seccomp": "allowed"} {
		if got[applied] != want {
			t.Errorf("%s: %q, want %q\n%s", applied, got[applied], want, out)
		}
	}
}

func TestLandlockABIApplied(t *testing.T) {
	for s, want := range map[string]int{"": 0, "ns,landlock,landlock-v6,seccomp": 6, "landlock,landlock-v3": 3, "landlock-vx": 0} {
		if got := ParseApplied(s).LandlockABI(); got != want {
			t.Errorf("%q: ABI %d, want %d", s, got, want)
		}
	}
}
