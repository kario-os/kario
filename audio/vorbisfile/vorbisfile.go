// Package vorbisfile plays Ogg Vorbis music files while decoding them: one
// decoded block is held at a time, mixed to mono and resampled by nearest
// sample to 48 kHz, as audio.MP3 does. The decoder is pure Go
// (github.com/jfreymuth/oggvorbis, MIT); like audio/opusfile it is kept out
// of package audio so only phone-audiod links it.
package vorbisfile

import (
	"errors"
	"os"

	"github.com/jfreymuth/oggvorbis"

	"github.com/kario-os/kario/audio"
)

// File is an audio.Source.
type File struct {
	f     *os.File
	r     *oggvorbis.Reader
	ch    int
	rate  int
	block []float32 // decoded values, interleaved
	have  int       // values in block
	off   int       // next value in block
	frame int64     // index of the input frame in cur
	cur   float64
	end   int // output samples, 0 if unknown
	pos   int
}

// Open opens path for streaming; the file stays open until Close.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r, err := oggvorbis.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	ch, rate := r.Channels(), r.SampleRate()
	if ch < 1 || ch > 2 || rate <= 0 {
		f.Close()
		return nil, errors.New("Vorbis file: only mono and stereo are supported")
	}
	v := &File{f: f, r: r, ch: ch, rate: rate, block: make([]float32, 4096*ch), frame: -1}
	if n := r.Length(); n > 0 {
		v.end = int(n * audio.Rate / int64(rate))
	}
	return v, nil
}

// next decodes the following input frame into cur.
func (v *File) next() bool {
	for v.off+v.ch > v.have {
		n, err := v.r.Read(v.block)
		v.have, v.off = n, 0
		if n == 0 && err != nil {
			return false
		}
	}
	s := 0.0
	for c := 0; c < v.ch; c++ {
		s += float64(v.block[v.off+c])
	}
	v.cur = s / float64(v.ch)
	v.off += v.ch
	v.frame++
	return true
}

func (v *File) Fill(buf []float64) bool {
	for i := range buf {
		want := int64(v.pos) * int64(v.rate) / audio.Rate
		for v.frame < want {
			if !v.next() {
				clear(buf[i:])
				return false
			}
		}
		buf[i] = v.cur
		v.pos++
	}
	return true
}

func (v *File) Pos() int { return v.pos }

func (v *File) Len() int { return v.end }

func (v *File) Close() error { return v.f.Close() }
