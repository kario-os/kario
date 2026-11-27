package sdk

import (
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// Now starts with the system clock's offset at the app's start and follows
// the clock events, before the app hears of them.
func TestClockEvent(t *testing.T) {
	defer clockOffset.Store(0)
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := os.NewFile(uintptr(fds[1]), "shell")
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	t.Setenv(ui.EnvClockOffset, "7200000")
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	ahead := func() time.Duration { return Now().Sub(time.Now()).Round(time.Minute) }
	if d := ahead(); d != 2*time.Hour {
		t.Errorf("at the start Now is %v ahead, want 2h", d)
	}
	got := make(chan time.Duration, 2)
	a.OnEvent = func(name, value string) {
		if name == ui.EventClock {
			got <- ahead()
		}
	}
	go func() { _ = a.Run() }()
	defer shell.Close()
	for _, c := range []struct {
		value string
		want  time.Duration
	}{{"-1800000", -30 * time.Minute}, {"0", 0}} {
		if _, err := shell.Write([]byte(`{"t":"event","event":"time.clock","value":"` + c.value + `"}` + "\n")); err != nil {
			t.Fatal(err)
		}
		select {
		case d := <-got:
			if d != c.want {
				t.Errorf("after %s Now is %v ahead when the app hears of it, want %v", c.value, d, c.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no clock event")
		}
	}
}
