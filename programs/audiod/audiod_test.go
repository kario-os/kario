package audiod

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
)

func wav(t *testing.T, rate, frames int) string {
	t.Helper()
	le := binary.LittleEndian
	b := make([]byte, 44+4*frames)
	copy(b, "RIFF")
	le.PutUint32(b[4:], uint32(36+4*frames))
	copy(b[8:], "WAVEfmt ")
	le.PutUint32(b[16:], 16)
	le.PutUint16(b[20:], 1)
	le.PutUint16(b[22:], 2)
	le.PutUint32(b[24:], uint32(rate))
	le.PutUint32(b[28:], uint32(rate*4))
	le.PutUint16(b[32:], 4)
	le.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	le.PutUint32(b[40:], uint32(4*frames))
	p := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Music alone goes to the sink at its own format; with a notification
// sounding the mixer plays; a file sink only ever takes the mixer.
func TestSoloAndBack(t *testing.T) {
	m := audio.NewMixer()
	m.SetVolume(audio.Media, 1)
	s, err := hifi.Open(hifi.Spec{Path: wav(t, 44100, 44100), Media: "music:a.wav", Label: "WAV 16/44.1"}, hifi.Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := m.Play("com.phone.music", "a", audio.Media, s)
	null := &nullSink{}
	var raw []byte
	if !solo(m, null, &raw) {
		t.Fatal("music alone should go past the mixer")
	}
	if p := null.plan; p.Mode != hifi.ModePCM || p.Format != "S16_LE" || p.Rate != 44100 || !p.BitPerfect {
		t.Fatalf("plan %v", p)
	}
	if out := m.State()[0].Output; !strings.Contains(out, "bit-perfect") || m.State()[0].Media != "music:a.wav" {
		t.Fatalf("state %+v", m.State()[0])
	}
	m.Play("system", "notify", audio.Notify, audio.SystemTones["notify"]())
	if solo(m, null, &raw) {
		t.Fatal("a notification needs the mixer")
	}
	f, err := newFileSink(filepath.Join(t.TempDir(), "out.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if solo(m, f, &raw) {
		t.Fatal("a WAV file takes only the mixer's output")
	}
	m.Control(st.ID, "stop")
}

func TestSinks(t *testing.T) {
	if s, err := openSink("alsa:hw:1,0"); err != nil || s.(*alsaSink).hw() != "hw:1,0" || s.(*alsaSink).card() != "1" {
		t.Fatalf("%v %v", s, err)
	}
	if s, _ := openSink("alsa"); s.(*alsaSink).hw() != "hw:0,0" {
		t.Fatal(s.Name())
	}
	if _, err := openSink("pulse"); err == nil {
		t.Fatal("unknown sink accepted")
	}
}
