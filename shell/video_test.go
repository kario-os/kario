package shell

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakePlayerSide is audiod's or mediad's side of a video player: the
// controls in the order they took effect there. A pause may take slow to
// (an emulator starved by the ones beside it).
type fakePlayerSide struct {
	mu    sync.Mutex
	ops   []string
	state string
}

func (f *fakePlayerSide) handle(slow time.Duration) wire.Handler {
	return func(method string, params json.RawMessage) (any, error) {
		var a struct{ Op string }
		_ = json.Unmarshal(params, &a)
		if method == svc.MediaPlayerClose {
			a.Op = ui.OpClose
		}
		if a.Op == ui.OpPause {
			time.Sleep(slow)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch a.Op {
		case ui.OpPause:
			f.state = ui.PlayPaused
		case ui.OpResume:
			f.state = ui.PlayPlaying
		}
		if a.Op != ui.OpState {
			f.ops = append(f.ops, a.Op)
		}
		return ui.VideoState{Player: "play1", Stream: "play1", State: f.state}, nil
	}
}

func (f *fakePlayerSide) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ops)
}

// newVideoTest: an app in front whose screen has a media node that names
// no stream yet, and its player of a video with sound, just opened.
func newVideoTest(t *testing.T, slowPause time.Duration) (*locTest, *App, *videoPlayer, *fakePlayerSide, *fakePlayerSide) {
	a := locApp("com.test.video")
	lt := newLocTest(t, a)
	c := lt.c
	c.cfg.RunDir = t.TempDir()
	c.broker.clients = map[string]*wire.Client{}
	sound, picture := &fakePlayerSide{state: ui.PlayPlaying}, &fakePlayerSide{state: ui.PlayPlaying}
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.AudiodSock), sound.handle(slowPause))
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.MediadSock), picture.handle(slowPause))
	a.stack = []*View{newView(ui.Screen{ID: "play", Body: []ui.Node{{Kind: ui.KindMedia, ID: "video"}}})}
	c.fg = a
	p := &videoPlayer{id: "play1", app: a, media: "p1", audioID: "a1", opened: time.Now()}
	c.players = map[string]*videoPlayer{p.id: p}
	return lt, a, p, sound, picture
}

// A video just opened is held until the app's screen names its stream,
// then let go, then closed: audiod and mediad take the three in that
// order however long the pause takes there. Each sent from a goroutine of
// its own, the resume could overtake the pause and leave the video at
// 0:00 (QEMU under parallel load), its picture or its sound held for good.
func TestVideoControlsInOrder(t *testing.T) {
	if !alone(t) {
		return
	}
	lt, a, p, sound, picture := newVideoTest(t, 200*time.Millisecond)
	c := lt.c
	c.reconcileVideos()
	if !p.away {
		t.Fatal("a player that no screen names yet plays")
	}
	a.top().S.Body[0].Stream = p.id
	c.reconcileVideos()
	if p.away {
		t.Fatal("a player on screen is held")
	}
	c.closeVideo(p)
	lt.pump("audiod and mediad to take the controls", func() bool { return len(sound.took()) == 3 && len(picture.took()) == 3 })
	if got, want := sound.took(), []string{ui.OpPause, ui.OpResume, ui.OpStop}; !slices.Equal(got, want) {
		t.Errorf("audiod took %v, want %v", got, want)
	}
	if got, want := picture.took(), []string{ui.OpPause, ui.OpResume, ui.OpClose}; !slices.Equal(got, want) {
		t.Errorf("mediad took %v, want %v", got, want)
	}
}

// While the shell holds a player (its stream not on screen) the app is
// told it plays: the app paused nothing. Told paused, the gallery stopped
// asking and showed 0:00 for good once the shell let the video go. The
// app's own pause is the app's to see.
func TestVideoStateHidesTheShellsHold(t *testing.T) {
	lt, a, p, _, picture := newVideoTest(t, 0)
	c := lt.c
	c.reconcileVideos()
	lt.pump("the hold to reach mediad", func() bool { return len(picture.took()) == 1 })
	ask := func(op string) ui.VideoState {
		t.Helper()
		var st ui.VideoState
		answered := false
		c.controlVideo(a, ui.VideoArgs{Player: p.id, Op: op}, func(v any, err error) {
			if err != nil {
				t.Errorf("%s: %v", op, err)
			}
			st, _ = v.(ui.VideoState)
			answered = true
		})
		lt.pump("the answer to "+op, func() bool { return answered })
		return st
	}
	if st := ask(ui.OpState); st.State != ui.PlayPlaying {
		t.Fatalf("held by the shell, the app is told %q", st.State)
	}
	a.top().S.Body[0].Stream = p.id
	c.reconcileVideos()
	if st := ask(ui.OpPause); st.State != ui.PlayPaused {
		t.Fatalf("paused by the app, the app is told %q", st.State)
	}
	if st := ask(ui.OpState); st.State != ui.PlayPaused {
		t.Fatalf("paused by the app, its poll is told %q", st.State)
	}
}
