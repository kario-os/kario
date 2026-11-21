// Package hifi plays music files for phone-audiod: lossless and DSD at
// their own rate and depth, a queue that runs from one track into the
// next without a gap (a cue sheet's tracks continue on the same decoder),
// ReplayGain, and the choice of how a track reaches the sound card
// (Negotiate): at its own format, bit-perfect when nothing changes the
// samples; as DoP or native DSD; or through the 48 kHz mixer when other
// sounds play with it.
package hifi

import (
	"errors"
	"io"
	"math"
	"sync"

	"github.com/kario-os/kario/audio/dsd"
)

// MixRate is the mixer's rate (audio.Rate; kept here to avoid an import
// cycle).
const MixRate = 48000

// Options are the player's settings.
type Options struct {
	DSD string // DSDAuto, DSDDoP or DSDPCM
}

// Stream is a queue of tracks: the one playing and, once queued, the next.
// It is an audio.Source for the mixer (FillStereo at 48 kHz) and goes to
// the sound card directly through Out. Safe for concurrent use.
type Stream struct {
	mu      sync.Mutex
	cur     *track
	next    *track
	pending chan *track
	opts    Options
	gain    float64
	changed bool
	err     error

	rs      *resampler
	rsRate  int
	mixDec  *dsd.Decimator
	mixKey  [2]int
	outDec  *dsd.Decimator
	outKey  [3]int
	drained bool
	dop     dsd.DoP

	ibuf []int32
	bbuf []byte
	fbuf []float32
	l, r []float32
	ob   []int32
}

// Open starts a queue with its first track, at ms into it.
func Open(first Spec, opts Options, ms int64) (*Stream, error) {
	t, err := openTrack(first)
	if err != nil {
		return nil, err
	}
	s := &Stream{cur: t, opts: opts, gain: first.gain()}
	if ms > 0 {
		if err := s.SeekMS(ms); err != nil {
			t.close()
			return nil, err
		}
	}
	return s, nil
}

// SetOptions changes the settings (the next negotiation uses them).
func (s *Stream) SetOptions(o Options) {
	s.mu.Lock()
	s.opts = o
	s.mu.Unlock()
}

func (s *Stream) Options() Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts
}

// Queue sets the track to play after the current one (nil: none). It is
// opened in the background, its decoder started early; the next track of
// the same cue sheet image waits for the current one's decoder instead.
func (s *Stream) Queue(spec *Spec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropNext()
	if spec == nil {
		return
	}
	ch := make(chan *track, 1)
	s.pending = ch
	cur := s.cur
	go func(spec Spec) {
		t, err := s.openNext(spec, cur)
		if err != nil {
			t = nil
		}
		ch <- t
	}(*spec)
}

func (s *Stream) openNext(spec Spec, cur *track) (*track, error) {
	if lazy, ok := lazyNext(spec, cur); ok {
		return lazy, nil
	}
	return openTrack(spec)
}

func (s *Stream) dropNext() {
	if s.next != nil {
		s.next.close()
		s.next = nil
	}
	if s.pending != nil {
		ch := s.pending
		s.pending = nil
		go func() {
			if t := <-ch; t != nil {
				t.close()
			}
		}()
	}
}

// advance moves on to the next track when there is one (waiting for it
// if it is still being opened).
func (s *Stream) advance() bool {
	if s.next == nil && s.pending != nil {
		s.next = <-s.pending
		s.pending = nil
	}
	if s.next == nil {
		return false
	}
	old := s.cur
	if old.continues(s.next) && old.at == s.next.segs[0].Start/old.unit() {
		old.handOver(s.next)
	}
	old.close()
	s.cur, s.next = s.next, nil
	s.gain = s.cur.spec.gain()
	s.changed = true
	return true
}

// Now is what plays: its media name, title and format label.
func (s *Stream) Now() (media, title, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.spec.Media, s.cur.spec.Title, s.cur.spec.Label
}

