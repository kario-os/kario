package main

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeShell plays the Shell for the app: location.get is refused while the
// app is not on the screen (fg), as the broker refuses it, and answered
// with fix once it is. pages are the pages the app sent, in order.
type fakeShell struct {
	t     *testing.T
	shell *wire.Conn
	mu    sync.Mutex
	fg    bool
	fix   ui.Location
	gets  int
	pages []ui.Screen
}

// startWeather starts the app against f, in the background unless f.fg.
func startWeather(t *testing.T, f *fakeShell) {
	t.Helper()
	f.t = t
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.shell = wire.New(os.NewFile(uintptr(fds[1]), "shell"))
	t.Cleanup(func() { f.shell.Close() })
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := sdk.New()
	if err != nil {
		t.Fatal(err)
	}
	newWeather(a)
	go a.Run()
	go f.serve()
	if err := f.shell.Send(ui.Msg{T: ui.MsgStart}); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeShell) serve() {
	for {
		var m ui.Msg
		if f.shell.Recv(&m) != nil {
			return
		}
		switch {
		case m.T == ui.MsgReq:
			f.answer(m)
		case m.Screen != nil:
			f.mu.Lock()
			f.pages = append(f.pages, *m.Screen)
			f.mu.Unlock()
		}
	}
}

func (f *fakeShell) answer(m ui.Msg) {
	f.mu.Lock()
	var v any = map[string]bool{"ok": true}
	res := ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true}
	switch m.Cap {
	case ui.CapAppSettings:
		v = map[string]any{}
	case ui.CapLocationGet:
		f.gets++
		v = f.fix
		if !f.fg {
			res = ui.Msg{T: ui.MsgRes, Req: m.Req, Err: ui.ErrLocationBackground}
		}
	}
	if res.OK {
		res.Data, _ = json.Marshal(v)
	}
	f.mu.Unlock()
	_ = f.shell.Send(res)
}

// waitTitle waits for a page whose title line (the place) is text.
func (f *fakeShell) waitTitle(text string) {
	f.t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		f.mu.Lock()
		ok := slices.ContainsFunc(f.pages, func(s ui.Screen) bool {
			return len(s.Body) > 0 && s.Body[0].Text == text
		})
		f.mu.Unlock()
		if ok {
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t.Fatalf("no page for %q (%d location requests, %d pages)", text, f.gets, len(f.pages))
}

// Started in the background (its refresh job wakes it), the app is refused
// the location; once on the screen it asks for it again instead of staying
// "not located" until the user refreshes.
func TestLocatesOnceOnScreenAfterBackgroundStart(t *testing.T) {
	f := &fakeShell{fix: ui.Location{Lat: 31.2304, Lon: 121.4737, AccuracyM: 10}}
	startWeather(t, f)
	f.waitTitle(tr.T("%s (not located)", tr.T("Beijing")))
	f.mu.Lock()
	f.fg = true
	f.mu.Unlock()
	if err := f.shell.Send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateForeground}); err != nil {
		t.Fatal(err)
	}
	f.waitTitle(tr.T("Shanghai"))
}

// Started on the screen, the app locates once; coming back to the screen
// does not ask again.
func TestLocatesOnceWhenStartedOnScreen(t *testing.T) {
	f := &fakeShell{fg: true, fix: ui.Location{Lat: 31.2304, Lon: 121.4737, AccuracyM: 10}}
	startWeather(t, f)
	f.waitTitle(tr.T("Shanghai"))
	if err := f.shell.Send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground}); err != nil {
		t.Fatal(err)
	}
	if err := f.shell.Send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateForeground}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets != 1 {
		t.Fatalf("%d location requests, want 1", f.gets)
	}
}
