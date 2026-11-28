package hifi

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/dsd/dsdtest"
)

// wav24 writes a 24-bit stereo WAV whose samples are a sine on the left
// and a counter-like pattern on the right (every value distinct, so a
// dropped or repeated frame shows).
func wav24(t *testing.T, path string, rate, frames int) []int32 {
	t.Helper()
	var b bytes.Buffer
	le := binary.LittleEndian
	data := frames * 6
	b.WriteString("RIFF")
	binary.Write(&b, le, uint32(36+data))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, le, uint32(16))
	binary.Write(&b, le, uint16(1))
	binary.Write(&b, le, uint16(2))
	binary.Write(&b, le, uint32(rate))
	binary.Write(&b, le, uint32(rate*6))
	binary.Write(&b, le, uint16(6))
	binary.Write(&b, le, uint16(24))
	b.WriteString("data")
	binary.Write(&b, le, uint32(data))
	ref := make([]int32, 2*frames)
	for i := 0; i < frames; i++ {
		l := int32(math.Round(0.5 * math.Sin(2*math.Pi*1000*float64(i)/float64(rate)) * (1 << 23)))
		r := int32(i%(1<<22)) - 1<<21
		for _, v := range []int32{l, r} {
			u := uint32(v)
			b.Write([]byte{byte(u), byte(u >> 8), byte(u >> 16)})
		}
		ref[2*i], ref[2*i+1] = l<<8, r<<8
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return ref
}

