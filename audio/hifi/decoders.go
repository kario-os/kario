package hifi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/kazzmir/opus-go/ogg"
	"github.com/kazzmir/opus-go/opus"

	"github.com/kario-os/kario/audio/flac"
	"github.com/kario-os/kario/audio/musicfile"
)

// PCM is a decoder at the file's own rate and depth: samples interleaved,
// left-justified in int32 (a 16-bit v is v<<16).
type PCM interface {
	Format() Format
	// Frames is the length, 0 if unknown.
	Frames() int64
	// Read fills dst (a multiple of the channel count); io.EOF at the end.
	Read(dst []int32) (int, error)
	SeekFrame(n int64) error
	Close() error
}

// openPCM opens the decoder for a file of the given kind.
func openPCM(path string, i musicfile.Info) (PCM, error) {
	switch i.Codec {
	case musicfile.FLAC:
		return openFLAC(path)
	case musicfile.PCM:
		return openRaw(path, i)
	case musicfile.MP3:
		return openMP3(path, i)
	case musicfile.Vorbis:
		return openVorbis(path, i)
	case musicfile.Opus:
		return openOpus(path, i)
	case musicfile.ALAC, musicfile.APE, musicfile.WavPack:
		return openFFmpeg(path, i)
	}
	return nil, fmt.Errorf("%s: %w", i.Codec, musicfile.ErrUnsupported)
}

type flacPCM struct {
	f *os.File
	d *flac.Decoder
}

