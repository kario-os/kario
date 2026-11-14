package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Tone plays segments of summed sine waves, optionally looping. All system
// sounds are original patterns (no Nokia tune or other borrowed melodies).
type Tone struct {
	segs  []seg
	loop  bool
	pos   int // samples into the current loop
	total int // samples played
	level float64
}

type seg struct {
	freqs []float64
	n     int // samples
}

func ms(d int) int { return d * Rate / 1000 }

func (t *Tone) Pos() int { return t.total }

func (t *Tone) Len() int {
	if t.loop {
		return 0
	}
	n := 0
	for _, s := range t.segs {
		n += s.n
	}
	return n
}

func (t *Tone) Fill(buf []float64) bool {
	period := 0
	for _, s := range t.segs {
		period += s.n
	}
	for i := range buf {
		if t.pos >= period {
			if !t.loop {
				return false
			}
			t.pos = 0
		}
		off, k := t.pos, 0
		for off >= t.segs[k].n {
			off -= t.segs[k].n
			k++
		}
		s := t.segs[k]
		if len(s.freqs) > 0 {
			// 5 ms fade in and out keeps the segments free of clicks.
			env := math.Min(1, math.Min(float64(off), float64(s.n-off))/float64(ms(5)))
			v := 0.0
			for _, f := range s.freqs {
				v += math.Sin(2 * math.Pi * f * float64(off) / Rate)
			}
			buf[i] = t.level * env * v / float64(len(s.freqs))
		}
		t.pos++
		t.total++
	}
	return true
}

func tone(level float64, loop bool, parts ...any) *Tone {
	t := &Tone{loop: loop, level: level}
	for i := 0; i+1 < len(parts); i += 2 {
		var fs []float64
		switch f := parts[i].(type) {
		case float64:
			if f > 0 {
				fs = []float64{f}
			}
		case []float64:
			fs = f
		}
		t.segs = append(t.segs, seg{freqs: fs, n: ms(parts[i+1].(int))})
	}
	return t
}

// SystemTones are the sounds the shell plays itself. Ringback and busy
// follow the Chinese network tones (450 Hz).
var SystemTones = map[string]func() *Tone{
	"ringtone": func() *Tone {
		return tone(0.5, true, 659.25, 140, 0.0, 30, 783.99, 140, 0.0, 30, 987.77, 220, 0.0, 700)
	},
	"alarm":    func() *Tone { return tone(0.6, true, 880.0, 180, 0.0, 120, 880.0, 180, 0.0, 120, 880.0, 180, 0.0, 600) },
	"ringback": func() *Tone { return tone(0.4, true, 450.0, 1000, 0.0, 4000) },
	"busy": func() *Tone {
		return tone(0.4, false, 450.0, 350, 0.0, 350, 450.0, 350, 0.0, 350, 450.0, 350, 0.0, 350)
	},
	"notify": func() *Tone { return tone(0.4, false, 1046.5, 80, 0.0, 20, 1318.5, 140) },
	// The emergency alert's attention signal: 853 and 960 Hz together,
	// 2 s then two half seconds (the CMAS and EU-Alert pattern).
	"alert": func() *Tone {
		return tone(0.7, true, []float64{853, 960}, 2000, 0.0, 500, []float64{853, 960}, 500, 0.0, 500, []float64{853, 960}, 500, 0.0, 500)
	},
	// The simulated far end of a call: two formant-like tones in
	// syllable-sized bursts (the prototype has no real voice path yet).
	"voice": func() *Tone {
		return tone(0.25, true, []float64{300, 600}, 180, 0.0, 90, []float64{330, 660}, 220, 0.0, 300, []float64{280, 560}, 160, 0.0, 450)
	},
}

// Synth plays a demo track: an original melody on a soft additive voice.
type Synth struct {
	notes []note
	idx   int
	off   int
	total int
	n     int
}

type note struct {
	midi float64 // 0 = rest
	len  int     // samples
}

func (s *Synth) Pos() int { return s.total }
func (s *Synth) Len() int { return s.n }

func (s *Synth) Fill(buf []float64) bool {
	for i := range buf {
		if s.idx >= len(s.notes) {
			return false
		}
		nt := s.notes[s.idx]
		if nt.midi > 0 {
			f := 440 * math.Pow(2, (nt.midi-69)/12)
			t := float64(s.off) / Rate
			attack := math.Min(1, float64(s.off)/float64(ms(10)))
			decay := math.Exp(-3 * float64(s.off) / float64(nt.len))
			v := math.Sin(2*math.Pi*f*t) + 0.3*math.Sin(4*math.Pi*f*t) + 0.1*math.Sin(6*math.Pi*f*t)
			buf[i] = 0.35 * attack * decay * v
		}
		s.off++
		s.total++
		if s.off >= nt.len {
			s.idx, s.off = s.idx+1, 0
		}
	}
	return true
}

// Track describes a demo track of the built-in media library.
type Track struct {
	ID, Title, Artist string
	Notes             string // "C5 E5 G5 - ..." with "-" for a rest
	BeatMS            int
	Repeat            int
}

var noteBase = map[byte]float64{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}

