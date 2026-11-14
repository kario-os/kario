package hifi

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/musicfile"
)

// Spec names a track (the shell's request): a file, a cue sheet's track
// or an SACD image's track.
type Spec struct {
	Path  string `json:"path"`
	Track int    `json:"track,omitempty"` // 0: the whole file; a cue sheet's or SACD image's track, from 1
	Media string `json:"media,omitempty"` // the app's name for it, reported back
	Title string `json:"title,omitempty"`
	Label string `json:"label,omitempty"` // "FLAC 24/96", shown while it plays
	// ReplayGain chosen by the shell (track or album), with its peak.
	GainDB float64 `json:"gain_db,omitempty"`
	Peak   float64 `json:"peak,omitempty"`
}

// gain is the ReplayGain as a factor, held down so the peak cannot clip.
func (s Spec) gain() float64 {
	if s.GainDB == 0 {
		return 1
	}
	g := math.Pow(10, s.GainDB/20)
	if s.Peak > 0 && g*s.Peak > 1 {
		g = 1 / s.Peak
	}
	return g
}

// track plays one Spec: its segments one after the other. Positions are
// frames of format (DSD: byte frames, 8 samples a channel).
type track struct {
	spec   Spec
	format Format
	segs   []musicfile.Segment
	frames int64
	info   musicfile.Info
	mt     musicfile.Track
	sacd   *dsd.SACD

	seg  int
	pcm  PCM
	dsd  dsd.Stream
	path string // the file the open decoder reads
	at   int64  // the decoder's position in that file
	end  int64  // where the segment ends in it, -1 at the file's end
	pos  int64
}

// unit is how many of a file's frames (musicfile's, DSD: samples) make
// one of the track's.
func (t *track) unit() int64 {
	if t.format.DSD {
		return 8
	}
	return 1
}

// describeTrack finds the track spec names, opening nothing.
func describeTrack(spec Spec) (*track, error) {
	ts, err := musicfile.Tracks(spec.Path)
	if err != nil {
		return nil, err
	}
	for k := range ts {
		if ts[k].Index == spec.Track || spec.Track == 0 && k == 0 {
			mt := ts[k]
			t := &track{spec: spec, segs: mt.Segments, info: mt.Info, frames: mt.Frames, mt: mt}
			if mt.Info.Container == "SACD" || mt.Info.Codec == musicfile.DST {
				t.segs = nil
			}
			return t, nil
		}
	}
	return nil, fmt.Errorf("%s has no track %d", spec.Path, spec.Track)
}

func openTrack(spec Spec) (*track, error) {
	t, err := describeTrack(spec)
	if err != nil {
		return nil, err
	}
	mt := t.mt
	if mt.Info.Container == "SACD" {
		return t, t.openSACD(&mt)
	}
	i := mt.Info
	if i.DSD() {
		t.format = Format{Rate: i.Rate, Channels: i.Channels, Bits: 1, DSD: true}
		if i.Codec == musicfile.DST {
			x, err := dsd.OpenDFFDST(spec.Path)
			if err != nil {
				return nil, err
			}
			return t, t.useFrames(x)
		}
	}
	if err := t.openSegment(0, 0); err != nil {
		return nil, err
	}
	if !t.format.DSD {
		t.format = t.pcm.Format()
	}
	t.frames = mt.Frames / t.unit()
	if whole := len(t.segs) == 1 && t.segs[0].Start == 0 && t.segs[0].End < 0; whole || t.frames == 0 {
		switch {
		case t.pcm != nil && t.pcm.Frames() > 0:
			t.frames = t.pcm.Frames()
		case t.dsd != nil && t.dsd.Len() > 0:
			t.frames = t.dsd.Len()
		}
	}
	return t, nil
}

func (t *track) openSACD(mt *musicfile.Track) error {
	s, err := dsd.OpenSACD(t.spec.Path)
	if err != nil {
		return err
	}
	t.sacd = s
	a := s.Stereo()
	r, err := s.OpenTrack(a, mt.Index)
	if err != nil {
		s.Close()
		return err
	}
	t.format = Format{Rate: a.Rate, Channels: a.Channels, Bits: 1, DSD: true}
	t.segs, t.end = nil, -1
	switch x := r.(type) {
	case *dsd.SACDDSD:
		t.dsd, t.frames = x, x.Len()
	case *dsd.SACDDST:
		return t.useFrames(x)
	}
	return nil
}

