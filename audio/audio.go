// Package audio is the core of phone-audiod: sources (synthesised system
// sounds, demo tracks, WAV files), a stereo mixer at 48 kHz, and the
// focus policy that decides what may sound at the same time. Music that
// plays alone can skip the mixer and reach the sound card at its own
// format (Solo; audio/hifi).
package audio

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/kario-os/kario/ui"
)

const (
	Rate     = 48000
	Frame    = Rate / 50 // 20 ms per mix tick
	Channels = 2         // the mixer's output: interleaved stereo
)

// Categories, highest priority first. A higher category takes focus from
// lower ones: media pauses during a call, the ringtone or an alarm and
// resumes afterwards; a notification only ducks media. An emergency alert
// (a cell broadcast) sounds over everything, at full volume, silent mode
// or not.
const (
	Alert  = "alert"
	Call   = "call"
	Ring   = "ring"
	Alarm  = "alarm"
	Media  = "media"
	Notify = "notify"
)

var priority = map[string]int{Alert: 6, Call: 5, Ring: 4, Alarm: 3, Media: 2, Notify: 1}

// Source produces mono samples in [-1, 1]. Fill returns false once it is
// done.
type Source interface {
	Fill(buf []float64) bool
	// Pos and Len are in samples at Rate; Len is 0 for endless sources.
	Pos() int
	Len() int
}

// Stereo sources fill both channels (music); the others play in the
// middle.
type Stereo interface {
	FillStereo(left, right []float64) bool
}

// Described sources say what plays now: a queue moves on to its next
// track by itself (gapless), and Changed reports that once.
type Described interface {
	Now() (media, title, label string)
	Changed() bool
}

type Stream struct {
	ID       string `json:"id"`
	App      string `json:"app"`
	Name     string `json:"name"`
	Category string `json:"category"`
	State    string `json:"state"`              // playing | paused | ended
	ByFocus  bool   `json:"by_focus,omitempty"` // paused by a higher category, resumes by itself
	Ducked   bool   `json:"ducked,omitempty"`
	PosMS    int    `json:"pos_ms"`
	LenMS    int    `json:"len_ms"`
	// Music: what plays (the app's media name, the format) and how it
	// reaches the sound card ("mixer 48 kHz", "pcm S32_LE 96000 Hz 2ch
	// bit-perfect", "dop …", "dsd …").
	Media    string  `json:"media,omitempty"`
	Format   string  `json:"format,omitempty"`
	Output   string  `json:"output,omitempty"`
	Gain     float64 `json:"-"`
	src      Source
	userStop bool
}

// Mixer holds the streams; Mix produces the next frame. Safe for concurrent
// use: RPC handlers change streams while the output loop mixes.
type Mixer struct {
	mu      sync.Mutex
	streams map[string]*Stream
	seq     int
	volume  map[string]float64
	route   string
	output  string // how the solo stream reaches the card
	shared  int64  // Frames when sounds last had to be mixed
	// OnChange is called (outside the lock) when a stream changes state.
	OnChange func(Stream)
	Frames   int64
}

// MixedOutput is the output of a media stream mixed with the others.
const MixedOutput = "mixer 48 kHz"

func NewMixer() *Mixer {
	return &Mixer{streams: map[string]*Stream{}, route: "earpiece", output: MixedOutput, shared: -Rate,
		volume: map[string]float64{Alert: 1, Call: 0.8, Ring: 0.9, Alarm: 1, Media: 0.7, Notify: 0.6}}
}

func (m *Mixer) Play(app, name, category string, src Source) (Stream, error) {
	if _, ok := priority[category]; !ok {
		return Stream{}, fmt.Errorf("unknown category %q", category)
	}
	m.mu.Lock()
	m.seq++
	s := &Stream{ID: fmt.Sprintf("a%d", m.seq), App: app, Name: name, Category: category, State: ui.PlayPlaying, Gain: 1, src: src}
	if d, ok := src.(Described); ok {
		media, title, label := d.Now()
		s.Media, s.Format = media, label
		if title != "" {
			s.Name = title
		}
	}
	var changed []Stream
	if category == Media {
		// One media stream at a time: a new song ends the previous one.
		for _, o := range m.streams {
			if o.Category == Media && o.State != ui.PlayEnded {
				o.State, o.userStop = ui.PlayEnded, true
				changed = append(changed, o.view(m))
				o.release()
				delete(m.streams, o.ID)
			}
		}
	} else {
		m.shared = m.Frames
	}
	m.streams[s.ID] = s
	changed = append(changed, m.applyFocus()...)
	out := s.view(m)
	m.mu.Unlock()
	m.notify(append(changed, out))
	return out, nil
}

