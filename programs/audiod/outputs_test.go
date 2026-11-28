package audiod

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/route"
	"github.com/kario-os/kario/audio/ucm"
	"github.com/kario-os/kario/ui"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	dacStream = `FiiO FiiO KA3 at usb-xhci-hcd.1.auto-1, high speed : USB Audio

Playback:
  Status: Stop
  Interface 1
    Altset 1
    Format: S32_LE
    Channels: 2
    Endpoint: 0x01 (1 OUT) (ASYNC)
    Rates: 44100, 48000, 88200, 96000, 176400, 192000, 352800, 384000
`
	headsetStream = `Logitech USB Headset at usb-xhci-hcd.1.auto-1.2, full speed : USB Audio

Playback:
  Interface 1
    Altset 1
    Format: S16_LE
    Channels: 2
    Rates: 48000

Capture:
  Interface 2
    Altset 1
    Format: S16_LE
    Channels: 1
    Rates: 48000
`
	micStream = `Blue Yeti at usb-xhci-hcd.1.auto-1.3, full speed : USB Audio

Capture:
  Interface 2
    Altset 1
    Format: S16_LE
    Channels: 2
    Rates: 48000
`
	cards = ` 0 [sdm845         ]: sdm845 - OnePlus 6T
                      OnePlus 6T
 1 [KA3            ]: USB-Audio - FiiO KA3
                      FiiO FiiO KA3 at usb-xhci-hcd.1.auto-1, high speed
 2 [Headset        ]: USB-Audio - Logitech USB Headset
                      Logitech USB Headset at usb-xhci-hcd.1.auto-1.2, full speed
 3 [Microphone     ]: USB-Audio - Yeti Stereo Microphone
                      Blue Yeti at usb-xhci-hcd.1.auto-1.3, full speed
`
)

// asound is a /proc/asound with the built-in card, a DAC, a USB headset
// and a USB microphone.
func asound(t *testing.T) string {
	d := t.TempDir()
	write(t, filepath.Join(d, "cards"), cards)
	write(t, filepath.Join(d, "card0", "pcm0p", "info"), "card: 0\n")
	write(t, filepath.Join(d, "card1", "usbid"), "2972:0047\n")
	write(t, filepath.Join(d, "card1", "stream0"), dacStream)
	write(t, filepath.Join(d, "card2", "usbid"), "046d:0a8f\n")
	write(t, filepath.Join(d, "card2", "stream0"), headsetStream)
	write(t, filepath.Join(d, "card3", "usbid"), "b58e:9e84\n")
	write(t, filepath.Join(d, "card3", "stream0"), micStream)
	return d
}

