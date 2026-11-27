package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeAudiod is phone-audiod as the script's audio step sees it: the
// streams its state answers with, and the stream events it sends.
type fakeAudiod struct {
	hub *wire.Hub
	mu  sync.Mutex
	now []audio.Stream
	// held, when set, runs inside the next state call before it answers:
	// what happened while a stalled guest held the answer up.
	held func()
}

func (f *fakeAudiod) handle(method string, _ json.RawMessage) (any, error) {
	if method != svc.AudioState {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	f.mu.Lock()
	held := f.held
	f.held = nil
	f.mu.Unlock()
	if held != nil {
		held()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"streams": append([]audio.Stream{}, f.now...)}, nil
}

func (f *fakeAudiod) event(s audio.Stream) {
	f.hub.Broadcast(map[string]any{"event": svc.AudioEventStream, "stream": s})
}

// played sends s playing and then ended, each once the Shell has the one
// before: a stream that came and went before the runner looked.
func (f *fakeAudiod) played(t *testing.T, r *Runner, s audio.Stream) {
	t.Helper()
	s.State = ui.PlayPlaying
	f.event(s)
	if _, ok := r.waitFor(func(string) bool { _, ok := r.C.streams[s.ID]; return ok }, 5000); !ok {
		t.Fatalf("the Shell did not hear of %s playing", s.ID)
	}
	s.State = ui.PlayEnded
	f.event(s)
	if _, ok := r.waitFor(func(string) bool { _, ok := r.C.streams[s.ID]; return !ok }, 5000); !ok {
		t.Fatalf("the Shell did not hear of %s ending", s.ID)
	}
}

// audioRunner is a Runner over a Shell whose phone-audiod is the fake.
func audioRunner(t *testing.T) (*Runner, *fakeAudiod) {
	t.Helper()
	c := snapCore(t)
	f := &fakeAudiod{}
	hub, err := wire.ServeHub(filepath.Join(c.cfg.RunDir, svc.AudiodSock), f.handle)
	if err != nil {
		t.Fatal(err)
	}
	f.hub = hub
	r := &Runner{C: c, Out: io.Discard}
	if err := r.step("sleep", "1"); err != nil {
		t.Fatal(err)
	}
	// The Shell subscribes when it gets to it; an event before that is lost.
	probe := audio.Stream{ID: "a0", App: "system", Name: "notify", Category: audio.Notify, State: ui.PlayPlaying}
	if _, ok := r.waitFor(func(string) bool { f.event(probe); _, ok := c.streams[probe.ID]; return ok }, 10000); !ok {
		t.Fatal("the Shell does not hear phone-audiod")
	}
	f.played(t, r, probe)
	return r, f
}

func mustFail(t *testing.T, r *Runner, cmd, arg, why string) {
	t.Helper()
	if err := r.step(cmd, arg); err == nil {
		t.Errorf("%s %s passed: %s", cmd, arg, why)
	}
}

// The QEMU selftest's voice message (d95): Hongyan writes the whole
// second into the ring and closes it, and the stream started and ended
// while the runner's one look at phone-audiod was held up. A person heard
// it, so audio media passes; paused it never was.
func TestAudioSeesAStreamThatCameAndWent(t *testing.T) {
	r, f := audioRunner(t)
	if err := r.step("type", ""); err != nil {
		t.Fatal(err)
	}
	voice := audio.Stream{ID: "a7", App: "com.phone.chat", Name: "Voice message", Category: audio.Media, State: ui.PlayPlaying}
	f.mu.Lock()
	f.held = func() {
		f.event(voice)
		voice.State = ui.PlayEnded
		f.event(voice)
	}
	f.mu.Unlock()
	if err := r.step("audio", "media 3000"); err != nil {
		t.Error(err)
	}
	mustFail(t, r, "audio", "paused media 300", "the voice message was never paused")

	if err := r.step("type", ""); err != nil {
		t.Fatal(err)
	}
	f.event(audio.Stream{ID: "a8", App: "com.phone.music", Category: audio.Media, State: ui.PlayPaused})
	f.event(audio.Stream{ID: "a8", App: "com.phone.music", Category: audio.Media, State: ui.PlayEnded})
	if err := r.step("audio", "paused media 3000"); err != nil {
		t.Error(err)
	}
}

// What audio accepts from before it looked: a stream in that state since
// the script's last input or audio step. One that never was in it, or was
// only before then, still fails; audio idle is about now.
func TestAudioSeenOnlySinceTheLastInputOrAudioStep(t *testing.T) {
	r, f := audioRunner(t)
	if err := r.step("type", ""); err != nil {
		t.Fatal(err)
	}
	f.event(audio.Stream{ID: "a1", App: "com.phone.chat", Category: audio.Media, State: ui.PlayPaused})
	f.event(audio.Stream{ID: "a1", App: "com.phone.chat", Category: audio.Media, State: ui.PlayEnded})
	mustFail(t, r, "audio", "media 300", "the stream was paused, never playing")
	mustFail(t, r, "audio", "ring 300", "nothing rang")

	f.played(t, r, audio.Stream{ID: "a2", App: "com.phone.chat", Category: audio.Media})
	if err := r.step("type", ""); err != nil {
		t.Fatal(err)
	}
	mustFail(t, r, "audio", "media 300", "the stream played before the last input")

	// Dialing: the ringback came and went; the call's voice never started.
	f.played(t, r, audio.Stream{ID: "a3", App: "system", Name: "ringback", Category: audio.Call})
	if err := r.step("audio", "call 3000"); err != nil {
		t.Error(err)
	}
	mustFail(t, r, "audio", "call 300", "the ringback counted for the audio step before")

	f.played(t, r, audio.Stream{ID: "a4", App: "com.phone.chat", Category: audio.Media})
	if err := r.step("audio", "idle 300"); err != nil {
		t.Errorf("a stream that ended counted against idle: %v", err)
	}
	f.mu.Lock()
	f.now = []audio.Stream{{ID: "a5", App: "com.phone.music", Category: audio.Media, State: ui.PlayPlaying}}
	f.mu.Unlock()
	mustFail(t, r, "audio", "idle 300", "music plays now")
}