func (m *Mixer) Control(id, op string) (Stream, error) {
	m.mu.Lock()
	s := m.streams[id]
	if s == nil {
		m.mu.Unlock()
		return Stream{}, fmt.Errorf("no stream %s", id)
	}
	switch op {
	case "pause":
		s.State, s.ByFocus = ui.PlayPaused, false
	case "resume":
		s.State = ui.PlayPlaying
	case "stop":
		s.State, s.userStop = ui.PlayEnded, true
		s.release()
		delete(m.streams, id)
	default:
		m.mu.Unlock()
		return Stream{}, fmt.Errorf("unknown control %q", op)
	}
	changed := append([]Stream{s.view(m)}, m.applyFocus()...)
	out := s.view(m)
	m.mu.Unlock()
	m.notify(changed)
	return out, nil
}

// Source is a stream's source (audiod queues, seeks and sets options on
// music through it); nil when there is no such stream.
func (m *Mixer) Source(id string) Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.streams[id]; s != nil {
		return s.src
	}
	return nil
}

// Touch reports a stream's state again (after a seek or a queue change).
func (m *Mixer) Touch(id string) (Stream, error) {
	m.mu.Lock()
	s := m.streams[id]
	if s == nil {
		m.mu.Unlock()
		return Stream{}, fmt.Errorf("no stream %s", id)
	}
	m.describe(s)
	out := s.view(m)
	m.mu.Unlock()
	m.notify([]Stream{out})
	return out, nil
}

func (m *Mixer) SetVolume(category string, v float64) {
	m.mu.Lock()
	m.volume[category] = math.Max(0, math.Min(1, v))
	m.mu.Unlock()
}

func (m *Mixer) SetRoute(route string) {
	m.mu.Lock()
	m.route = route
	m.mu.Unlock()
}

// SetOutput records how the solo stream reaches the card (shown in its
// state); it reports whether that changed.
func (m *Mixer) SetOutput(o string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.output == o {
		return false
	}
	m.output = o
	return true
}