func TestScanUSB(t *testing.T) {
	got := scanUSB(asound(t))
	want := []usbCard{{num: "1", id: "2972:0047", name: "FiiO KA3"}, {num: "2", id: "046d:0a8f", name: "Logitech USB Headset", mic: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
	if got := scanUSB(t.TempDir()); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestPlaybackControl(t *testing.T) {
	scontents := `Simple mixer control 'Mic',0
  Capabilities: cvolume cvolume-joined cswitch cswitch-joined
  Capture channels: Mono
Simple mixer control 'Headset',0
  Capabilities: pvolume pswitch pswitch-joined
  Playback channels: Front Left - Front Right
  Limits: Playback 0 - 127
Simple mixer control 'PCM',0
  Capabilities: pvolume pswitch
  Limits: Playback 0 - 100
`
	if got := playbackControl(scontents); got != "PCM" {
		t.Fatal(got)
	}
	if got := playbackControl(strings.Replace(scontents, "'PCM'", "'Clock'", 1)); got != "Headset" {
		t.Fatal(got)
	}
	if got := playbackControl("Simple mixer control 'Mic',0\n  Capabilities: cvolume\n"); got != "" {
		t.Fatal(got)
	}
}

func event(typ, code uint16, val int32) []byte {
	b := make([]byte, inputEventLen)
	le := binary.LittleEndian
	le.PutUint16(b[16:], typ)
	le.PutUint16(b[18:], code)
	le.PutUint32(b[20:], uint32(val))
	return b
}

// The jack's input device is found by its switches; a headset's two
// switches in one report make one change.
func TestJack(t *testing.T) {
	sys := t.TempDir()
	write(t, filepath.Join(sys, "event0", "device", "name"), "pm8941_pwrkey\n")
	write(t, filepath.Join(sys, "event3", "device", "name"), "Alert slider\n")
	write(t, filepath.Join(sys, "event3", "device", "capabilities", "sw"), "0\n")
	write(t, filepath.Join(sys, "event7", "device", "name"), "QCM6490 Headset Jack\n")
	write(t, filepath.Join(sys, "event7", "device", "capabilities", "sw"), "3d4\n")
	if ev, name := findJack(sys); ev != "event7" || name != "QCM6490 Headset Jack" {
		t.Fatal(ev, name)
	}
	if ev, _ := findJack(t.TempDir()); ev != "" {
		t.Fatal(ev)
	}
	var in bytes.Buffer
	for _, e := range [][]byte{
		event(evSW, swHeadphone, 1), event(evSW, swMicrophone, 1), event(evSyn, 0, 0),
		event(evSyn, 0, 0),
		event(evSW, swMicrophone, 0), event(evSW, swHeadphone, 0), event(evSyn, 0, 0),
		event(evSW, swLineout, 1), event(evSyn, 0, 0),
	} {
		in.Write(e)
	}
	var got []jackState
	_ = jackEvents(&in, jackState{}, func(s jackState) { got = append(got, s) })
	want := []jackState{{phones: true, mic: true}, {}, {lineout: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}

type fakeHW struct {
	mu   sync.Mutex
	ctl  map[string]string
	sets []string
}

func (f *fakeHW) Control(card string) string { return f.ctl[card] }
func (f *fakeHW) Set(card, control string, level float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, card+" "+control+" "+strconv.FormatFloat(level, 'f', -1, 64))
	return nil
}

type views struct {
	mu  sync.Mutex
	got []outputsView
}

func (v *views) add(o outputsView) {
	v.mu.Lock()
	v.got = append(v.got, o)
	v.mu.Unlock()
}

func newTestOutputs(base sink, router *ucm.Router) (*outputs, *audio.Mixer, *fakeHW) {
	m := audio.NewMixer()
	o := newOutputs(m, base, router)
	hw := &fakeHW{ctl: map[string]string{"1": "PCM"}}
	o.hw, o.logf = hw, func(string, ...any) {}
	return o, m, hw
}

func media(t *testing.T, m *audio.Mixer) audio.Stream {
	t.Helper()
	for _, s := range m.State() {
		if s.Category == audio.Media {
			return s
		}
	}
	t.Fatal("no media stream")
	return audio.Stream{}
}

// The owner's case on audiod: music on the speaker, a headset in the
// jack, then a USB DAC with its own volume; each takes the music at once,
// at its own volume; pulling out the DAC pauses the music, which then
// stays paused through a ringtone.
func TestOutputsPlugUnplug(t *testing.T) {
	o, m, hw := newTestOutputs(&nullSink{}, nil)
	var seen views
	o.changed = seen.add
	track, _ := audio.NewSynth(audio.DemoTracks[0])
	m.Play("com.phone.music", "a", audio.Media, track)
	o.SetVolume("", 0.8)
	if v := m.Volume(audio.Media); v != 0.8 {
		t.Fatal(v)
	}

	o.jack(jackState{phones: true, mic: true})
	if v := o.View(); v.Media != route.Wired || v.Volume != safeLevel || m.Volume(audio.Media) != safeLevel {
		t.Fatalf("%+v %v", v, m.Volume(audio.Media))
	}
	o.SetVolume(route.Wired, 0.3)
	if m.Volume(audio.Media) != 0.3 {
		t.Fatal(m.Volume(audio.Media))
	}

	o.usbCards(scanUSB(asound(t)))
	v := o.View()
	if v.Media != "usb:2" || len(v.Outputs) != 4 {
		t.Fatalf("%+v", v)
	}
	// The DAC (card 1) has a volume control: absolute volume.
	if err := o.Pick("usb:1"); err != nil {
		t.Fatal(err)
	}
	if v := o.View(); v.Media != "usb:1" || !v.Absolute || m.Volume(audio.Media) != 1 {
		t.Fatalf("%+v", v)
	}
	o.SetVolume("usb:2972:0047", 0.4)
	if !slices.Equal(hw.sets, []string{"1 PCM 0.5", "1 PCM 0.4"}) || m.Volume(audio.Media) != 1 {
		t.Fatal(hw.sets, m.Volume(audio.Media))
	}
	// No UCM call set-up for the jack here: a cellular call has no way
	// to the headset, and no earpiece either.
	if v := o.View(); v.Profiles["usb:1"] != "generic" || v.ModemCall != route.Speaker || v.Call != "usb:1" || v.CallMic != route.PhoneMic {
		t.Fatalf("%+v", v)
	}

	// The headset (USB) goes: it was not playing, nothing pauses.
	dir := asound(t)
	os.RemoveAll(filepath.Join(dir, "card2"))
	o.usbCards(scanUSB(dir))
	if s := media(t, m); s.State != ui.PlayPlaying {
		t.Fatalf("%+v", s)
	}
	// The DAC goes while a ringtone holds the music paused: paused for
	// good, back on the headset in the jack at its volume.
	ring, _ := m.Play("system", "ringtone", audio.Ring, audio.SystemTones["ringtone"]())
	os.RemoveAll(filepath.Join(dir, "card1"))
	o.usbCards(scanUSB(dir))
	m.Control(ring.ID, "stop")
	if s := media(t, m); s.State != ui.PlayPaused || s.ByFocus {
		t.Fatalf("%+v", s)
	}
	if v := o.View(); v.Media != route.Wired || m.Volume(audio.Media) != 0.3 {
		t.Fatalf("%+v %v", v, m.Volume(audio.Media))
	}
	o.jack(jackState{})
	if v := o.View(); v.Media != route.Speaker || m.Volume(audio.Media) != 0.8 || len(v.Outputs) != 1 {
		t.Fatalf("%+v", v)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	noisy := 0
	for _, v := range seen.got {
		if v.Noisy != "" {
			noisy++
			if v.Noisy != "usb:1" {
				t.Fatalf("noisy %q", v.Noisy)
			}
		}
	}
	if noisy != 1 {
		t.Fatalf("%d noisy events", noisy)
	}
}

// What the mixer plays to: media on the DAC's own aplay; a ringtone on
// the built-in card and the DAC; a cellular call on the built-in card.
func TestTargets(t *testing.T) {
	base := &alsaSink{device: "plughw:0,2", base: "plughw:0,2"}
	o, _, _ := newTestOutputs(base, nil)
	if got := o.targets(audio.Media, false); len(got) != 1 || got[0] != base {
		t.Fatal(got)
	}
	o.usbCards(scanUSB(asound(t)))
	_ = o.Pick("usb:1")
	got := o.targets(audio.Media, false)
	if len(got) != 1 || got[0].Name() != "alsa:plughw:1,0" {
		t.Fatal(got)
	}
	got = o.targets(audio.Ring, false)
	if len(got) != 2 || got[0] != base || got[1].Name() != "alsa:plughw:1,0" {
		t.Fatal(got)
	}
	if got := o.targets(audio.Call, true); len(got) != 1 || got[0] != base {
		t.Fatal(got)
	}
	if got := o.targets(audio.Call, false); len(got) != 1 || got[0].Name() != "alsa:plughw:1,0" {
		t.Fatal(got)
	}
	// A file sink takes everything.
	f := &nullSink{}
	o, _, _ = newTestOutputs(f, nil)
	o.usbCards(scanUSB(asound(t)))
	if got := o.targets(audio.Ring, false); len(got) != 1 || got[0] != f {
		t.Fatal(got)
	}
}

type ucmRuns struct{ runs []string }

func (u *ucmRuns) Run(cmds []string) error {
	u.runs = append(u.runs, strings.Join(cmds, " "))
	return nil
}

// The jack on a card routed by UCM: media to the headphones, a ringtone
// on both, a cellular call on the headset with its microphone.
func TestTargetsJack(t *testing.T) {
	tool := &ucmRuns{}
	r := &ucm.Router{Tool: tool, Media: ucm.Setup{Verb: "HiFi", Devices: []string{"Speaker"}},
		Earpiece:   ucm.Setup{Verb: "VoiceCall", Devices: []string{"Earpiece", "Mic"}},
		Headphones: ucm.Setup{Verb: "HiFi", Devices: []string{"Headphones"}},
		Headset:    ucm.Setup{Verb: "VoiceCall", Devices: []string{"Headphones", "Mic"}}, HeadsetMic: "HeadsetMic", Mic: "Mic",
		State: filepath.Join(t.TempDir(), "ucm.json")}
	_ = r.Start()
	_ = r.SetCalls(nil)
	o, _, _ := newTestOutputs(&nullSink{}, r)
	o.jack(jackState{phones: true, mic: true})
	if v := o.View(); v.ModemCall != route.Wired || v.ModemMic != route.Wired {
		t.Fatalf("%+v", v)
	}
	tool.runs = nil
	o.targets(audio.Media, false)
	o.targets(audio.Ring, false)
	_ = r.Call(ui.TelCall{ID: "c1", State: "active"})
	want := []string{
		"set _verb HiFi set _enadev Speaker set _enadev Headphones set _disdev Speaker",
		"set _verb HiFi set _enadev Headphones set _enadev Speaker",
		"set _verb HiFi set _enadev Speaker set _enadev Headphones set _verb VoiceCall set _enadev Headphones set _enadev HeadsetMic",
	}
	if !slices.Equal(tool.runs, want) {
		t.Fatalf("\n%s", strings.Join(tool.runs, "\n"))
	}
}

func TestSim(t *testing.T) {
	o, m, _ := newTestOutputs(&nullSink{}, nil)
	dac := simArgs{Op: "plug", ID: "usb:dac", Kind: route.USB, Name: "DAC", HWVolume: true}
	if err := o.Sim(dac); err == nil || o.View().Sim {
		t.Fatal("stand-ins taken without --sim-outputs")
	}
	o.sim = true
	if err := o.Sim(dac); err != nil {
		t.Fatal(err)
	}
	if v := o.View(); v.Media != "usb:dac" || !v.Absolute || m.Volume(audio.Media) != 1 || v.Outputs[1].Key != "usb:dac" || !v.Sim {
		t.Fatalf("%+v", v)
	}
	for _, bad := range []simArgs{{Op: "plug", ID: "x", Kind: route.Wired}, {Op: "plug", ID: "e", Kind: route.Earpiece}, {Op: "eject"}, {Op: "unplug", ID: route.Speaker}} {
		if err := o.Sim(bad); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if err := o.Sim(simArgs{Op: "unplug", ID: "usb:dac"}); err != nil || o.View().Media != route.Speaker {
		t.Fatal(err)
	}
}
