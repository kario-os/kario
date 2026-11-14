package dsd_test

import (
	"bytes"
	"io"
	"math"
	"math/cmplx"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/dsd/dsdtest"
)

// spectrum is the power at bins 0..maxHz of x (one channel; the tests
// take whole periods of their tone, so no window is needed).
func spectrum(x []float64, rate, maxHz float64) (bin float64, power []float64) {
	n := len(x)
	bin = rate / float64(n)
	power = make([]float64, int(maxHz/bin))
	w := make([]complex128, n)
	for i := range x {
		w[i] = complex(x[i], 0)
	}
	for k := range power {
		var s complex128
		step := cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n)))
		e := complex(1, 0)
		for i := range w {
			s += w[i] * e
			e *= step
		}
		power[k] = real(s)*real(s) + imag(s)*imag(s)
	}
	return
}

// snr is the tone's power over everything else up to 20 kHz, in dB.
func snr(x []float64, rate, f float64) float64 {
	bin, p := spectrum(x, rate, 20000)
	k := int(math.Round(f / bin))
	var sig, noise float64
	for i, v := range p {
		switch {
		case i < 3:
		case i >= k-1 && i <= k+1:
			sig += v
		default:
			noise += v
		}
	}
	return 10 * math.Log10(sig/noise)
}

func decimate(t *testing.T, rate, pcmRate int, frames []byte, ch int) []float32 {
	d, err := dsd.NewDecimator(rate, ch, pcmRate)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, (len(frames)/ch/d.Ratio()+1)*ch)
	n := d.Process(frames, out)
	return out[:n*ch]
}

func TestDecimationIsCleanAndAtLevel(t *testing.T) {
	for _, c := range []struct {
		rate, pcm int
	}{{dsd.Base, 176400}, {dsd.Base, 88200}, {2 * dsd.Base, 176400}, {4 * dsd.Base, 176400}} {
		frames := dsdtest.Tone(c.rate, 1, 0.12, []float64{1000}, 0.5)
		pcm := decimate(t, c.rate, c.pcm, frames, 1)
		if want := len(frames) * 8 / (c.rate / c.pcm); len(pcm) != want {
			t.Fatalf("%s → %d: %d samples, want %d", dsd.Name(c.rate), c.pcm, len(pcm), want)
		}
		x := make([]float64, c.pcm/20)
		for i := range x {
			x[i] = float64(pcm[len(pcm)-len(x)+i])
		}
		if s := snr(x, float64(c.pcm), 1000); s < 70 {
			t.Errorf("%s → %d: SNR %.1f dB to 20 kHz", dsd.Name(c.rate), c.pcm, s)
		}
		peak := 0.0
		for _, v := range x {
			peak = math.Max(peak, math.Abs(v))
		}
		if math.Abs(20*math.Log10(peak/0.5)) > 0.3 {
			t.Errorf("%s → %d: peak %.3f for a 0.5 tone", dsd.Name(c.rate), c.pcm, peak)
		}
	}
	if _, err := dsd.NewDecimator(dsd.Base, 2, 48000); err == nil {
		t.Fatal("DSD64 to 48 kHz is not whole bytes and must be refused")
	}
}

func TestSilenceIsQuiet(t *testing.T) {
	pcm := decimate(t, dsd.Base, 176400, bytes.Repeat([]byte{dsd.Silence}, 40000), 1)
	for _, v := range pcm {
		if math.Abs(float64(v)) > 1e-4 {
			t.Fatalf("idle pattern gives %v", v)
		}
	}
}

func TestDoP(t *testing.T) {
	var p dsd.DoP
	in := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	out := make([]int32, 4)
	if n := p.Pack(in, 2, out); n != 4 {
		t.Fatalf("%d samples", n)
	}
	want := []uint32{0x051133, 0x052244, 0xfa5577, 0xfa6688}
	for i, w := range want {
		if uint32(out[i])>>8 != w || out[i]&0xff != 0 {
			t.Fatalf("sample %d is %08x, want %06x00", i, uint32(out[i]), w)
		}
	}
	p.Pack(in[:4], 2, out)
	if uint32(out[0])>>24 != 0x05 {
		t.Fatal("markers must keep alternating across calls")
	}
	if dsd.DoPRate(dsd.Base) != 176400 || dsd.DoPRate(2*dsd.Base) != 352800 {
		t.Fatal("DoP rates")
	}
}