// State lists the streams, highest priority first.
func (m *Mixer) State() []Stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Stream{}
	for _, s := range m.streams {
		out = append(out, s.view(m))
	}
	sort.Slice(out, func(i, j int) bool {
		if priority[out[i].Category] != priority[out[j].Category] {
			return priority[out[i].Category] > priority[out[j].Category]
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (m *Mixer) Route() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.route
}

// release frees what the source holds (an app's shared ring, a decoder).
func (s *Stream) release() {
	if c, ok := s.src.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

func (s *Stream) view(m *Mixer) Stream {
	v := *s
	v.src = nil
	if s.src != nil {
		v.PosMS = s.src.Pos() * 1000 / Rate
		v.LenMS = s.src.Len() * 1000 / Rate
	}
	if s.Category == Media && s.Format != "" {
		v.Output = m.output
	}
	return v
}

// describe takes a queue's new track into the stream; true if it moved.
func (m *Mixer) describe(s *Stream) bool {
	d, ok := s.src.(Described)
	if !ok || !d.Changed() {
		return false
	}
	media, title, label := d.Now()
	s.Media, s.Format = media, label
	if title != "" {
		s.Name = title
	}
	return true
}

// applyFocus pauses or ducks media under higher categories; called with
// the lock held, returns the streams that changed.
func (m *Mixer) applyFocus() []Stream {
	top, notifying := 0, false
	for _, s := range m.streams {
		if s.State == ui.PlayPlaying && s.Category != Media && s.Category != Notify {
			top = max(top, priority[s.Category])
		}
		notifying = notifying || (s.State == ui.PlayPlaying && s.Category == Notify)
	}
	var changed []Stream
	for _, s := range m.streams {
		if s.Category != Media {
			continue
		}
		switch {
		case top > priority[Media] && s.State == ui.PlayPlaying:
			s.State, s.ByFocus = ui.PlayPaused, true
			changed = append(changed, s.view(m))
		case top <= priority[Media] && s.State == ui.PlayPaused && s.ByFocus:
			s.State, s.ByFocus = ui.PlayPlaying, false
			changed = append(changed, s.view(m))
		}
		if s.Ducked != notifying {
			s.Ducked = notifying
		}
	}
	return changed
}

func (m *Mixer) notify(changed []Stream) {
	if m.OnChange == nil {
		return
	}
	for _, s := range changed {
		m.OnChange(s)
	}
}

// Mix renders the next frame as interleaved 16-bit stereo; out holds
// Channels * Frame samples.
func (m *Mixer) Mix(out []int16) {
	n := len(out) / Channels
	accL, accR := make([]float64, n), make([]float64, n)
	l, r := make([]float64, n), make([]float64, n)
	var changed []Stream
	m.mu.Lock()
	playing := 0
	for _, s := range m.streams {
		if s.State == ui.PlayPlaying {
			playing++
			if s.Category != Media {
				m.shared = m.Frames
			}
		}
	}
	if playing > 1 {
		m.shared = m.Frames
	}
	ended := false
	for id, s := range m.streams {
		if s.State != ui.PlayPlaying {
			continue
		}
		clear(l)
		clear(r)
		var more bool
		if st, ok := s.src.(Stereo); ok {
			more = st.FillStereo(l, r)
		} else {
			more = s.src.Fill(l)
			copy(r, l)
		}
		g := s.Gain * m.volume[s.Category]
		if s.Ducked {
			g *= 0.3
		}
		if s.Category == Call && m.route == "speaker" {
			g *= 1.4
		}
		for i := range accL {
			accL[i] += l[i] * g
			accR[i] += r[i] * g
		}
		if m.describe(s) {
			changed = append(changed, s.view(m))
		}
		if !more {
			s.State = ui.PlayEnded
			changed = append(changed, s.view(m))
			s.release()
			delete(m.streams, id)
			ended = true
		}
	}
	if ended {
		changed = append(changed, m.applyFocus()...)
	}
	m.Frames += int64(n)
	m.mu.Unlock()
	for i := range accL {
		out[2*i] = int16(math.Max(-1, math.Min(1, accL[i])) * 32767)
		out[2*i+1] = int16(math.Max(-1, math.Min(1, accR[i])) * 32767)
	}
	m.notify(changed)
}

// Solo is the stream that may go to the sound card by itself: the only
// one playing, a media stream, a second after anything else last needed
// mixing in. gain is its volume. ok is false when the mixer is needed.
func (m *Mixer) Solo() (id string, src Source, gain float64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Frames-m.shared < Rate {
		return "", nil, 0, false
	}
	var solo *Stream
	for _, s := range m.streams {
		if s.State != ui.PlayPlaying {
			continue
		}
		if solo != nil || s.Category != Media || s.Ducked {
			return "", nil, 0, false
		}
		solo = s
	}
	if solo == nil {
		return "", nil, 0, false
	}
	return solo.ID, solo.src, solo.Gain * m.volume[Media], true
}

// Played accounts for a frame's worth (20 ms) the solo stream played
// past the mixer: the clock moves on, the stream ends when it has no
// more, a queue's new track is reported.
func (m *Mixer) Played(id string, more bool) {
	m.mu.Lock()
	m.Frames += Frame
	s := m.streams[id]
	var changed []Stream
	if s != nil {
		if m.describe(s) {
			changed = append(changed, s.view(m))
		}
		if !more {
			s.State = ui.PlayEnded
			changed = append(changed, s.view(m))
			s.release()
			delete(m.streams, id)
			changed = append(changed, m.applyFocus()...)
		}
	}
	m.mu.Unlock()
	m.notify(changed)
}