// Changed reports (once) that the queue moved on to its next track.
func (s *Stream) Changed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.changed
	s.changed = false
	return c
}

// Native is the current track's format as decoded (DST: the PCM ffmpeg
// makes of it).
func (s *Stream) Native() Format {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.format
}

// Gain is the current track's ReplayGain factor.
func (s *Stream) Gain() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gain
}

// SeekMS moves within the current track.
func (s *Stream) SeekMS(ms int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.cur
	n := ms * int64(t.format.Rate) / 1000 / t.unit()
	if t.frames > 0 {
		n = min(n, t.frames)
	}
	if err := t.seek(max(0, n)); err != nil {
		return err
	}
	s.resetFilters()
	return nil
}

func (s *Stream) resetFilters() {
	s.rs, s.drained = nil, false
	if s.mixDec != nil {
		s.mixDec.Reset()
	}
	if s.outDec != nil {
		s.outDec.Reset()
	}
}

func (s *Stream) at48(frames int64) int {
	t := s.cur
	if t.format.Rate == 0 {
		return 0
	}
	return int(frames * t.unit() * MixRate / int64(t.format.Rate))
}

// Pos and Len are the current track's position and length in 48 kHz
// samples (audio.Source).
func (s *Stream) Pos() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at48(s.cur.pos)
}

func (s *Stream) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at48(s.cur.frames)
}

// Err is what stopped the stream early, if anything did.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropNext()
	s.cur.close()
	return nil
}

// Fill is the mono mixer's (audio.Source).
func (s *Stream) Fill(buf []float64) bool {
	r := make([]float64, len(buf))
	more := s.FillStereo(buf, r)
	for i := range buf {
		buf[i] = (buf[i] + r[i]) / 2
	}
	return more
}

// FillStereo plays at 48 kHz into the mixer, with the track's ReplayGain.
func (s *Stream) FillStereo(left, right []float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for n < len(left) {
		if s.rs != nil {
			n += s.rs.pull(left[n:], right[n:])
			if n == len(left) {
				break
			}
		}
		if s.drained {
			clear(left[n:])
			clear(right[n:])
			return false
		}
		k, rate, err := s.readStereo(1024)
		if err != nil {
			if s.advance() {
				continue
			}
			if err != io.EOF {
				s.err = err
			}
			// Let the filter's tail out, then end.
			if s.rs == nil {
				s.drained = true
				continue
			}
			z := make([]float32, s.rs.half*2)
			s.rs.push(z, z)
			s.drained = true
			continue
		}
		if s.rs == nil || s.rsRate != rate {
			s.rs, s.rsRate = newResampler(rate, MixRate), rate
		}
		s.rs.push(s.l[:k], s.r[:k])
	}
	return true
}

// readStereo reads up to max frames of the current track as stereo float
// (DSD decimated to 88.2 kHz first) with the ReplayGain applied.
func (s *Stream) readStereo(max int) (int, int, error) {
	t := s.cur
	ch := t.format.Channels
	if cap(s.l) < max {
		s.l, s.r = make([]float32, max), make([]float32, max)
	}
	var f []float32
	rate := t.format.Rate
	if t.format.DSD {
		const pcm = 88200
		if key := [2]int{rate, ch}; s.mixDec == nil || s.mixKey != key {
			d, err := dsd.NewDecimator(rate, ch, pcm)
			if err != nil {
				return 0, 0, err
			}
			s.mixDec, s.mixKey = d, key
		}
		need := max * s.mixDec.Ratio() * ch
		if cap(s.bbuf) < need {
			s.bbuf = make([]byte, need)
		}
		k, err := t.readDSD(s.bbuf[:need])
		if err != nil {
			return 0, 0, err
		}
		if cap(s.fbuf) < (k/ch/s.mixDec.Ratio()+1)*ch {
			s.fbuf = make([]float32, (k/ch/s.mixDec.Ratio()+1)*ch)
		}
		m := s.mixDec.Process(s.bbuf[:k], s.fbuf)
		f, rate = s.fbuf[:m*ch], pcm
	} else {
		if cap(s.ibuf) < max*ch {
			s.ibuf = make([]int32, max*ch)
		}
		k, err := t.readPCM(s.ibuf[:max*ch])
		if err != nil {
			return 0, 0, err
		}
		if cap(s.fbuf) < k {
			s.fbuf = make([]float32, k)
		}
		f = s.fbuf[:k]
		for i, v := range s.ibuf[:k] {
			f[i] = float32(v) / (1 << 31)
		}
	}
	n := downmix(f, ch, s.l, s.r)
	if g := float32(s.gain); g != 1 {
		for i := 0; i < n; i++ {
			s.l[i] *= g
			s.r[i] *= g
		}
	}
	return n, rate, nil
}