// useFrames plays DST frames: as plain DSD when the encoder left them
// uncoded (DST's escape, which ffmpeg mishandles), through ffmpeg
// otherwise.
func (t *track) useFrames(x dsd.Frames) error {
	t.segs, t.end = nil, -1
	if f, err := x.Next(); err == nil && len(f) > 0 && f[0]&0x80 == 0 {
		x.SeekFrame(0)
		s := &uncodedDST{x: x}
		t.dsd, t.frames = s, s.Len()
		return nil
	}
	x.SeekFrame(0)
	p, err := openDST(x)
	if err != nil {
		x.Close()
		return err
	}
	t.pcm, t.format, t.frames = p, p.Format(), p.Frames()
	return nil
}

// openSegment opens segment k (reusing the decoder when it is already on
// that file) and moves to off frames into it.
func (t *track) openSegment(k int, off int64) error {
	sg := t.segs[k]
	start := sg.Start/t.unit() + off
	if t.path != sg.Path {
		t.closeDecoder()
		i, err := musicfile.Probe(sg.Path)
		if err != nil {
			return err
		}
		if i.DSD() {
			var s dsd.Stream
			switch i.Container {
			case "DSF":
				s, err = dsd.OpenDSF(sg.Path)
			default:
				s, err = dsd.OpenDFF(sg.Path)
			}
			if err != nil {
				return err
			}
			t.dsd = s
		} else {
			p, err := openPCM(sg.Path, i)
			if err != nil {
				return err
			}
			t.pcm = p
		}
		t.path, t.at = sg.Path, 0
	}
	if t.at != start {
		var err error
		if t.dsd != nil {
			err = t.dsd.SeekFrame(start)
		} else {
			err = t.pcm.SeekFrame(start)
		}
		if err != nil {
			return err
		}
		t.at = start
	}
	t.seg, t.end = k, -1
	if sg.End >= 0 {
		t.end = sg.End / t.unit()
	}
	return nil
}

func (t *track) closeDecoder() {
	if t.pcm != nil {
		t.pcm.Close()
		t.pcm = nil
	}
	if t.dsd != nil {
		t.dsd.Close()
		t.dsd = nil
	}
	t.path = ""
}

func (t *track) close() {
	t.closeDecoder()
	if t.sacd != nil {
		t.sacd.Close()
		t.sacd = nil
	}
}

// room is how many frames the current segment still has, -1 to the end
// of its file.
func (t *track) room() int64 {
	if t.end < 0 {
		return -1
	}
	return t.end - t.at
}

// next moves to the following segment; false after the last.
func (t *track) next() (bool, error) {
	if t.segs == nil || t.seg+1 >= len(t.segs) {
		return false, nil
	}
	return true, t.openSegment(t.seg+1, 0)
}

// ready opens the decoder of a track that was queued without one (it was
// to take over the previous track's, which did not end where it starts).
func (t *track) ready() error {
	if t.pcm != nil || t.dsd != nil || t.segs == nil {
		return nil
	}
	return t.seek(t.pos)
}

