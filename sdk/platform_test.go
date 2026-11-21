package sdk

import (
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/kario-os/kario/ui"
)

// An app learns the system's platform level from PHONE_PLATFORM; a system
// that does not say is level 1.
func TestPlatformLevel(t *testing.T) {
	for v, want := range map[string]int{"3": 3, strconv.Itoa(ui.PlatformLevel): ui.PlatformLevel, "": 1, "0": 1, "-2": 1, "x": 1} {
		if got := platformLevel(v); got != want {
			t.Errorf("PHONE_PLATFORM=%q: level %d, want %d", v, got, want)
		}
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := os.NewFile(uintptr(fds[1]), "shell")
	defer shell.Close()
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	t.Setenv(ui.EnvPlatform, "7")
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.conn.Close()
	if a.Platform != 7 {
		t.Errorf("Platform %d, want 7", a.Platform)
	}
}
