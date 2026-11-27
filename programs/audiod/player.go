package audiod

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
	"github.com/kario-os/kario/audio/ucm"
	"github.com/kario-os/kario/ui"
)

// player is audiod's output: at every tick (50 a second) the microphone
// feeds the recordings and the next 20 ms of sound go to the sink, past
// the mixer for music that plays alone. A sink that fails does not hold
// the clock up: those 20 ms are lost, and the sink tries itself again
// (alsaSink: after retryAfter). The time a failing write took (a stalled
// card, up to stallAfter) is made up at once, without sound.
type player struct {
	m *audio.Mixer
	// out is where this tick plays (the policy's first target), also the
	// others (a ringtone on the speaker and the headphones), base the
	// built-in card's sink.
	out, base sink
	also      []sink
	alsoErr   map[string]string
	outs      *outputs
	router    *ucm.Router
	rec       *recordings
	now       func() time.Time
	// sink: the mixer's writes; direct: a track's own set-up.
	sink, direct health

	buf, micBuf []int16
	raw         []byte
	mic         audio.Mic
}

func newPlayer(m *audio.Mixer, out sink, router *ucm.Router, rec *recordings) *player {
	return &player{m: m, out: out, base: out, router: router, rec: rec, now: time.Now, alsoErr: map[string]string{},
		buf: make([]int16, audio.Channels*audio.Frame), micBuf: make([]int16, audio.Frame)}
}

func (p *player) run(ticks <-chan time.Time) {
	for range ticks {
		p.step()
	}
}

func (p *player) step() {
	if p.outs != nil {
		sinks := p.outs.targets(p.m.Top(), p.router != nil && p.router.InCall())
		p.out, p.also = sinks[0], sinks[1:]
	}
	if p.router != nil {
		routeSink(p.base, p.router.PCM())
	}
	p.rec.feed(&p.mic, p.micBuf)
	if p.solo() {
		return
	}
	if p.m.SetOutput(audio.MixedOutput) {
		touchMedia(p.m)
	}
	p.m.Mix(p.buf)
	t0 := p.now()
	err := p.out.Write(p.buf)
	for _, s := range p.also {
		p.alsoWrote(s, s.Write(p.buf))
	}
	if err != nil {
		p.catchUp(p.now().Sub(t0))
	}
	p.wrote(err)
}

// alsoWrote tells a second output's failure once, and its end.
func (p *player) alsoWrote(s sink, err error) {
	why := ""
	if err != nil {
		why = err.Error()
	}
	if p.alsoErr[s.Name()] == why {
		return
	}
	p.alsoErr[s.Name()] = why
	if err != nil {
		log.Printf("phone-audiod: sink %s (with %s): %v", s.Name(), p.out.Name(), err)
	}
}

const (
	tick       = time.Second / 50
	maxCatchUp = 5 * time.Second
)

// catchUp moves the mixer and the recordings on by the time a failed
// write held the loop.
func (p *player) catchUp(held time.Duration) {
	for range min(held, maxCatchUp) / tick {
		p.rec.feed(&p.mic, p.micBuf)
		p.m.Mix(p.buf)
	}
}

func (p *player) wrote(err error) {
	if !p.sink.wrote(err, p.now()) {
		return
	}
	if err != nil {
		log.Printf("phone-audiod: sink %s: %v: no sound until it plays again", p.out.Name(), err)
	} else {
		log.Printf("phone-audiod: sink %s plays", p.out.Name())
	}
}

