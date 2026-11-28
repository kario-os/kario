package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// dominant estimates the strongest frequency of a frame by zero crossings.
func dominant(samples []int16) float64 {
	cross := 0
	for i := 1; i < len(samples); i++ {
		if (samples[i-1] < 0) != (samples[i] < 0) {
			cross++
		}
	}
	return float64(cross) / 2 * Rate / float64(len(samples))
}

// Unplugging the output media plays on pauses it for good, also while a
// ringtone holds it paused: it must not come back on the speaker when the
// ringtone stops.
func TestNoisy(t *testing.T) {
	m := NewMixer()
	track, _ := NewSynth(DemoTracks[0])
	music, _ := m.Play("com.phone.music", DemoTracks[0].Title, Media, track)
	if m.Top() != Media {
		t.Fatal(m.Top())
	}
	ring, _ := m.Play("system", "ringtone", Ring, SystemTones["ringtone"]())
	if m.Top() != Ring {
		t.Fatal(m.Top())
	}
	if got := m.Noisy(); len(got) != 1 || got[0].ID != music.ID || got[0].State != "paused" || got[0].ByFocus {
		t.Fatalf("%+v", got)
	}
	m.Control(ring.ID, "stop")
	if s := m.State()[0]; s.State != "paused" || m.Top() != "" {
		t.Fatalf("media resumed after the ringtone: %+v", s)
	}
	m.Control(music.ID, "resume")
	if len(m.Noisy()) != 1 || m.State()[0].State != "paused" {
		t.Fatal("playing media not paused")
	}
	if len(m.Noisy()) != 0 {
		t.Fatal("paused media reported again")
	}
}

// run mixes frames and returns the left channel.
func run(m *Mixer, frames int) []int16 {
	out := make([]int16, 0, frames*Frame)
	buf := make([]int16, Channels*Frame)
	for i := 0; i < frames; i++ {
		m.Mix(buf)
		for k := 0; k < Frame; k++ {
			out = append(out, buf[2*k])
		}
	}
	return out
}

func TestFocus(t *testing.T) {
	m := NewMixer()
	var events []Stream
	m.OnChange = func(s Stream) { events = append(events, s) }
	track, _ := NewSynth(DemoTracks[0])
	music, _ := m.Play("com.phone.music", DemoTracks[0].Title, Media, track)
	ring, _ := m.Play("system", "ringtone", Ring, SystemTones["ringtone"]())
	state := func(id string) string {
		for _, s := range m.State() {
			if s.ID == id {
				return s.State
			}
		}
		return "gone"
	}
	if state(music.ID) != "paused" {
		t.Fatalf("media should pause under the ringtone, is %s", state(music.ID))
	}
	m.Control(ring.ID, "stop")
	if state(music.ID) != "playing" {
		t.Fatalf("media should resume after the ringtone, is %s", state(music.ID))
	}
	m.Control(music.ID, "pause")
	call, _ := m.Play("system", "voice", Call, SystemTones["voice"]())
	m.Control(call.ID, "stop")
	if state(music.ID) != "paused" {
		t.Fatal("media the user paused must stay paused after a call")
	}
	notify, _ := m.Play("system", "notify", Notify, SystemTones["notify"]())
	run(m, 20)
	if state(notify.ID) != "gone" {
		t.Fatal("the notification sound should end by itself")
	}
	next, _ := NewSynth(DemoTracks[1])
	m.Play("com.phone.music", DemoTracks[1].Title, Media, next)
	if state(music.ID) != "gone" {
		t.Fatal("a new song must end the previous one")
	}
	if len(events) < 6 {
		t.Fatalf("expected change events, got %d", len(events))
	}
}

func TestTonesAreWhatTheySay(t *testing.T) {
	for name, want := range map[string]float64{"alarm": 880, "ringback": 450} {
		m := NewMixer()
		m.Play("system", name, Alarm, SystemTones[name]())
		got := dominant(run(m, 8)) // 160 ms, inside the first tone segment
		if math.Abs(got-want) > want*0.05 {
			t.Errorf("%s: dominant %.0f Hz, want %.0f", name, got, want)
		}
	}
}

func TestDemoTracksAndWav(t *testing.T) {
	for _, track := range DemoTracks {
		if l := TrackLenMS(track); l < 10000 || l > 60000 {
			t.Errorf("%s: %d ms", track.Title, l)
		}
	}
	var b bytes.Buffer
	pcm := []int16{0, 1000, -1000, 32767, -32768, 5}
	b.Write(WavHeader(uint32(2 * len(pcm))))
	binary.Write(&b, binary.LittleEndian, pcm)
	w, err := DecodeWav(&b)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float64, len(pcm))
	w.Fill(buf)
	if math.Abs(buf[1]-1000.0/32768) > 1e-9 || w.Len() != len(pcm) {
		t.Fatalf("decode: %v len %d", buf, w.Len())
	}
}

// queue is a Described stereo source standing in for audio/hifi's
// stream: two "tracks", left and right different.
type queue struct {
	n, track int
	moved    bool
}

