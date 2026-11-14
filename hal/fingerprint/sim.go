package fingerprint

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"time"
)

// Sim is the simulated sensor of the developer machine and QEMU. A test
// touches it with a named finger (Touch: the shell's "finger" command,
// through phone-keystored): each name is a different finger, each touch a
// slightly different reading of it, as a real sensor never reads a finger
// the same way twice.
type Sim struct {
	// Steps is how many touches an enrolment takes.
	Steps int

	mu      sync.Mutex
	waiters []chan Sample
	// pending is a touch no Capture was waiting for, kept for simKeep: the
	// matcher may be re-arming just as the finger lands.
	pending   *Sample
	pendingAt time.Time
}

const (
	simFeatures = 64
	simKeep     = time.Second
	// A reading changes about one feature in eight by up to simNoise; a
	// match needs simAgree features within simTolerance of the template.
	simNoise     = 6
	simTolerance = 10
	simAgree     = simFeatures * 7 / 8
)

func NewSim() *Sim { return &Sim{Steps: 5} }

func (s *Sim) Name() string { return "simulated" }

// Touch puts the named finger on the sensor.
func (s *Sim) Touch(finger string) {
	sample := simRead(finger)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.waiters) > 0 {
		w := s.waiters[0]
		s.waiters = s.waiters[1:]
		w <- sample
		return
	}
	s.pending, s.pendingAt = &sample, time.Now()
}

func (s *Sim) Capture(ctx context.Context) (Sample, error) {
	s.mu.Lock()
	if p := s.pending; p != nil {
		s.pending = nil
		if time.Since(s.pendingAt) < simKeep {
			s.mu.Unlock()
			return *p, nil
		}
	}
	w := make(chan Sample, 1)
	s.waiters = append(s.waiters, w)
	s.mu.Unlock()
	select {
	case sample := <-w:
		return sample, nil
	case <-ctx.Done():
		s.mu.Lock()
		for i, x := range s.waiters {
			if x == w {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		select {
		case sample := <-w:
			// Touched as the wait ended: the touch is not lost.
			s.mu.Lock()
			s.pending, s.pendingAt = &sample, time.Now()
			s.mu.Unlock()
		default:
		}
		return Sample{}, ctx.Err()
	}
}

// simRead is one reading of a finger: its features (a hash of its name)
// with noise.
func simRead(finger string) Sample {
	f := make([]byte, 0, simFeatures)
	for i := byte(0); len(f) < simFeatures; i++ {
		h := sha256.Sum256(append([]byte{i}, finger...))
		f = append(f, h[:]...)
	}
	f = f[:simFeatures]
	noise := make([]byte, 2*simFeatures)
	_, _ = rand.Read(noise)
	for i := range f {
		if noise[i]%8 != 0 {
			continue
		}
		d := int(noise[simFeatures+i]%(2*simNoise+1)) - simNoise
		f[i] = byte(min(255, max(0, int(f[i])+d)))
	}
	return Sample{Features: f}
}

func agree(a, b []byte) int {
	if len(a) != len(b) {
		return 0
	}
	n := 0
	for i := range a {
		if d := int(a[i]) - int(b[i]); d >= -simTolerance && d <= simTolerance {
			n++
		}
	}
	return n
}

func (s *Sim) Match(t Template, sample Sample) bool {
	return agree(t, sample.Features) >= simAgree
}

func (s *Sim) Enroll() Enrollment {
	return &simEnrollment{steps: max(1, s.Steps)}
}

type simEnrollment struct {
	steps   int
	samples [][]byte
}

func (e *simEnrollment) Steps() int { return e.steps }

func (e *simEnrollment) Add(s Sample) Progress {
	p := Progress{Done: len(e.samples), Steps: e.steps}
	if p.Done >= e.steps {
		return p
	}
	if p.Done > 0 && agree(e.samples[0], s.Features) < simAgree {
		p.Rejected = RejectOtherFinger
		return p
	}
	e.samples = append(e.samples, s.Features)
	p.Done++
	return p
}

// Template is the mean of the readings, closer to the finger than any one.
func (e *simEnrollment) Template() Template {
	if len(e.samples) < e.steps {
		return nil
	}
	t := make(Template, simFeatures)
	for i := range t {
		sum := 0
		for _, s := range e.samples {
			sum += int(s[i])
		}
		t[i] = byte((sum + len(e.samples)/2) / len(e.samples))
	}
	return t
}
