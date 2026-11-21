// Package opusfile plays Ogg Opus music files (RFC 7845) while decoding
// them: one packet (at most 120 ms) is held at a time, mixed to mono at
// 48 kHz, Opus's own rate, so nothing is resampled. The decoder is libopus
// transpiled to Go (github.com/kazzmir/opus-go, as in audio/voice); it is
// kept out of package audio so only phone-audiod links it.
package opusfile

import (
	"errors"
	"os"

	"github.com/kazzmir/opus-go/ogg"
	"github.com/kazzmir/opus-go/opus"

	"github.com/kario-os/kario/audio"
)

// File is an audio.Source.
type File struct {
	f    *os.File
	r    *ogg.OpusReader
	dec  *opus.Decoder
	ch   int
	pcm  []int16 // the decoded packet, interleaved
	have int     // frames in pcm
	off  int     // next frame in pcm
	skip int     // pre-skip frames still to drop
	end  int     // frames in the stream (the last granule less the pre-skip), 0 if unknown
	pos  int
}

// Open opens path for streaming; the file stays open until Close.
func Open(path string) (*File, error) {
	n, err := audio.OpusLen(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r, err := ogg.NewOpusReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	ch := int(r.Head.Channels)
	if ch != 1 && ch != 2 {
		f.Close()
		return nil, errors.New("Opus file: only mono and stereo are supported")
	}
	dec, err := opus.NewDecoder(audio.Rate, ch)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{f: f, r: r, dec: dec, ch: ch, pcm: make([]int16, 120*audio.Rate/1000*ch), skip: int(r.Head.PreSkip), end: n}, nil
}

func (o *File) next() bool {
	for o.off >= o.have {
		p, err := o.r.ReadAudioPacket()
		if err != nil {
			return false
		}
		n, err := o.dec.Decode(p.Data, o.pcm, len(o.pcm)/o.ch, false)
		if err != nil {
			return false
		}
		o.have, o.off = n, 0
		if o.skip > 0 {
			d := min(o.skip, n)
			o.skip -= d
			o.off = d
		}
	}
	return true
}

func (o *File) Fill(buf []float64) bool {
	for i := range buf {
		if o.end > 0 && o.pos >= o.end || !o.next() {
			return false
		}
		k := o.off * o.ch
		v := float64(o.pcm[k])
		if o.ch == 2 {
			v = (v + float64(o.pcm[k+1])) / 2
		}
		buf[i] = v / 32768
		o.off++
		o.pos++
	}
	return true
}

func (o *File) Pos() int { return o.pos }

func (o *File) Len() int { return o.end }

func (o *File) Close() error {
	o.dec.Close()
	return o.f.Close()
}

var _ audio.Source = (*File)(nil)