// readPCM reads interleaved PCM (PCM tracks).
func (t *track) readPCM(dst []int32) (int, error) {
	if err := t.ready(); err != nil {
		return 0, err
	}
	ch := t.format.Channels
	n := 0
	for n+ch <= len(dst) {
		want := (len(dst) - n) / ch
		if r := t.room(); r >= 0 {
			want = int(min(int64(want), r))
		}
		if t.frames > 0 {
			want = int(min(int64(want), t.frames-t.pos))
		}
		k := 0
		var err error
		if want > 0 {
			k, err = t.pcm.Read(dst[n : n+want*ch])
		}
		k -= k % ch
		n += k
		t.at += int64(k / ch)
		t.pos += int64(k / ch)
		if k > 0 && err == nil {
			continue
		}
		if err != nil && err != io.EOF {
			return n, err
		}
		if t.frames > 0 && t.pos >= t.frames {
			break
		}
		more, err := t.next()
		if err != nil {
			return n, err
		}
		if !more {
			break
		}
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// readDSD reads byte frames (DSD tracks).
func (t *track) readDSD(dst []byte) (int, error) {
	if err := t.ready(); err != nil {
		return 0, err
	}
	ch := t.format.Channels
	n := 0
	for n+ch <= len(dst) {
		want := (len(dst) - n) / ch
		if r := t.room(); r >= 0 {
			want = int(min(int64(want), r))
		}
		if t.frames > 0 {
			want = int(min(int64(want), t.frames-t.pos))
		}
		k := 0
		var err error
		if want > 0 {
			k, err = t.dsd.Read(dst[n : n+want*ch])
		}
		k -= k % ch
		n += k
		t.at += int64(k / ch)
		t.pos += int64(k / ch)
		if k > 0 && err == nil {
			continue
		}
		if err != nil && err != io.EOF {
			return n, err
		}
		if t.frames > 0 && t.pos >= t.frames {
			break
		}
		more, err := t.next()
		if err != nil {
			return n, err
		}
		if !more {
			break
		}
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// seek moves to frame n of the track.
func (t *track) seek(n int64) error {
	if n < 0 || t.frames > 0 && n > t.frames {
		return fmt.Errorf("seek to %d of %d", n, t.frames)
	}
	if t.segs == nil {
		var err error
		if t.dsd != nil {
			err = t.dsd.SeekFrame(n)
		} else {
			err = t.pcm.SeekFrame(n)
		}
		if err == nil {
			t.pos = n
		}
		return err
	}
	rest := n
	for k, sg := range t.segs {
		length := (sg.End - sg.Start) / t.unit()
		if sg.End < 0 || rest < length || k == len(t.segs)-1 {
			if err := t.openSegment(k, rest); err != nil {
				return err
			}
			t.pos = n
			return nil
		}
		rest -= length
	}
	return errors.New("seek past the track")
}

// continues says whether u starts where t ends, in the same file (the
// next track of a cue sheet): t's decoder can go on into u unchanged.
func (t *track) continues(u *track) bool {
	if t.segs == nil || u.segs == nil {
		return false
	}
	last, first := t.segs[len(t.segs)-1], u.segs[0]
	return last.Path == first.Path && last.End >= 0 && last.End == first.Start
}

// handOver gives t's decoder (standing at the end of t) to u.
func (t *track) handOver(u *track) {
	u.closeDecoder()
	u.pcm, u.dsd, u.path, u.at = t.pcm, t.dsd, t.path, t.at
	t.pcm, t.dsd, t.path = nil, nil, ""
	u.seg, u.end, u.pos = 0, -1, 0
	if u.segs[0].End >= 0 {
		u.end = u.segs[0].End / u.unit()
	}
}

// uncodedDST plays DST frames that all carry plain DSD (the first bit of
// a frame says "not DST coded"); a coded frame among them plays as
// silence.
type uncodedDST struct {
	x    dsd.Frames
	have []byte
	pos  int64
}

func (u *uncodedDST) Rate() int     { return u.x.Rate() }
func (u *uncodedDST) Channels() int { return u.x.Channels() }
func (u *uncodedDST) per() int64    { return int64(u.x.Rate() / 8 / dsd.FramesPerSecond) }
func (u *uncodedDST) Len() int64    { return u.x.Count() * u.per() }
func (u *uncodedDST) Close() error  { return u.x.Close() }

func (u *uncodedDST) SeekFrame(n int64) error {
	f := n / u.per()
	if err := u.x.SeekFrame(f); err != nil {
		return err
	}
	u.have, u.pos = nil, f*u.per()
	if skip := n - u.pos; skip > 0 {
		b := make([]byte, skip*int64(u.Channels()))
		if _, err := u.Read(b); err != nil {
			return err
		}
	}
	return nil
}

func (u *uncodedDST) Read(p []byte) (int, error) {
	ch := u.Channels()
	size := int(u.per()) * ch
	n := 0
	for n+ch <= len(p) {
		if len(u.have) == 0 {
			f, err := u.x.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return n, err
			}
			if len(f) != 1+size || f[0]&0x80 != 0 {
				f = make([]byte, 1+size)
				for i := range f {
					f[i] = dsd.Silence
				}
			}
			u.have = f[1:]
		}
		k := min(len(u.have), (len(p)-n)/ch*ch)
		copy(p[n:], u.have[:k])
		u.have = u.have[k:]
		n += k
		u.pos += int64(k / ch)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}