func openFLAC(path string) (PCM, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	d, err := flac.Open(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &flacPCM{f: f, d: d}, nil
}

func (p *flacPCM) Format() Format {
	return Format{Rate: p.d.Rate(), Channels: p.d.Channels(), Bits: p.d.Bits()}
}
func (p *flacPCM) Frames() int64                 { return p.d.Samples() }
func (p *flacPCM) Read(dst []int32) (int, error) { return p.d.Read(dst) }
func (p *flacPCM) SeekFrame(n int64) error       { return p.d.SeekSample(n) }
func (p *flacPCM) Close() error                  { return p.f.Close() }

// rawPCM reads WAV and AIFF samples.
type rawPCM struct {
	f    *os.File
	info musicfile.Info
	l    musicfile.Layout
	pos  int64
	buf  []byte
}

func openRaw(path string, i musicfile.Info) (PCM, error) {
	if i.PCM == nil {
		return nil, errors.New("no PCM layout")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &rawPCM{f: f, info: i, l: *i.PCM}, nil
}

func (p *rawPCM) Format() Format {
	bits := p.info.Bits
	if p.info.Float {
		bits = 24
	}
	return Format{Rate: p.info.Rate, Channels: p.info.Channels, Bits: bits, Float: p.info.Float}
}
func (p *rawPCM) Frames() int64 { return p.info.Frames }
func (p *rawPCM) Close() error  { return p.f.Close() }

func (p *rawPCM) SeekFrame(n int64) error {
	if n < 0 || n > p.info.Frames {
		return fmt.Errorf("seek to %d of %d frames", n, p.info.Frames)
	}
	p.pos = n
	return nil
}

func (p *rawPCM) Read(dst []int32) (int, error) {
	ch, bps := p.info.Channels, p.l.BytesPerSample
	frames := min(int64(len(dst)/ch), p.info.Frames-p.pos)
	if frames <= 0 {
		return 0, io.EOF
	}
	need := int(frames) * ch * bps
	if cap(p.buf) < need {
		p.buf = make([]byte, need)
	}
	b := p.buf[:need]
	k, err := p.f.ReadAt(b, p.l.Offset+p.pos*int64(ch*bps))
	k -= k % (ch * bps)
	if k == 0 {
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	n := k / bps
	be := p.l.BigEndian
	for i := 0; i < n; i++ {
		s := b[i*bps : i*bps+bps]
		var v int32
		switch {
		case p.info.Float && bps == 4:
			u := binary.LittleEndian.Uint32(s)
			if be {
				u = binary.BigEndian.Uint32(s)
			}
			v = floatToInt(float64(math.Float32frombits(u)))
		case p.info.Float:
			u := binary.LittleEndian.Uint64(s)
			if be {
				u = binary.BigEndian.Uint64(s)
			}
			v = floatToInt(math.Float64frombits(u))
		case bps == 1 && !be:
			v = int32(int8(s[0]-128)) << 24 // WAV's 8-bit samples are unsigned
		default:
			var u uint32
			for j := 0; j < bps; j++ {
				if be {
					u = u<<8 | uint32(s[j])
				} else {
					u |= uint32(s[j]) << (8 * j)
				}
			}
			v = int32(u << (32 - 8*bps))
		}
		dst[i] = v
	}
	p.pos += int64(n / ch)
	return n, nil
}

func floatToInt(x float64) int32 {
	return int32(math.Max(math.MinInt32, math.Min(math.MaxInt32, math.Round(x*(1<<31)))))
}

// mp3PCM: go-mp3 always gives 16-bit stereo; LAME's encoder delay and
// padding are dropped so albums play without gaps.
type mp3PCM struct {
	f      *os.File
	d      *mp3.Decoder
	info   musicfile.Info
	pos    int64 // frames handed out (after the delay)
	buf    []byte
	mono   bool
	frames int64
}

func openMP3(path string, i musicfile.Info) (PCM, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	d, err := mp3.NewDecoder(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	p := &mp3PCM{f: f, d: d, info: i, mono: i.Channels == 1, frames: i.Frames}
	if n := d.Length() / 4; n > 0 {
		p.frames = n - int64(i.Delay+i.Padding)
	}
	return p, p.SeekFrame(0)
}

func (p *mp3PCM) Format() Format {
	ch := 2
	if p.mono {
		ch = 1
	}
	return Format{Rate: p.d.SampleRate(), Channels: ch, Bits: 16}
}
func (p *mp3PCM) Frames() int64 { return p.frames }
func (p *mp3PCM) Close() error  { return p.f.Close() }

func (p *mp3PCM) SeekFrame(n int64) error {
	if _, err := p.d.Seek((n+int64(p.info.Delay))*4, io.SeekStart); err != nil {
		return err
	}
	p.pos = n
	return nil
}

func (p *mp3PCM) Read(dst []int32) (int, error) {
	ch := 2
	if p.mono {
		ch = 1
	}
	frames := min(int64(len(dst)/ch), p.frames-p.pos)
	if frames <= 0 {
		return 0, io.EOF
	}
	need := int(frames) * 4
	if cap(p.buf) < need {
		p.buf = make([]byte, need)
	}
	k, err := io.ReadFull(p.d, p.buf[:need])
	k -= k % 4
	if k == 0 {
		if err == nil || err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return 0, err
	}
	n := 0
	for i := 0; i < k; i += 4 {
		l := int32(int16(binary.LittleEndian.Uint16(p.buf[i:]))) << 16
		r := int32(int16(binary.LittleEndian.Uint16(p.buf[i+2:]))) << 16
		dst[n] = l
		n++
		if !p.mono {
			dst[n] = r
			n++
		}
	}
	p.pos += int64(k / 4)
	return n, nil
}

type vorbisPCM struct {
	f    *os.File
	r    *oggvorbis.Reader
	info musicfile.Info
	buf  []float32
}

func openVorbis(path string, i musicfile.Info) (PCM, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r, err := oggvorbis.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if r.Length() > 0 {
		i.Frames = r.Length()
	}
	return &vorbisPCM{f: f, r: r, info: i}, nil
}

func (p *vorbisPCM) Format() Format {
	return Format{Rate: p.r.SampleRate(), Channels: p.r.Channels(), Bits: 24, Float: true}
}
func (p *vorbisPCM) Frames() int64           { return p.info.Frames }
func (p *vorbisPCM) Close() error            { return p.f.Close() }
func (p *vorbisPCM) SeekFrame(n int64) error { return p.r.SetPosition(n) }

func (p *vorbisPCM) Read(dst []int32) (int, error) {
	ch := p.r.Channels()
	dst = dst[:len(dst)/ch*ch]
	if cap(p.buf) < len(dst) {
		p.buf = make([]float32, len(dst))
	}
	n, err := p.r.Read(p.buf[:len(dst)])
	n -= n % ch
	for i := 0; i < n; i++ {
		dst[i] = floatToInt(float64(p.buf[i]))
	}
	if n == 0 && err == nil {
		err = io.EOF
	}
	return n, err
}

// opusPCM decodes Ogg Opus at 48 kHz; seeking decodes from the start (the
// files are voice notes and short tracks).
type opusPCM struct {
	path string
	f    *os.File
	r    *ogg.OpusReader
	dec  *opus.Decoder
	ch   int
	pcm  []int16
	have int
	off  int
	skip int
	pos  int64
	end  int64
}

func openOpus(path string, i musicfile.Info) (PCM, error) {
	p := &opusPCM{path: path, end: i.Frames}
	return p, p.reopen()
}

func (p *opusPCM) reopen() error {
	if p.f != nil {
		p.dec.Close()
		p.f.Close()
	}
	f, err := os.Open(p.path)
	if err != nil {
		return err
	}
	r, err := ogg.NewOpusReader(f)
	if err != nil {
		f.Close()
		return err
	}
	ch := int(r.Head.Channels)
	if ch != 1 && ch != 2 {
		f.Close()
		return errors.New("Opus file: only mono and stereo are supported")
	}
	dec, err := opus.NewDecoder(48000, ch)
	if err != nil {
		f.Close()
		return err
	}
	p.f, p.r, p.dec, p.ch = f, r, dec, ch
	p.pcm = make([]int16, 5760*ch)
	p.have, p.off, p.skip, p.pos = 0, 0, int(r.Head.PreSkip), 0
	return nil
}

func (p *opusPCM) Format() Format { return Format{Rate: 48000, Channels: p.ch, Bits: 16} }
func (p *opusPCM) Frames() int64  { return p.end }

func (p *opusPCM) Close() error {
	p.dec.Close()
	return p.f.Close()
}

func (p *opusPCM) SeekFrame(n int64) error {
	if err := p.reopen(); err != nil {
		return err
	}
	buf := make([]int32, 4800*p.ch)
	for p.pos < n {
		k := min(int64(len(buf)/p.ch), n-p.pos)
		if m, err := p.Read(buf[:k*int64(p.ch)]); m == 0 || err != nil {
			return err
		}
	}
	return nil
}

func (p *opusPCM) Read(dst []int32) (int, error) {
	n := 0
	for n+p.ch <= len(dst) {
		if p.end > 0 && p.pos >= p.end {
			break
		}
		for p.off >= p.have {
			pk, err := p.r.ReadAudioPacket()
			if err != nil {
				if n == 0 {
					return 0, io.EOF
				}
				return n, nil
			}
			k, err := p.dec.Decode(pk.Data, p.pcm, len(p.pcm)/p.ch, false)
			if err != nil {
				return n, err
			}
			p.have, p.off = k, 0
			if p.skip > 0 {
				d := min(p.skip, k)
				p.skip -= d
				p.off = d
			}
		}
		for c := 0; c < p.ch; c++ {
			dst[n] = int32(p.pcm[p.off*p.ch+c]) << 16
			n++
		}
		p.off++
		p.pos++
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}