func TestNativeDSDFormats(t *testing.T) {
	in := []byte{0xa0, 0xb0, 0xa1, 0xb1, 0xa2, 0xb2, 0xa3, 0xb3}
	for f, want := range map[string][]byte{
		dsd.FormatU8:    {0xa0, 0xb0, 0xa1, 0xb1, 0xa2, 0xb2, 0xa3, 0xb3},
		dsd.FormatU16BE: {0xa0, 0xa1, 0xb0, 0xb1, 0xa2, 0xa3, 0xb2, 0xb3},
		dsd.FormatU16LE: {0xa1, 0xa0, 0xb1, 0xb0, 0xa3, 0xa2, 0xb3, 0xb2},
		dsd.FormatU32BE: {0xa0, 0xa1, 0xa2, 0xa3, 0xb0, 0xb1, 0xb2, 0xb3},
		dsd.FormatU32LE: {0xa3, 0xa2, 0xa1, 0xa0, 0xb3, 0xb2, 0xb1, 0xb0},
	} {
		out := make([]byte, 8)
		if n := dsd.PackNative(f, in, 2, out); n != 8 || !bytes.Equal(out, want) {
			t.Errorf("%s: % x, want % x", f, out[:n], want)
		}
	}
}

func readAll(t *testing.T, s dsd.Stream) []byte {
	var out []byte
	buf := make([]byte, 1000*s.Channels())
	for {
		n, err := s.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, name string, b []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDSFAndDFF(t *testing.T) {
	frames := dsdtest.Tone(dsd.Base, 2, 0.05, []float64{1000, 1500}, 0.5)
	frames = frames[:len(frames)-2*3] // not a whole block
	f, err := dsd.OpenDSF(write(t, "a.dsf", dsdtest.DSF(dsd.Base, 2, frames, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Len() != int64(len(frames)/2) || f.Rate() != dsd.Base || !f.Info.LSBFirst {
		t.Fatalf("DSF %+v", f.Info)
	}
	if got := readAll(t, f); !bytes.Equal(got, frames) {
		t.Fatal("DSF frames differ")
	}
	f.SeekFrame(5000)
	if got := readAll(t, f); !bytes.Equal(got, frames[10000:]) {
		t.Fatal("DSF after seek")
	}
	d, err := dsd.OpenDFF(write(t, "a.dff", dsdtest.DFF(dsd.Base, 2, frames, false, false, "Title", "Artist")))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Info.Title != "Title" || d.Info.Artist != "Artist" || d.Len() != int64(len(frames)/2) {
		t.Fatalf("DFF %+v", d.Info)
	}
	d.SeekFrame(77)
	if got := readAll(t, d); !bytes.Equal(got, frames[154:]) {
		t.Fatal("DFF frames differ")
	}
}

func TestDSTDFF(t *testing.T) {
	frames := dsdtest.Tone(dsd.Base, 2, 0.1, []float64{1000}, 0.5)
	per := dsd.FrameBytes * 2
	for _, index := range []bool{true, false} {
		x, err := dsd.OpenDFFDST(write(t, "a.dff", dsdtest.DFF(dsd.Base, 2, frames, true, index, "", "")))
		if err != nil {
			t.Fatal(err)
		}
		if x.Count() != int64((len(frames)+per-1)/per) || (x.Info.Index != nil) != index {
			t.Fatalf("index %v: %d frames, index %d", index, x.Count(), len(x.Info.Index))
		}
		x.SeekFrame(3)
		f, err := x.Next()
		if err != nil || len(f) != 1+per || !bytes.Equal(f[1:], frames[3*per:4*per]) {
			t.Fatalf("index %v: frame 3 %d bytes, %v", index, len(f), err)
		}
		x.Close()
	}
}

func TestSACDImage(t *testing.T) {
	t1 := dsdtest.Tone(dsd.Base, 2, 0.4, []float64{500}, 0.5)
	t2 := dsdtest.Tone(dsd.Base, 2, 0.3, []float64{700}, 0.5)
	mc := dsdtest.Tone(dsd.Base, 6, 0.2, []float64{400, 400, 800, 100, 400, 400}, 0.4)
	img := dsdtest.SACD("\u6d4b\u8bd5\u5531\u7247", "\u6f14\u594f\u8005",
		dsdtest.Area{Channels: 2, Tracks: []dsdtest.Track{{"\u7b2c\u4e00\u9996", "A", t1}, {"Two", "B", t2}}},
		dsdtest.Area{Channels: 6, Speakers: 4, DST: true, Tracks: []dsdtest.Track{{"Multi", "C", mc}}})
	s, err := dsd.OpenSACD(write(t, "a.iso", img))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Title != "\u6d4b\u8bd5\u5531\u7247" || s.Artist != "\u6f14\u594f\u8005" || len(s.Areas) != 2 {
		t.Fatalf("disc %q %q, %d areas", s.Title, s.Artist, len(s.Areas))
	}
	st := s.Stereo()
	if st.Multichannel || st.DST || st.Channels != 2 || len(st.Tracks) != 2 || st.Tracks[0].Title != "\u7b2c\u4e00\u9996" || st.Tracks[1].Performer != "B" {
		t.Fatalf("stereo area %+v", st)
	}
	per := dsd.FrameBytes * 2
	for i, want := range [][]byte{t1, t2} {
		tr, err := s.OpenTrack(st, i+1)
		if err != nil {
			t.Fatal(err)
		}
		r := tr.(*dsd.SACDDSD)
		got := readAll(t, r)
		nf := (len(want) + per - 1) / per
		if len(got) != nf*per || !bytes.Equal(got[:len(want)], want) {
			t.Fatalf("track %d: %d bytes, want %d", i+1, len(got), nf*per)
		}
		r.SeekFrame(int64(dsd.FrameBytes*7 + 100))
		rest := readAll(t, r)
		if !bytes.Equal(rest, got[per*7+200:]) {
			t.Fatalf("track %d after seek: %d bytes", i+1, len(rest))
		}
	}
	m := s.Areas[1]
	if !m.Multichannel || !m.DST || m.Channels != 6 || m.Speakers != 4 {
		t.Fatalf("multichannel area %+v", m)
	}
	tr, _ := s.OpenTrack(m, 1)
	x := tr.(*dsd.SACDDST)
	x.SeekFrame(5)
	f, err := x.Next()
	if err != nil || len(f) != 1+6*dsd.FrameBytes || !bytes.Equal(f[1:], mc[5*6*dsd.FrameBytes:6*6*dsd.FrameBytes]) {
		t.Fatalf("DST frame 5: %d bytes, %v", len(f), err)
	}
	n := 1
	for {
		if _, err := x.Next(); err != nil {
			break
		}
		n++
	}
	if int64(n+5) != x.Count() {
		t.Fatalf("%d frames after frame 5, the track has %d", n, x.Count())
	}
}

// The DST wrapper against ffmpeg's DST decoder: it takes the file and
// gives a frame's worth of samples for each frame. There is no DST encoder
// to make coded frames with, and ffmpeg lays out the frames DST leaves
// uncoded wrongly (it copies them packed into a buffer it reads with a
// stride of four), so only the structure is checked; audio/hifi plays
// uncoded frames itself.
func TestDSTThroughFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	frames := dsdtest.Tone(dsd.Base, 2, 0.4, []float64{1000, 2000}, 0.5)
	per := dsd.FrameBytes * 2
	var coded [][]byte
	n := len(frames) / per
	for i := 0; i < n; i++ {
		coded = append(coded, append([]byte{0}, frames[i*per:(i+1)*per]...))
	}
	path := write(t, "w.dff", dsd.DSTFile(dsd.Base, 2, coded))
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-ar", "176400", "-f", "s32le", "-c:a", "pcm_s32le", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(out)/8, n*dsd.FrameBytes*8/16; math.Abs(float64(got-want)) > 200 {
		t.Fatalf("ffmpeg gave %d frames, want about %d", got, want)
	}
}

// One second of stereo DSD64 to 176.4 kHz PCM (the CPU it costs a phone
// is ns/op over 1e9).
func BenchmarkDecimateDSD64(b *testing.B) {
	frames := dsdtest.Tone(dsd.Base, 2, 1, []float64{1000}, 0.5)
	d, _ := dsd.NewDecimator(dsd.Base, 2, 176400)
	out := make([]float32, (len(frames)/2/d.Ratio()+1)*2)
	b.SetBytes(int64(len(frames)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Process(frames, out)
	}
}
