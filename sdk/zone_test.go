package sdk

import (
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// A time zone event puts the zone in force before the app hears of it.
func TestTimeZoneEvent(t *testing.T) {
	t.Setenv("TZ", "UTC")
	old := time.Local
	defer func() { time.Local = old }()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := os.NewFile(uintptr(fds[1]), "shell")
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	a.OnEvent = func(name, value string) {
		if name == ui.EventTimeZone {
			got <- time.Now().Location().String() + " " + os.Getenv("TZ")
		}
	}
	go func() { _ = a.Run() }()
	defer shell.Close()
	if _, err := shell.Write([]byte(`{"t":"event","event":"time.zone","value":"America/Mexico_City"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case z := <-got:
		if z != "America/Mexico_City America/Mexico_City" {
			t.Errorf("zone in force when the app heard of it: %q", z)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no time zone event")
	}
}
