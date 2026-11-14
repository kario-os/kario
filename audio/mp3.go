package audio

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/hajimehoshi/go-mp3"
)

// MP3 plays an MP3 file while decoding it (go-mp3: MPEG-1 Layer III in pure
// Go), mixed to mono and resampled by nearest sample to 48 kHz like
// Wav. Only one decoded chunk is held at a time: a whole song decoded up
// front would be tens of MB.
type MP3 struct {
	f      *os.File
	d      *mp3.Decoder
	rate   int
	frames int // total input frames, 0 if unknown
	chunk  []byte
	have   int   // valid bytes in chunk
	off    int   // read offset in chunk
	frame  int64 // index of the frame at off
	cur    float64
	pos    int // output samples
}

// OpenMP3 opens path for streaming; the file stays open until Close.
func OpenMP3(path string) (*MP3, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	d, err := mp3.NewDecoder(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	switch d.SampleRate() {
	case 32000, 44100, 48000:
	default:
		// go-mp3 decodes MPEG-2 / 2.5 (lower rates) wrongly, without an error.
		f.Close()
		return nil, fmt.Errorf("%d Hz MP3: only MPEG-1 Layer III (32, 44.1, 48 kHz) is supported", d.SampleRate())
	}
	m := &MP3{f: f, d: d, rate: d.SampleRate(), chunk: make([]byte, 16*1024), frame: -1}
	if n := d.Length(); n > 0 {
		m.frames = int(n / 4)
	}
	return m, nil
}

// next decodes the following stereo frame into cur.
func (m *MP3) next() bool {
	if m.off+4 > m.have {
		copy(m.chunk, m.chunk[m.off:m.have])
		m.have -= m.off
		m.off = 0
		for m.have < 4 {
			n, err := m.d.Read(m.chunk[m.have:])
			m.have += n
			if err != nil && m.have < 4 {
				return false
			}
		}
	}
	l := int16(binary.LittleEndian.Uint16(m.chunk[m.off:]))
	r := int16(binary.LittleEndian.Uint16(m.chunk[m.off+2:]))
	m.cur = (float64(l) + float64(r)) / 65536
	m.off += 4
	m.frame++
	return true
}

func (m *MP3) Fill(buf []float64) bool {
	for i := range buf {
		want := int64(m.pos) * int64(m.rate) / Rate
		for m.frame < want {
			if !m.next() {
				return false
			}
		}
		buf[i] = m.cur
		m.pos++
	}
	return true
}

func (m *MP3) Pos() int { return m.pos }

func (m *MP3) Len() int {
	if m.rate == 0 {
		return 0
	}
	return m.frames * Rate / m.rate
}

func (m *MP3) Close() error { return m.f.Close() }
