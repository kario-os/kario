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
