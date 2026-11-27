package flac

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// encode makes a FLAC with the host's ffmpeg and returns it with ffmpeg's
// own decoding (s32le, left-justified like ours) as the reference.
func encode(t *testing.T, src string, rate, ch, bits int, extra ...string) (string, []int32) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg on this machine")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "a.flac")
	fmtArg := "s16"
	if bits > 16 {
		fmtArg = "s32"
	}
	args := []string{"-v", "error", "-f", "lavfi", "-i", src, "-ar", itoa(rate), "-ac", itoa(ch), "-sample_fmt", fmtArg}
	if bits > 16 {
		args = append(args, "-bits_per_raw_sample", itoa(bits))
	}
	args = append(append(args, extra...), "-c:a", "flac", out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", out, "-f", "s32le", "-c:a", "pcm_s32le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	ref := make([]int32, len(raw)/4)
	for i := range ref {
		ref[i] = int32(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return out, ref
}

func itoa(n int) string { return strconv.Itoa(n) }

func decodeAll(t *testing.T, d *Decoder) []int32 {
	t.Helper()
	var out []int32
	buf := make([]int32, 4096*d.Channels())
	for {
		n, err := d.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func same(t *testing.T, what string, got, want []int32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d samples, want %d", what, len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: sample %d is %d, want %d", what, i, got[i], want[i])
		}
	}
}

func TestBitExactAgainstFFmpeg(t *testing.T) {
	signal := "aevalsrc=0.6*sin(440*2*PI*t)+0.02*random(0)|0.4*sin(997*2*PI*t)+0.3*sin(13*2*PI*t):d=1.3"
	for _, c := range []struct {
		name           string
		rate, ch, bits int
		extra          []string
	}{
		{"16/44.1 stereo", 44100, 2, 16, nil},
		{"24/96 stereo", 96000, 2, 24, nil},
		{"24/192 stereo", 192000, 2, 24, nil},
		{"16/48 mono", 48000, 1, 16, nil},
		{"24/96 independent channels", 96000, 2, 24, []string{"-ch_mode", "indep"}},
		{"16/44.1 fixed predictors", 44100, 2, 16, []string{"-compression_level", "0"}},
		{"24/88.2 high LPC order", 88200, 2, 24, []string{"-compression_level", "12"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path, ref := encode(t, signal, c.rate, c.ch, c.bits, c.extra...)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			d, err := Open(f)
			if err != nil {
				t.Fatal(err)
			}
			if d.Rate() != c.rate || d.Channels() != c.ch || d.Bits() != c.bits {
				t.Fatalf("stream %d Hz %d ch %d bits", d.Rate(), d.Channels(), d.Bits())
			}
			if want := int64(len(ref) / c.ch); d.Samples() != want {
				t.Fatalf("%d samples in STREAMINFO, %d decoded by ffmpeg", d.Samples(), want)
			}
			same(t, "decode", decodeAll(t, d), ref)
			for _, at := range []int64{0, 1, 4095, 4096, d.Samples() / 2, d.Samples() - 7} {
				if err := d.SeekSample(at); err != nil {
					t.Fatal(err)
				}
				if d.Pos() != at {
					t.Fatalf("seek %d: at %d", at, d.Pos())
				}
				buf := make([]int32, 300*c.ch)
				n, _ := d.Read(buf)
				end := min(len(ref), int(at)*c.ch+len(buf))
				same(t, "after seek", buf[:n], ref[int(at)*c.ch:end])
			}
		})
	}
}

func TestSeekWithoutSeekTable(t *testing.T) {
	path, ref := encode(t, "sine=f=300:d=25", 44100, 1, 16)
	f, _ := os.Open(path)
	defer f.Close()
	d, err := Open(f)
	if err != nil {
		t.Fatal(err)
	}
	d.Meta.Seek = nil
	for _, at := range []int64{20 * 44100, 11*44100 + 333, 3} {
		if err := d.SeekSample(at); err != nil {
			t.Fatal(err)
		}
		buf := make([]int32, 1000)
		n, _ := d.Read(buf)
		same(t, "bisected seek", buf[:n], ref[at:at+1000])
	}
	if err := d.SeekSample(d.Samples()); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Read(make([]int32, 10)); n != 0 || err != io.EOF {
		t.Fatalf("at the end: %d %v", n, err)
	}
}

func TestMetadataBlocks(t *testing.T) {
	path, _ := encode(t, "sine=f=500:d=0.2", 44100, 2, 16, "-metadata", "title=Tone", "-metadata", "artist=Kario")
	f, _ := os.Open(path)
	defer f.Close()
	m, err := ReadMetadata(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bytes.ToUpper(m.Comments), []byte("TITLE=TONE")) || !bytes.Contains(bytes.ToUpper(m.Comments), []byte("ARTIST=KARIO")) {
		t.Fatalf("comments %q", m.Comments)
	}
	if m.FramesAt <= 42 {
		t.Fatalf("first frame at %d", m.FramesAt)
	}
}

func TestRejectsOtherFiles(t *testing.T) {
	if _, err := Open(bytes.NewReader([]byte("RIFF\x00\x00\x00\x00WAVEfmt "))); err == nil {
		t.Fatal("a WAV file opened as FLAC")
	}
}

// Decoding a second of 24/96 stereo.
func BenchmarkDecode2496(b *testing.B) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		b.Skip("no ffmpeg")
	}
	out := filepath.Join(b.TempDir(), "a.flac")
	if err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "anoisesrc=d=1:a=0.3:r=96000", "-ac", "2", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", out).Run(); err != nil {
		b.Skip(err)
	}
	buf := make([]int32, 8192)
	for i := 0; i < b.N; i++ {
		f, _ := os.Open(out)
		d, err := Open(f)
		if err != nil {
			b.Fatal(err)
		}
		for {
			if _, err := d.Read(buf); err != nil {
				break
			}
		}
		f.Close()
	}
}
