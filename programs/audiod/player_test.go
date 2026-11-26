package audiod

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
)

// deadSink is a sound device that cannot be opened.
type deadSink struct {
	err    error
	writes int
}

func (d *deadSink) Write([]int16) error { d.writes++; return d.err }
func (d *deadSink) Name() string        { return "alsa:plughw:0,0" }

func logged(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}

func lines(b *bytes.Buffer) []string {
	return strings.FieldsFunc(b.String(), func(r rune) bool { return r == '\n' })
}

func ring(t *testing.T) *audio.RingBuf {
	t.Helper()
	f, err := audio.NewRingFile("test", audio.Rate*2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	r, err := audio.MapRing(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Without a sound device the clock goes on: recordings and playback move
// 20 ms a tick, as fast as the ticks come. audiod used to wait a second
// after each failed write, so on the 6T in the chroot (audiod on the
// unrouted earpiece PCM) music, voice messages, recordings and video ran
// 50 times too slow.
func TestFailingSinkKeepsTime(t *testing.T) {
	logged(t)
	m := audio.NewMixer()
	synth, err := audio.NewSynth(audio.DemoTracks[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Play("com.phone.music", "demo", audio.Media, synth); err != nil {
		t.Fatal(err)
	}
	out := &deadSink{err: errors.New("aplay plughw:0,0 S16_LE 48000 2: audio open error: Invalid argument")}
	rec := &recordings{}
	rec.start("com.phone.recorder", "memo", ring(t))
	p := newPlayer(m, out, nil, rec)
	ticks, done := make(chan time.Time), make(chan struct{})
	go func() { p.run(ticks); close(done) }()
	deadline := time.After(5 * time.Second)
	for i := range 50 {
		select {
		case ticks <- time.Now():
		case <-deadline:
			t.Fatalf("tick %d: the loop waits for the sink that failed", i)
		}
	}
	close(ticks)
	<-done
	if got := rec.streams()[0].PosMS; got != 1000 {
		t.Errorf("the recording is at %d ms after 50 ticks, want 1000", got)
	}
	if got := m.State()[0].PosMS; got < 990 || got > 1000 {
		t.Errorf("the music is at %d ms after 50 ticks, want 1000", got)
	}
	if out.writes != 50 {
		t.Errorf("%d writes, want one a tick", out.writes)
	}
}

// stalled is a card that takes nothing: its first write fails after
// holding the loop for a while, the others at once.
type stalled struct {
	deadSink
	now  *time.Time
	hold time.Duration
}

func (s *stalled) Write(pcm []int16) error {
	if s.writes == 0 {
		*s.now = s.now.Add(s.hold)
	}
	return s.deadSink.Write(pcm)
}

// The time a failing write held the loop is made up: recordings and
// playback are where the wall clock says.
func TestStalledSinkMadeUp(t *testing.T) {
	logged(t)
	m := audio.NewMixer()
	synth, err := audio.NewSynth(audio.DemoTracks[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Play("com.phone.music", "demo", audio.Media, synth); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1e9, 0)
	out := &stalled{deadSink: deadSink{err: errors.New("aplay plughw:0,2 S16_LE 48000 2: the card took nothing for 2s")}, now: &now, hold: 300 * time.Millisecond}
	rec := &recordings{}
	rec.start("com.phone.recorder", "memo", ring(t))
	p := newPlayer(m, out, nil, rec)
	p.now = func() time.Time { return now }
	for range 50 {
		p.step()
	}
	if got := rec.streams()[0].PosMS; got != 1300 {
		t.Errorf("the recording is at %d ms after 50 ticks and a 300 ms stall, want 1300", got)
	}
	if got := m.State()[0].PosMS; got < 1290 || got > 1300 {
		t.Errorf("the music is at %d ms after 50 ticks and a 300 ms stall, want 1300", got)
	}
}

// A failing sink is told once, again only when its reason changes, and
// its return once writes have gone through for a second: not when a new
// aplay takes a write before it fails. audio.state carries the reason.
func TestSinkFailureToldOnce(t *testing.T) {
	said := logged(t)
	out := &deadSink{err: errors.New("aplay plughw:0,0 S16_LE 48000 2: audio open error: Invalid argument")}
	p := newPlayer(audio.NewMixer(), out, nil, &recordings{})
	now := time.Unix(1e9, 0)
	p.now = func() time.Time { return now }
	tick := func(n int) {
		for range n {
			p.step()
			now = now.Add(20 * time.Millisecond)
		}
	}
	tick(250)
	if got := lines(said); len(got) != 1 || !strings.Contains(got[0], "phone-audiod: sink alsa:plughw:0,0: aplay plughw:0,0 S16_LE 48000 2: audio open error: Invalid argument: no sound") {
		t.Fatalf("5 s of a failing sink logged %q", got)
	}
	if got := p.sink.failure(); got != out.err.Error() {
		t.Fatalf("failure %q", got)
	}
	out.err = errors.New("aplay plughw:0,0 S16_LE 48000 2: Device or resource busy")
	tick(10)
	out.err = nil
	tick(1)
	out.err = errors.New("aplay plughw:0,0 S16_LE 48000 2: Device or resource busy")
	tick(1)
	if got := lines(said); len(got) != 2 || !strings.Contains(got[1], "busy") {
		t.Fatalf("a new reason and one write between failures logged %q", got)
	}
	out.err = nil
	tick(50)
	if p.sink.failure() == "" {
		t.Fatal("back after less than a second of writes")
	}
	tick(1)
	if got := lines(said); len(got) != 3 || !strings.HasSuffix(got[2], "phone-audiod: sink alsa:plughw:0,0 plays") || p.sink.failure() != "" {
		t.Fatalf("a second of writes: logged %q, failure %q", got, p.sink.failure())
	}
	tick(100)
	if got := lines(said); len(got) != 3 {
		t.Fatalf("a sink that plays logged %q", got)
	}
}

// fakeAplay puts an aplay that runs script on PATH; each run is a line
// of its arguments in the file it returns.
func fakeAplay(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	body := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> '%s'\n%s", runs, script)
	if err := os.WriteFile(filepath.Join(dir, "aplay"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return runs
}

func runsOf(t *testing.T, path string, want int) []string {
	t.Helper()
	var got []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		raw, _ := os.ReadFile(path)
		if got = lines(bytes.NewBuffer(raw)); len(got) >= want {
			break
		}
	}
	return got
}

// A card aplay cannot open: the write fails with aplay's reason, and
// for retryAfter every write fails the same way at once, without
// another aplay; then one more.
func TestAlsaSinkRetries(t *testing.T) {
	runs := fakeAplay(t, "echo 'aplay: main:850: audio open error: Invalid argument' >&2\nexit 1\n")
	now := time.Unix(1e9, 0)
	s := &alsaSink{device: "plughw:0,2", base: "plughw:0,2", now: func() time.Time { return now }}
	pcm := make([]int16, audio.Channels*audio.Frame)
	var err error
	for deadline := time.Now().Add(5 * time.Second); err == nil && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		err = s.Write(pcm)
	}
	const want = "aplay plughw:0,2 S16_LE 48000 2: audio open error: Invalid argument"
	if err == nil || err.Error() != want {
		t.Fatalf("write: %v, want %s", err, want)
	}
	for range int(retryAfter/(20*time.Millisecond)) - 1 {
		now = now.Add(20 * time.Millisecond)
		if err := s.Write(pcm); err == nil || err.Error() != want {
			t.Fatalf("within %v: %v", retryAfter, err)
		}
	}
	if got := runsOf(t, runs, 1); len(got) != 1 || got[0] != "-q -t raw -f S16_LE -r 48000 -c 2 -D plughw:0,2 --buffer-time=200000" {
		t.Fatalf("aplay ran %q", got)
	}
	now = now.Add(20 * time.Millisecond)
	_ = s.Write(pcm)
	if got := runsOf(t, runs, 2); len(got) != 2 {
		t.Fatalf("after %v aplay ran %q, want once more", retryAfter, got)
	}
	s.stop()
}

// An aplay that takes nothing (waiting for a card another program
// holds) fails the write after stallAfter instead of holding the loop.
func TestAlsaSinkStall(t *testing.T) {
	fakeAplay(t, "exec sleep 30\n")
	s := &alsaSink{device: "plughw:0,2", base: "plughw:0,2"}
	pcm := make([]int16, audio.Channels*audio.Frame)
	start := time.Now()
	var err error
	for i := 0; i < 100 && err == nil; i++ {
		err = s.Write(pcm)
	}
	if err == nil || !strings.HasSuffix(err.Error(), "the card took nothing for 2s") {
		t.Fatalf("write: %v", err)
	}
	if d := time.Since(start); d > stallAfter+3*time.Second {
		t.Fatalf("the stalled card held the writes for %v", d)
	}
	if s.w != nil {
		t.Fatal("the stalled aplay is still there")
	}
}

// fussy is a card that takes the mixer's output, not a track's own
// set-up: Open refuses it from the start, or after a write that failed.
type fussy struct {
	nullSink
	err     error
	rawFail bool
}

func (f *fussy) Open(hifi.Plan) error { return f.err }

func (f *fussy) WriteRaw(hifi.Plan, []byte) error {
	if f.rawFail {
		f.err = fmt.Errorf("%w: aplay plughw:0,2 S16_LE 44100 2: Invalid argument", errNotTaken)
		return f.err
	}
	return nil
}

// Music alone on a card that does not take its format goes through the
// mixer at its own pace: the samples taken for the card are not lost to
// the mixer as well (the track used to run ahead), and the refusal is
// told once.
func TestSoloRefused(t *testing.T) {
	for _, out := range []*fussy{
		{err: fmt.Errorf("%w: aplay plughw:0,2 S16_LE 44100 2: Invalid argument", errNotTaken)},
		{rawFail: true},
	} {
		said := logged(t)
		m := audio.NewMixer()
		m.SetVolume(audio.Media, 1)
		s, err := hifi.Open(hifi.Spec{Path: wav(t, 44100, 44100*3), Media: "music:a.wav", Label: "WAV 16/44.1"}, hifi.Options{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Play("com.phone.music", "a", audio.Media, s); err != nil {
			t.Fatal(err)
		}
		p := newPlayer(m, out, nil, &recordings{})
		for range 50 {
			p.step()
		}
		// The mixer's resampler reads a little ahead of the 44.1 kHz track.
		if got := m.State()[0].PosMS; got < 980 || got > 1050 {
			t.Errorf("raw writes fail %v: the track is at %d ms after 50 ticks, want about 1000", out.rawFail, got)
		}
		if got := lines(said); len(got) != 1 || !strings.Contains(got[0], "not taken") {
			t.Errorf("raw writes fail %v: logged %q", out.rawFail, got)
		}
	}
}