// solo plays 20 ms of the music that plays alone straight to the card,
// at the set-up its format gets there; false when the mixer has to play.
func (p *player) solo() bool {
	d, ok := p.out.(direct)
	if !ok || len(p.also) > 0 {
		return false
	}
	id, src, gain, ok := p.m.Solo()
	if !ok {
		return false
	}
	s, ok := src.(*hifi.Stream)
	if !ok {
		return false
	}
	for try := 0; try < 2; try++ {
		f := s.Native()
		plan := hifi.Negotiate(f, d.Caps(), gain*s.Gain(), s.Options().DSD)
		if plan.Mode == hifi.ModeMixer {
			return false
		}
		if err := d.Open(plan); err != nil {
			p.refused(plan, err)
			return false
		}
		frames := plan.Rate / 50
		if need := frames * plan.BytesPerFrame(); cap(p.raw) < need {
			p.raw = make([]byte, need)
		}
		b := p.raw[:frames*plan.BytesPerFrame()]
		n, more, err := s.Out(plan, gain, frames, b)
		if errors.Is(err, hifi.ErrFormatChanged) {
			continue
		}
		t0 := p.now()
		if err := d.WriteRaw(plan, b[:n]); err != nil {
			// What Out took is lost; the track's clock moves on as the
			// mixer's would.
			p.refused(plan, err)
			p.m.Played(id, more)
			p.catchUp(p.now().Sub(t0))
			return true
		}
		p.direct.wrote(nil, p.now())
		p.wrote(nil)
		changed := p.m.SetOutput(plan.String())
		if err := s.Err(); !more && err != nil {
			log.Printf("phone-audiod: music stopped: %v", err)
		}
		p.m.Played(id, more)
		if changed {
			log.Printf("phone-audiod: music %s → %s", f, plan)
			touchMedia(p.m)
		}
		return true
	}
	return false
}

func (p *player) refused(plan hifi.Plan, err error) {
	if p.direct.wrote(err, p.now()) {
		log.Printf("phone-audiod: %s not taken (%v): music goes through the mixer", plan, err)
	}
}

// health follows whether writes go through: a failure is told once, when
// it starts or its reason changes, and the end of it once writes have gone
// through for a second (a new aplay takes a write or two before it finds
// it cannot open the card).
type health struct {
	mu    sync.Mutex
	err   string
	since time.Time
}

// wrote notes a write's outcome: true when that starts, changes or ends a
// failure.
func (h *health) wrote(err error, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case err != nil:
		h.since = time.Time{}
		if err.Error() == h.err {
			return false
		}
		h.err = err.Error()
		return true
	case h.err == "":
		return false
	case h.since.IsZero():
		h.since = now
		return false
	case now.Sub(h.since) < time.Second:
		return false
	}
	h.err, h.since = "", time.Time{}
	return true
}

// failure is why writes fail, "" while they go through.
func (h *health) failure() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// recordings are the open captures: the microphone feeds each one's
// ring, nothing is mixed.
type recordings struct {
	mu   sync.Mutex
	open map[string]*capture
	seq  int
}

type capture struct {
	s    audio.Stream
	ring *audio.RingBuf
}

func (r *recordings) start(app, name string, ring *audio.RingBuf) audio.Stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open == nil {
		r.open = map[string]*capture{}
	}
	r.seq++
	c := &capture{s: audio.Stream{ID: fmt.Sprintf("r%d", r.seq), App: app, Name: name, Category: "record", State: ui.PlayPlaying}, ring: ring}
	r.open[c.s.ID] = c
	return c.s
}

// stop ends a recording; its ring tells the app.
func (r *recordings) stop(id string) (audio.Stream, bool) {
	r.mu.Lock()
	c := r.open[id]
	delete(r.open, id)
	r.mu.Unlock()
	if c == nil {
		return audio.Stream{}, false
	}
	c.ring.Finish()
	c.ring.Close()
	c.s.State = ui.PlayEnded
	return c.s, true
}

func (r *recordings) streams() []audio.Stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]audio.Stream, 0, len(r.open))
	for _, c := range r.open {
		out = append(out, c.s)
	}
	return out
}

func (r *recordings) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.open)
}

// feed gives each recording the microphone's next 20 ms.
func (r *recordings) feed(mic *audio.Mic, buf []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.open) == 0 {
		return
	}
	mic.Next(buf)
	for _, c := range r.open {
		c.ring.Write(buf)
		c.s.PosMS += 1000 / 50
	}
}