func parseNotes(score string, beat, repeat int) ([]note, int, error) {
	var out []note
	n := 0
	for r := 0; r < max(1, repeat); r++ {
		for _, tok := range strings.Fields(score) {
			length := beat
			if strings.HasSuffix(tok, ":2") {
				tok, length = strings.TrimSuffix(tok, ":2"), beat*2
			}
			if tok == "-" {
				out = append(out, note{0, ms(length)})
				n += ms(length)
				continue
			}
			base, ok := noteBase[tok[0]]
			if !ok || len(tok) < 2 {
				return nil, 0, fmt.Errorf("bad note %q", tok)
			}
			rest := tok[1:]
			if rest[0] == '#' {
				base++
				rest = rest[1:]
			}
			var oct int
			if _, err := fmt.Sscanf(rest, "%d", &oct); err != nil {
				return nil, 0, fmt.Errorf("bad note %q", tok)
			}
			out = append(out, note{12*float64(oct+1) + base, ms(length)})
			n += ms(length)
		}
	}
	return out, n, nil
}

// DemoTracks are generated on the fly, so the image carries no audio files.
// Their titles are in the device's language.
var DemoTracks = []Track{
	{ID: "demo:dawn", Title: tr.T("Dawn"), Artist: tr.T("Demo track"), BeatMS: 250, Repeat: 2,
		Notes: "C5 E5 G5 C6:2 B5 G5 E5 - D5 F5 A5 D6:2 C6 A5 F5 - E5 G5 B5 E6:2 D6 B5 G5 C6:2 - - -"},
	{ID: "demo:night", Title: tr.T("Night Walk"), Artist: tr.T("Demo track"), BeatMS: 300, Repeat: 2,
		Notes: "A4 C5 E5 A5:2 G5 E5 C5 - F4 A4 C5 F5:2 E5 C5 A4 - G4 B4 D5 G5:2 F5 D5 B4 A4:2 - - -"},
	{ID: "demo:rain", Title: tr.T("Rainy Lane"), Artist: tr.T("Demo track"), BeatMS: 280, Repeat: 2,
		Notes: "D5 E5 G5 A5 G5 E5 D5:2 A4 C5 D5 E5:2 D5 C5 A4:2 G4 A4 C5 D5 E5 G5 E5:2 D5:2 - - -"},
}

func TrackByID(id string) (Track, bool) {
	for _, t := range DemoTracks {
		if t.ID == id {
			return t, true
		}
	}
	return Track{}, false
}

func NewSynth(t Track) (*Synth, error) {
	notes, n, err := parseNotes(t.Notes, t.BeatMS, t.Repeat)
	if err != nil {
		return nil, err
	}
	return &Synth{notes: notes, n: n}, nil
}

// TrackLenMS is the length of a demo track.
func TrackLenMS(t Track) int {
	_, n, _ := parseNotes(t.Notes, t.BeatMS, t.Repeat)
	return n * 1000 / Rate
}

// Wav plays 16-bit PCM WAV data (mono or stereo, any rate, resampled by
// nearest sample to 48 kHz).
type Wav struct {
	pcm      []int16
	channels int
	rate     int
	pos      int // output samples
}

func (w *Wav) frames() int { return len(w.pcm) / w.channels }
func (w *Wav) Len() int    { return w.frames() * Rate / w.rate }
func (w *Wav) Pos() int    { return w.pos }

func (w *Wav) Fill(buf []float64) bool {
	for i := range buf {
		in := w.pos * w.rate / Rate
		if in >= w.frames() {
			return false
		}
		v := 0.0
		for c := 0; c < w.channels; c++ {
			v += float64(w.pcm[in*w.channels+c]) / 32768
		}
		buf[i] = v / float64(w.channels)
		w.pos++
	}
	return true
}

func OpenWav(path string) (*Wav, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return DecodeWav(f)
}

func DecodeWav(r io.Reader) (*Wav, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	w := &Wav{}
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, errors.New("WAV without data chunk")
		}
		size := binary.LittleEndian.Uint32(ch[4:])
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		switch string(ch[0:4]) {
		case "fmt ":
			if len(body) < 16 || binary.LittleEndian.Uint16(body[0:]) != 1 || binary.LittleEndian.Uint16(body[14:]) != 16 {
				return nil, errors.New("only 16-bit PCM WAV is supported")
			}
			w.channels = int(binary.LittleEndian.Uint16(body[2:]))
			w.rate = int(binary.LittleEndian.Uint32(body[4:]))
		case "data":
			if w.channels == 0 {
				return nil, errors.New("WAV data before fmt")
			}
			w.pcm = make([]int16, len(body)/2)
			for i := range w.pcm {
				w.pcm[i] = int16(binary.LittleEndian.Uint16(body[2*i:]))
			}
			return w, nil
		}
		if size%2 == 1 {
			io.ReadFull(r, make([]byte, 1))
		}
	}
}

// WavHeader is a 44-byte header for mono 16-bit PCM at Rate.
func WavHeader(dataBytes uint32) []byte { return WavHeaderFor(dataBytes, 1) }

// WavHeaderFor is a 44-byte header for 16-bit PCM at Rate with channels.
func WavHeaderFor(dataBytes uint32, channels int) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+dataBytes)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], Rate)
	binary.LittleEndian.PutUint32(h[28:], uint32(Rate*2*channels))
	binary.LittleEndian.PutUint16(h[32:], uint16(2*channels))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataBytes)
	return h
}