// downmix makes stereo of interleaved samples: mono to both sides,
// 5.0 and 5.1 (SACD's L R C [LFE] LS RS) by the usual -3 dB for the centre
// and surrounds, the LFE left out, scaled so nothing clips.
func downmix(f []float32, ch int, l, r []float32) int {
	n := len(f) / ch
	switch ch {
	case 1:
		copy(l, f[:n])
		copy(r, f[:n])
	case 2:
		for i := 0; i < n; i++ {
			l[i], r[i] = f[2*i], f[2*i+1]
		}
	case 5, 6:
		const k, norm = 0.7071, 1 / (1 + 2*0.7071)
		ls, rs := 3, 4
		if ch == 6 {
			ls, rs = 4, 5
		}
		for i := 0; i < n; i++ {
			s := f[i*ch:]
			l[i] = norm * (s[0] + k*s[2] + k*s[ls])
			r[i] = norm * (s[1] + k*s[2] + k*s[rs])
		}
	default:
		for i := 0; i < n; i++ {
			var a, b float32
			for c := 0; c < ch; c++ {
				if c%2 == 0 {
					a += f[i*ch+c]
				} else {
					b += f[i*ch+c]
				}
			}
			l[i], r[i] = a/float32((ch+1)/2), b/float32(ch/2)
		}
	}
	return n
}

// ErrFormatChanged: Out was asked for a plan made for another format (the
// queue moved on to a track that needs the card set up anew).
var ErrFormatChanged = errors.New("the track's format changed")

// Out reads up to frames ALSA frames for the plan, packed into out, with
// mixGain (the media volume) on top of the track's ReplayGain. more is
// false once the queue is done. A track of another format waits for the
// next call, which then returns ErrFormatChanged.
func (s *Stream) Out(p Plan, mixGain float64, frames int, out []byte) (n int, more bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur.format != p.Source {
		return 0, true, ErrFormatChanged
	}
	g := mixGain * s.gain
	bpf := p.BytesPerFrame()
	for n+bpf <= len(out) && n/bpf < frames {
		want := min(frames-n/bpf, (len(out)-n)/bpf)
		k, err := s.outChunk(p, g, want, out[n:])
		n += k
		if err == nil && k > 0 {
			continue
		}
		if err != nil && err != io.EOF {
			s.err = err
			return n, false, nil
		}
		if !s.advance() {
			return n, false, nil
		}
		if s.cur.format != p.Source {
			s.resetFilters()
			return n, true, nil
		}
	}
	return n, true, nil
}