func unpackS32(b []byte) []int32 {
	out := make([]int32, len(b)/4)
	for i := range out {
		out[i] = int32(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

// drain reads a stream to its end through Out with a fixed plan.
func drain(t *testing.T, s *Stream, p Plan, gain float64) []byte {
	t.Helper()
	var all []byte
	buf := make([]byte, p.Rate/50*p.BytesPerFrame())
	for i := 0; i < 100000; i++ {
		n, more, err := s.Out(p, gain, p.Rate/50, buf)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, buf[:n]...)
		if !more {
			return all
		}
	}
	t.Fatal("the stream does not end")
	return nil
}

var virtio = Caps{Alts: []Alt{{Formats: []string{"S16_LE", "S32_LE", "FLOAT_LE"}, MinCh: 1, MaxCh: 2, MinRate: 8000, MaxRate: 384000}}}

func TestBitPerfectWAV(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.wav")
	ref := wav24(t, p, 96000, 30000)
	s, err := Open(Spec{Path: p}, Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f := s.Native()
	if f.Rate != 96000 || f.Bits != 24 || f.Channels != 2 {
		t.Fatalf("format %v", f)
	}
	plan := Negotiate(f, virtio, 1, DSDAuto)
	if plan.Mode != ModePCM || plan.Format != "S32_LE" || plan.Rate != 96000 || !plan.BitPerfect {
		t.Fatalf("plan %v", plan)
	}
	got := unpackS32(drain(t, s, plan, 1))
	if len(got) != len(ref) {
		t.Fatalf("%d samples, want %d", len(got), len(ref))
	}
	for i := range ref {
		if got[i] != ref[i] {
			t.Fatalf("sample %d: %#x, want %#x", i, got[i], ref[i])
		}
	}
}

func TestNegotiation(t *testing.T) {
	usb := ParseUSBStream(usbStream)
	cases := []struct {
		name    string
		src     Format
		caps    Caps
		gain    float64
		dsd     string
		mode    string
		format  string
		rate    int
		ch      int
		perfect bool
	}{
		{"16/44.1 as is", Format{Rate: 44100, Channels: 2, Bits: 16}, virtio, 1, "", ModePCM, "S16_LE", 44100, 2, true},
		{"24/96 in 32 bits", Format{Rate: 96000, Channels: 2, Bits: 24}, virtio, 1, "", ModePCM, "S32_LE", 96000, 2, true},
		{"volume: not bit-perfect", Format{Rate: 96000, Channels: 2, Bits: 24}, virtio, 0.7, "", ModePCM, "S32_LE", 96000, 2, false},
		{"mono doubled", Format{Rate: 48000, Channels: 1, Bits: 16}, Caps{Alts: []Alt{{Formats: []string{"S16_LE"}, MinCh: 2, MaxCh: 2, MinRate: 48000, MaxRate: 48000}}}, 1, "", ModePCM, "S16_LE", 48000, 2, true},
		{"float converted", Format{Rate: 48000, Channels: 2, Bits: 24, Float: true}, virtio, 1, "", ModePCM, "S32_LE", 48000, 2, false},
		{"USB DAC 24/192", Format{Rate: 192000, Channels: 2, Bits: 24}, usb, 1, "", ModePCM, "S24_3LE", 192000, 2, true},
		{"USB DAC lacks 88.2", Format{Rate: 88200, Channels: 2, Bits: 24}, usb, 1, "", ModeMixer, "", 0, 0, false},
		{"native DSD", Format{Rate: dsd.Base, Channels: 2, Bits: 1, DSD: true}, usb, 1, DSDAuto, ModeDSD, "DSD_U32_BE", 88200, 2, true},
		{"DSD256 native", Format{Rate: 4 * dsd.Base, Channels: 2, Bits: 1, DSD: true}, usb, 1, DSDAuto, ModeDSD, "DSD_U32_BE", 352800, 2, true},
		{"DoP chosen", Format{Rate: dsd.Base, Channels: 2, Bits: 1, DSD: true}, usb, 1, DSDDoP, ModeDoP, "S24_3LE", 176400, 2, true},
		{"DSD with volume to PCM", Format{Rate: dsd.Base, Channels: 2, Bits: 1, DSD: true}, usb, 0.5, DSDAuto, ModePCM, "S24_3LE", 176400, 2, false},
		{"DSD to PCM by choice", Format{Rate: dsd.Base, Channels: 2, Bits: 1, DSD: true}, usb, 1, DSDPCM, ModePCM, "S24_3LE", 176400, 2, false},
		{"no native DSD on virtio", Format{Rate: dsd.Base, Channels: 2, Bits: 1, DSD: true}, virtio, 1, DSDAuto, ModePCM, "S32_LE", 176400, 2, false},
		{"DoP in 32 bits when chosen", Format{Rate: dsd.Base, Channels: 2, Bits: 1, DSD: true}, virtio, 1, DSDDoP, ModeDoP, "S32_LE", 176400, 2, true},
		{"multichannel DSD mixed down", Format{Rate: dsd.Base, Channels: 6, Bits: 1, DSD: true}, usb, 1, DSDAuto, ModePCM, "S24_3LE", 176400, 2, false},
		{"5.1 FLAC mixed down", Format{Rate: 48000, Channels: 6, Bits: 24}, virtio, 1, "", ModePCM, "S32_LE", 48000, 2, false},
		{"null sink takes all", Format{Rate: 352800, Channels: 2, Bits: 32}, Caps{Any: true}, 1, "", ModePCM, "S32_LE", 352800, 2, true},
	}
	for _, c := range cases {
		p := Negotiate(c.src, c.caps, c.gain, c.dsd)
		if p.Mode != c.mode || p.Format != c.format || p.Rate != c.rate || p.Channels != c.ch || p.BitPerfect != c.perfect {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
}

const usbStream = `Topping D50s at usb-xhci-hcd.0-1, high speed : USB Audio

Playback:
  Status: Stop
  Interface 1
    Altset 1
    Format: S16_LE
    Channels: 2
    Endpoint: 0x01 (1 OUT) (ASYNC)
    Rates: 44100, 48000, 96000, 192000
    Data packet interval: 125 us
    Bits: 16
  Interface 1
    Altset 2
    Format: S24_3LE
    Channels: 2
    Endpoint: 0x01 (1 OUT) (ASYNC)
    Rates: 44100, 48000, 96000, 176400, 192000, 352800, 384000
    Bits: 24
  Interface 1
    Altset 3
    Format: DSD_U32_BE
    Channels: 2
    Endpoint: 0x01 (1 OUT) (ASYNC)
    Rates: 88200, 176400, 352800
    Bits: 32

Capture:
  Interface 2
    Altset 1
    Format: S16_LE
    Channels: 2
    Rates: 48000
`

func TestParseHWParams(t *testing.T) {
	c, err := ParseHWParams(`Playing raw data '/dev/zero' : Signed 16 bit Little Endian, Rate 48000 Hz, Stereo
HW Params of device "hw:0,0":
--------------------
ACCESS:  MMAP_INTERLEAVED RW_INTERLEAVED
FORMAT:  S16_LE S32_LE
SUBFORMAT:  STD
SAMPLE_BITS: [16 32]
FRAME_BITS: [32 64]
CHANNELS: 2
RATE: [44100 192000]
PERIOD_TIME: (83 1486078)
--------------------
`)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Supports("S32_LE", 2, 96000) || c.Supports("S24_3LE", 2, 96000) || c.Supports("S16_LE", 1, 48000) || c.Supports("S16_LE", 2, 384000) {
		t.Fatalf("%+v", c)
	}
	usb := ParseUSBStream(usbStream)
	if len(usb.Alts) != 3 || !usb.Supports("DSD_U32_BE", 2, 88200) || usb.Supports("S24_3LE", 2, 88200) || usb.Supports("S16_LE", 2, 176400) {
		t.Fatalf("%+v", usb)
	}
}

func TestDSFOut(t *testing.T) {
	frames := dsdtest.Tone(dsd.Base, 2, 0.2, []float64{1000, 1500}, 0.5)
	p := filepath.Join(t.TempDir(), "a.dsf")
	os.WriteFile(p, dsdtest.DSF(dsd.Base, 2, frames, nil), 0o644)
	open := func() *Stream {
		s, err := Open(Spec{Path: p}, Options{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	usb := ParseUSBStream(usbStream)
	s := open()
	native := drain(t, s, Negotiate(s.Native(), usb, 1, DSDAuto), 1)
	s.Close()
	if len(native) != len(frames)/8*8 || !bytes.Equal(native[:8], []byte{frames[0], frames[2], frames[4], frames[6], frames[1], frames[3], frames[5], frames[7]}) {
		t.Fatalf("native DSD: %d bytes, starts % x", len(native), native[:8])
	}
	s = open()
	plan := Negotiate(s.Native(), usb, 1, DSDDoP)
	dop := drain(t, s, plan, 1)
	s.Close()
	if plan.Mode != ModeDoP || len(dop) != len(frames)/4*3*2 {
		t.Fatalf("DoP %v: %d bytes", plan, len(dop))
	}
	for i := 0; i+6 <= len(dop); i += 6 {
		want := byte(0x05)
		if i/6%2 == 1 {
			want = 0xfa
		}
		if dop[i+2] != want || dop[i+5] != want {
			t.Fatalf("frame %d markers %#x %#x", i/6, dop[i+2], dop[i+5])
		}
	}
	if dop[1] != frames[0] || dop[0] != frames[2] || dop[4] != frames[1] {
		t.Fatalf("DoP payload % x for frames % x", dop[:6], frames[:4])
	}
	s = open()
	plan = Negotiate(s.Native(), virtio, 1, DSDAuto)
	pcm := unpackS32(drain(t, s, plan, 1))
	s.Close()
	if plan.Rate != 176400 || len(pcm) != len(frames)/2/2*2 {
		t.Fatalf("PCM %v: %d samples", plan, len(pcm))
	}
	peak := 0.0
	for _, v := range pcm[len(pcm)/2:] {
		peak = math.Max(peak, math.Abs(float64(v)/(1<<31)))
	}
	if peak < 0.47 || peak > 0.53 {
		t.Fatalf("DSD at 0.5 is %.3f in PCM", peak)
	}
}

// dominant estimates a frequency by zero crossings.
func dominant(x []float64, rate float64) float64 {
	c := 0
	for i := 1; i < len(x); i++ {
		if (x[i-1] < 0) != (x[i] < 0) {
			c++
		}
	}
	return float64(c) / 2 * rate / float64(len(x))
}

func TestMixerPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.wav")
	wav24(t, p, 44100, 44100)
	s, err := Open(Spec{Path: p, GainDB: -6.0206}, Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, r := make([]float64, 960), make([]float64, 960)
	var left []float64
	for s.FillStereo(l, r) {
		left = append(left, l...)
	}
	left = append(left, l...)
	if d := len(left) - 48000; d < -960 || d > 2000 {
		t.Fatalf("%d samples at 48 kHz for 1 s", len(left))
	}
	mid := left[10000:40000]
	if f := dominant(mid, 48000); math.Abs(f-1000) > 5 {
		t.Fatalf("1 kHz plays at %.1f Hz", f)
	}
	peak := 0.0
	for _, v := range mid {
		peak = math.Max(peak, math.Abs(v))
	}
	if math.Abs(peak-0.25) > 0.01 {
		t.Fatalf("0.5 with -6 dB ReplayGain peaks at %.3f", peak)
	}
	if s.Len() != 48000 {
		t.Fatalf("Len %d", s.Len())
	}
}

func TestResamplerStopBand(t *testing.T) {
	r := newResampler(192000, 48000)
	n := 192000 / 2
	a, b := make([]float32, n), make([]float32, n)
	for i := range a {
		a[i] = float32(0.5 * math.Sin(2*math.Pi*30000*float64(i)/192000))
		b[i] = float32(0.5 * math.Sin(2*math.Pi*5000*float64(i)/192000))
	}
	r.push(a, b)
	l, rr := make([]float64, n/4), make([]float64, n/4)
	k := r.pull(l, rr)
	var pl, pr float64
	for i := k / 4; i < k; i++ {
		pl = math.Max(pl, math.Abs(l[i]))
		pr = math.Max(pr, math.Abs(rr[i]))
	}
	if 20*math.Log10(pl/0.5) > -60 || math.Abs(pr-0.5) > 0.01 {
		t.Fatalf("30 kHz leaks at %.1f dB, 5 kHz at %.3f", 20*math.Log10(pl/0.5), pr)
	}
}

func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
}

// ALAC and WavPack go through ffmpeg: still the WAV's samples exactly.
func TestFFmpegCodecsAreLossless(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wav")
	ref := wav24(t, src, 96000, 96000)
	for _, c := range []struct{ file, codec, fmt string }{{"a.m4a", "alac", "s32p"}, {"a.wv", "wavpack", "s32p"}} {
		out := filepath.Join(dir, c.file)
		ffmpeg(t, "-i", src, "-c:a", c.codec, "-sample_fmt", c.fmt, out)
		s, err := Open(Spec{Path: out}, Options{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		plan := Negotiate(s.Native(), virtio, 1, "")
		got := unpackS32(drain(t, s, plan, 1))
		if !plan.BitPerfect || len(got) != len(ref) {
			t.Fatalf("%s: %v, %d samples of %d", c.codec, plan, len(got), len(ref))
		}
		for i := range ref {
			if got[i] != ref[i] {
				t.Fatalf("%s: sample %d %#x, want %#x", c.codec, i, got[i], ref[i])
			}
		}
		if err := s.SeekMS(500); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 1000*8)
		n, _, _ := s.Out(plan, 1, 1000, buf)
		after := unpackS32(buf[:n])
		for i := range after {
			if after[i] != ref[48000*2+i] {
				t.Fatalf("%s after seek: sample %d %#x, want %#x", c.codec, i, after[i], ref[48000*2+i])
			}
		}
		s.Close()
	}
}

func TestDSTFramesThroughFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	frames := dsdtest.Tone(dsd.Base, 2, 0.5, []float64{1000}, 0.5)
	p := filepath.Join(t.TempDir(), "a.dff")
	os.WriteFile(p, dsdtest.DFF(dsd.Base, 2, frames, true, true, "", ""), 0o644)
	x, err := dsd.OpenDFFDST(p)
	if err != nil {
		t.Fatal(err)
	}
	d, err := openDST(x)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Past a batch boundary: the batches join with exactly a frame's worth
	// of output for every frame.
	if err := d.SeekFrame(2352*3 + 100); err != nil {
		t.Fatal(err)
	}
	var n int
	buf := make([]int32, 4096)
	for {
		k, err := d.Read(buf)
		n += k
		if err != nil || k == 0 {
			break
		}
	}
	if want := (int(x.Count())*2352 - (2352*3 + 100)) * 2; n != want {
		t.Fatalf("%d samples after the seek, want %d", n, want)
	}
}