func (q *queue) Fill(buf []float64) bool { return true }
func (q *queue) FillStereo(l, r []float64) bool {
	for i := range l {
		l[i], r[i] = 0.5, -0.5
		q.n++
		if q.n == Rate && q.track == 0 {
			q.track, q.moved = 1, true
		}
	}
	return q.n < 2*Rate
}
func (q *queue) Pos() int { return q.n }
func (q *queue) Len() int { return 2 * Rate }
func (q *queue) Now() (string, string, string) {
	return []string{"music:a", "music:b"}[q.track], []string{"A", "B"}[q.track], "FLAC 24/96"
}
func (q *queue) Changed() bool { c := q.moved; q.moved = false; return c }

func TestStereoQueueAndSolo(t *testing.T) {
	m := NewMixer()
	var events []Stream
	m.OnChange = func(s Stream) { events = append(events, s) }
	q := &queue{}
	s, _ := m.Play("com.phone.music", "", Media, q)
	if s.Media != "music:a" || s.Name != "A" || s.Format != "FLAC 24/96" || s.Output != MixedOutput {
		t.Fatalf("%+v", s)
	}
	buf := make([]int16, Channels*Frame)
	m.Mix(buf)
	if buf[0] <= 0 || buf[1] >= 0 {
		t.Fatalf("left %d right %d", buf[0], buf[1])
	}
	id, src, gain, ok := m.Solo()
	if !ok || id != s.ID || src != Source(q) || gain != 0.7 {
		t.Fatalf("music alone should go solo: %v %v %v", id, ok, gain)
	}
	for i := 0; i < 49; i++ {
		m.Played(id, true)
	}
	ping, _ := m.Play("system", "notify", Notify, SystemTones["notify"]())
	if _, _, _, ok := m.Solo(); ok {
		t.Fatal("a notification needs the mixer")
	}
	run(m, 20)
	if st := m.State(); len(st) != 1 || st[0].ID == ping.ID {
		t.Fatalf("after the ping: %+v", st)
	}
	if _, _, _, ok := m.Solo(); ok {
		t.Fatal("the mixer stays a second after the ping")
	}
	run(m, 50)
	if _, _, _, ok := m.Solo(); !ok {
		t.Fatal("solo again a second later")
	}
	moved := false
	for _, e := range events {
		moved = moved || e.ID == s.ID && e.Media == "music:b" && e.Name == "B"
	}
	if !moved {
		t.Fatal("the queue's next track was not reported")
	}
	m.SetOutput("pcm S32_LE 96000 Hz 2ch bit-perfect")
	if st := m.State(); st[0].Output != "pcm S32_LE 96000 Hz 2ch bit-perfect" {
		t.Fatalf("%+v", st)
	}
	for i := 0; i < 100; i++ {
		m.Played(id, i < 99)
	}
	if len(m.State()) != 0 {
		t.Fatal("the solo stream should end")
	}
}

// Mono sums the sides into both; the balance turns one side down; with
// either, music does not go to the card by itself (its stereo would not
// change). Off, the output is as it was.
func TestMonoAndBalance(t *testing.T) {
	m := NewMixer()
	m.SetVolume(Media, 1)
	s, _ := m.Play("com.phone.music", "", Media, &asym{})
	buf := make([]int16, Channels*Frame)
	side := func() (int16, int16) {
		m.Mix(buf)
		return buf[0], buf[1]
	}
	if l, r := side(); l != 13106 || r != 3276 {
		t.Fatalf("as it is: %d %d", l, r)
	}
	m.SetChannels(true, 0)
	if l, r := side(); l != r || l != 8191 {
		t.Fatalf("mono: %d %d", l, r)
	}
	m.SetChannels(false, 0.5)
	if l, r := side(); l != 6553 || r != 3276 {
		t.Fatalf("balance right: %d %d", l, r)
	}
	m.SetChannels(false, -1)
	if l, r := side(); l != 13106 || r != 0 {
		t.Fatalf("left only: %d %d", l, r)
	}
	m.SetChannels(true, 1)
	if l, r := side(); l != 0 || r != 8191 {
		t.Fatalf("mono on the right: %d %d", l, r)
	}
	if _, _, _, ok := m.Solo(); ok {
		t.Fatal("music went past the mixer with mono on")
	}
	m.SetChannels(false, 0)
	if id, _, _, ok := m.Solo(); !ok || id != s.ID {
		t.Fatal("music alone goes solo again once mono and balance are off")
	}
	if mono, bal := m.Channels(); mono || bal != 0 {
		t.Fatal(mono, bal)
	}
}

// asym plays 0.4 on the left and 0.1 on the right.
type asym struct{ n int }

func (a *asym) Fill(buf []float64) bool { return true }
func (a *asym) FillStereo(l, r []float64) bool {
	for i := range l {
		l[i], r[i] = 0.4, 0.1
	}
	a.n += len(l)
	return true
}
func (a *asym) Pos() int { return a.n }
func (a *asym) Len() int { return 0 }
