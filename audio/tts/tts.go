// Package tts is the speech service phone-audiod runs for the screen
// reader (shell/reader.go): a line is said at once, cutting what is being
// said, or after it. The voice is an Engine. The one built in, Simulated,
// has no voice: it logs what it would say and takes as long as a voice
// would. No engine the zero-GPLv3 images may carry speaks both Chinese
// and English and is small (19-core-apps.md §1.4), so a real one is not
// implemented yet.
package tts

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// Engine speaks.
type Engine interface {
	Name() string
	// Say starts saying text in the language lang (a tag: zh-Hans, en,
	// es) and returns how long that takes.
	Say(text, lang string) (time.Duration, error)
	// Stop cuts what is being said.
	Stop()
}

// Utterance is a line the engine was given.
type Utterance struct {
	Text      string    `json:"text"`
	Lang      string    `json:"lang"`
	At        time.Time `json:"at"`
	Interrupt bool      `json:"interrupt,omitempty"`
}

// State is speech.state's answer: the engine, whether it only pretends,
// whether it is saying something, and the lines said last, oldest first.
type State struct {
	Engine    string      `json:"engine"`
	Simulated bool        `json:"simulated"`
	Speaking  bool        `json:"speaking"`
	Said      []Utterance `json:"said"`
}

const (
	// MaxRunes cuts a line: a screen reader reads a page item by item.
	MaxRunes = 2000
	// kept is how many lines State lists.
	kept = 64
)

// Service queues lines for one engine. Safe for concurrent use.
type Service struct {
	mu     sync.Mutex
	engine Engine
	queue  []Utterance
	until  time.Time
	timer  *time.Timer
	said   []Utterance
	gen    int
}

func New(e Engine) *Service { return &Service{engine: e} }

// Say says text: interrupt cuts what is being said and drops what waits,
// else it waits its turn. Empty text says nothing.
func (s *Service) Say(text, lang string, interrupt bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if r := []rune(text); len(r) > MaxRunes {
		text = string(r[:MaxRunes-1]) + "…"
	}
	u := Utterance{Text: text, Lang: lang, At: time.Now(), Interrupt: interrupt}
	s.mu.Lock()
	defer s.mu.Unlock()
	busy := time.Now().Before(s.until)
	switch {
	case interrupt:
		s.queue = nil
		if busy {
			s.engine.Stop()
		}
	case busy:
		s.queue = append(s.queue, u)
		return nil
	}
	return s.start(u)
}

// Stop cuts what is being said and drops what waits.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = nil
	if time.Now().Before(s.until) {
		s.engine.Stop()
	}
	s.until = time.Time{}
	s.gen++
}

// State is the service as speech.state reports it.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, sim := s.engine.(Simulated)
	return State{Engine: s.engine.Name(), Simulated: sim, Speaking: time.Now().Before(s.until), Said: append([]Utterance{}, s.said...)}
}

// start gives u to the engine; the lock is held.
func (s *Service) start(u Utterance) error {
	u.At = time.Now()
	d, err := s.engine.Say(u.Text, u.Lang)
	if err != nil {
		s.until = time.Time{}
		return err
	}
	if s.said = append(s.said, u); len(s.said) > kept {
		s.said = s.said[len(s.said)-kept:]
	}
	s.until = u.At.Add(d)
	s.gen++
	gen := s.gen
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(d, func() { s.next(gen) })
	return nil
}

// next says the line that waited, once the one started as gen is done.
func (s *Service) next(gen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || len(s.queue) == 0 {
		return
	}
	u := s.queue[0]
	s.queue = s.queue[1:]
	_ = s.start(u)
}

// Simulated is the engine without a voice: Log gets each line, and it
// takes as long as Duration says a voice would.
type Simulated struct {
	Log func(format string, args ...any)
}

func (Simulated) Name() string { return "simulated" }

func (e Simulated) Say(text, lang string) (time.Duration, error) {
	if e.Log != nil {
		e.Log("tts (simulated, no voice): %s: %s", lang, text)
	}
	return Duration(text), nil
}

func (Simulated) Stop() {}

// Duration is about how long a voice takes over text at a screen reader's
// usual rate: Chinese and Japanese characters at four a second, other
// words at two and a half, and a short pause after each sentence or
// clause.
func Duration(text string) time.Duration {
	var d time.Duration
	inWord := false
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana):
			d += 250 * time.Millisecond
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				d += 400 * time.Millisecond
			}
			inWord = true
		case r == '\'' || r == '-':
		case unicode.IsPunct(r):
			d += 200 * time.Millisecond
			inWord = false
		default:
			inWord = false
		}
	}
	return max(d, 200*time.Millisecond)
}
