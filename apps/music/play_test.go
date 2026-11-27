package main

import (
	"encoding/json"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeShell starts the app against a socket the test plays the Shell on.
type fakeShell struct {
	t     *testing.T
	p     *player
	shell *wire.Conn
	reqs  chan ui.Msg
}

func newFakeShell(t *testing.T) *fakeShell {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := wire.New(os.NewFile(uintptr(fds[1]), "shell"))
	t.Cleanup(func() { shell.Close() })
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := sdk.New()
	if err != nil {
		t.Fatal(err)
	}
	p := &player{app: a, cur: -1, state: ui.PlayStopped}
	a.OnAction = p.action
	go a.Run()
	f := &fakeShell{t: t, p: p, shell: shell, reqs: make(chan ui.Msg, 16)}
	go func() {
		for {
			var m ui.Msg
			if shell.Recv(&m) != nil {
				close(f.reqs)
				return
			}
			if m.T == ui.MsgReq {
				f.reqs <- m
			}
		}
	}()
	return f
}

// req is the app's next capability request, which must be for cap.
func (f *fakeShell) req(cap string) ui.Msg {
	f.t.Helper()
	select {
	case m := <-f.reqs:
		if m.Cap != cap {
			f.t.Fatalf("asked for %s %s, want %s", m.Cap, m.Args, cap)
		}
		return m
	case <-time.After(2 * time.Second):
		f.t.Fatalf("no %s", cap)
	}
	return ui.Msg{}
}

// play answers an audio.play with the stream it started and waits for the
// app to take it.
func (f *fakeShell) play(m ui.Msg, stream string) {
	f.t.Helper()
	data, _ := json.Marshal(map[string]string{"id": stream})
	if err := f.shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true, Data: data}); err != nil {
		f.t.Fatal(err)
	}
	for got, end := "", time.Now().Add(2*time.Second); got != stream; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			f.t.Fatalf("stream %s was not taken", stream)
		}
		f.on(func() { got = f.p.stream })
	}
}

// on runs fn on the app's loop.
func (f *fakeShell) on(fn func()) {
	done := make(chan struct{})
	f.p.app.Post(func() { fn(); close(done) })
	<-done
}

// between plays a library track that ends, so the app starts the demo
// track after it: the request for that stream, not answered yet.
func (f *fakeShell) between() ui.Msg {
	f.t.Helper()
	f.on(func() {
		f.p.lib = []track{{MediaItem: ui.MediaItem{ID: "music:a.dsf", Title: "A"}}, {MediaItem: ui.MediaItem{ID: "demo:dawn", Title: "Dawn"}}}
		f.p.start([]int{0, 1}, 0)
	})
	f.play(f.req(ui.CapAudioPlay), "a1")
	f.on(func() { f.p.onAudio(streamEvent{ID: "a1", State: ui.PlayEnded}) })
	return f.req(ui.CapAudioPlay)
}

func (f *fakeShell) press(action string) {
	f.on(func() { f.p.action(sdk.ActionEvent{Screen: "now", Node: "toggle", Action: action}) })
}

// Stop or Pause pressed as one track ends and the queue starts the next
// (the two-second DSD64 sample on a slow emulator): the next track's
// stream was not there yet, the press was dropped, and the answer set it
// playing, on through the demo tracks. The press is done to the stream
// that comes.
func TestPressBetweenTracks(t *testing.T) {
	for _, c := range []struct{ action, op, state string }{
		{ui.OpStop, ui.OpStop, ui.PlayStopped},
		{"toggle", ui.OpPause, ui.PlayPlaying},
	} {
		t.Run(c.action, func(t *testing.T) {
			f := newFakeShell(t)
			dawn := f.between()
			f.press(c.action)
			f.play(dawn, "a2")
			var args ui.AudioControlArgs
			if err := json.Unmarshal(f.req(ui.CapAudioControl).Args, &args); err != nil || args.ID != "a2" || args.Op != c.op {
				t.Fatalf("sent %+v (%v), want %s to a2", args, err, c.op)
			}
			var state string
			f.on(func() { state = f.p.state })
			if state != c.state {
				t.Fatalf("state %s, want %s", state, c.state)
			}
		})
	}
}

// Stop and then Play before the next track's stream comes: the track Play
// starts plays, the Stop was for the one before it.
func TestPlayAfterStopBetweenTracks(t *testing.T) {
	f := newFakeShell(t)
	dawn := f.between()
	f.press(ui.OpStop)
	f.press("toggle")
	again := f.req(ui.CapAudioPlay)
	f.play(dawn, "a2")
	f.play(again, "a3")
	select {
	case m := <-f.reqs:
		t.Fatalf("asked for %s %s after both answers", m.Cap, m.Args)
	case <-time.After(300 * time.Millisecond):
	}
	var state string
	f.on(func() { state = f.p.state })
	if state != ui.PlayPlaying {
		t.Fatalf("state %s, want playing", state)
	}
}