// outChunk reads one piece of the current track for the plan.
func (s *Stream) outChunk(p Plan, g float64, frames int, out []byte) (int, error) {
	t := s.cur
	ch := t.format.Channels
	switch {
	case p.Mode == ModeDSD:
		w := dsd.NativeWidth(p.Format)
		need := frames * w * ch
		if cap(s.bbuf) < need {
			s.bbuf = make([]byte, need)
		}
		k, err := t.readDSD(s.bbuf[:need])
		k -= k % (w * ch)
		if err != nil {
			return 0, err
		}
		return dsd.PackNative(p.Format, s.bbuf[:k], ch, out), nil
	case p.Mode == ModeDoP:
		need := frames * 2 * ch
		if cap(s.bbuf) < need {
			s.bbuf = make([]byte, need)
		}
		k, err := t.readDSD(s.bbuf[:need])
		k -= k % (2 * ch)
		if err != nil {
			return 0, err
		}
		s.ob = grow(s.ob, k/2)
		m := s.dop.Pack(s.bbuf[:k], ch, s.ob)
		return pack(p.Format, s.ob[:m], out), nil
	case t.format.DSD:
		if key := [3]int{t.format.Rate, ch, p.PCMRate}; s.outDec == nil || s.outKey != key {
			d, err := dsd.NewDecimator(t.format.Rate, ch, p.PCMRate)
			if err != nil {
				return 0, err
			}
			s.outDec, s.outKey = d, key
		}
		need := frames * s.outDec.Ratio() * ch
		if cap(s.bbuf) < need {
			s.bbuf = make([]byte, need)
		}
		k, err := t.readDSD(s.bbuf[:need])
		if err != nil {
			return 0, err
		}
		s.fbuf = growf(s.fbuf, (k/ch/s.outDec.Ratio()+1)*ch)
		m := s.outDec.Process(s.bbuf[:k], s.fbuf)
		return s.packFloat(p, s.fbuf[:m*ch], ch, g, out), nil
	}
	s.ibuf = grow(s.ibuf, frames*ch)
	k, err := t.readPCM(s.ibuf[:frames*ch])
	if err != nil {
		return 0, err
	}
	in := s.ibuf[:k]
	if ch > 2 && p.Channels == 2 {
		s.fbuf = growf(s.fbuf, k)
		for i, v := range in {
			s.fbuf[i] = float32(v) / (1 << 31)
		}
		return s.packFloat(p, s.fbuf[:k], ch, g, out), nil
	}
	if ch == 1 && p.Channels == 2 {
		s.ob = grow(s.ob, 2*k)
		for i, v := range in {
			s.ob[2*i], s.ob[2*i+1] = v, v
		}
		in = s.ob[:2*k]
	}
	if math.Abs(g-1) > 1e-9 {
		scale(in, g, containerBits(p.Format))
	}
	return pack(p.Format, in, out), nil
}

// packFloat downmixes float samples to the plan's channels, applies the
// gain and packs them.
func (s *Stream) packFloat(p Plan, f []float32, ch int, g float64, out []byte) int {
	n := len(f) / ch
	if cap(s.l) < n {
		s.l, s.r = make([]float32, n), make([]float32, n)
	}
	downmix(f, ch, s.l, s.r)
	s.ob = grow(s.ob, n*p.Channels)
	for i := 0; i < n; i++ {
		if p.Channels == 1 {
			s.ob[i] = floatToInt(float64(s.l[i]+s.r[i]) / 2 * g)
			continue
		}
		s.ob[2*i] = floatToInt(float64(s.l[i]) * g)
		s.ob[2*i+1] = floatToInt(float64(s.r[i]) * g)
	}
	if containerBits(p.Format) < 32 {
		scale(s.ob[:n*p.Channels], 1, containerBits(p.Format))
	}
	return pack(p.Format, s.ob[:n*p.Channels], out)
}

func grow(b []int32, n int) []int32 {
	if cap(b) < n {
		return make([]int32, n)
	}
	return b[:n]
}

func growf(b []float32, n int) []float32 {
	if cap(b) < n {
		return make([]float32, n)
	}
	return b[:n]
}

// lazyNext: the next track continues the current one in the same file (a
// cue sheet image): it gets the current decoder when the current ends,
// so nothing is opened now.
func lazyNext(spec Spec, cur *track) (*track, bool) {
	if cur == nil || cur.segs == nil {
		return nil, false
	}
	t, err := describeTrack(spec)
	if err != nil || t.segs == nil {
		return nil, false
	}
	t.format = cur.format
	if !cur.continues(t) {
		return nil, false
	}
	t.frames /= t.unit()
	return t, true
}
