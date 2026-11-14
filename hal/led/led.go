// Package led drives the Linux LED class (/sys/class/leds): the
// notification light that slow-blinks while there are unread messages or
// missed calls. Blinking uses the kernel's timer trigger, which hands the
// pattern to the LED controller when its driver can blink by itself (the
// PMIC LEDs of phones: qcom-lpg) and otherwise toggles it from a kernel
// timer; either way no process wakes up to blink.
package led

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/hal"
)

// Pattern is what an LED shows: off (the zero value), steady on (On > 0,
// Off 0) or blinking On then Off.
type Pattern struct {
	On, Off time.Duration
}

// SlowBlink is the notification pattern: a short flash every five seconds
// (4% of the time lit).
var SlowBlink = Pattern{On: 200 * time.Millisecond, Off: 4800 * time.Millisecond}

func (p Pattern) String() string {
	switch {
	case p.On == 0:
		return "off"
	case p.Off == 0:
		return "on"
	}
	return fmt.Sprintf("blink %d/%d ms", p.On.Milliseconds(), p.Off.Milliseconds())
}

// Light is one LED.
type Light interface {
	Name() string
	Set(Pattern) error
	Pattern() Pattern
}

// Config is an LED as a board names it (sku.json leds): the LED class name
// and, for a multicolor LED, the intensity of each channel (in the order
// of its multi_index).
type Config struct {
	Name  string `json:"name"`
	Color []int  `json:"color,omitempty"`
}

// Sysfs is an LED of the LED class.
type Sysfs struct {
	cfg  Config
	mu   sync.Mutex
	last Pattern
}

// Open is the LED named in cfg; it need not exist yet (a virtual LED may
// come later), each Set looks it up again.
func Open(cfg Config) *Sysfs { return &Sysfs{cfg: cfg} }

func (l *Sysfs) Name() string { return l.cfg.Name }

func (l *Sysfs) dir() string { return filepath.Join(hal.SysRoot, "class", "leds", l.cfg.Name) }

func (l *Sysfs) write(file, value string) error {
	return os.WriteFile(filepath.Join(l.dir(), file), []byte(value), 0o644)
}

func (l *Sysfs) read(file string) string {
	b, _ := os.ReadFile(filepath.Join(l.dir(), file))
	return strings.TrimSpace(string(b))
}

// Set shows p. Brightness is the LED's maximum: the light is for noticing,
// and a short flash at full brightness costs less than a long dim one.
func (l *Sysfs) Set(p Pattern) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := os.Stat(l.dir()); err != nil {
		return fmt.Errorf("led %s: %w", l.cfg.Name, err)
	}
	if err := l.write("trigger", "none"); err != nil {
		return err
	}
	if p.On == 0 {
		l.last = p
		return l.write("brightness", "0")
	}
	if len(l.cfg.Color) > 0 {
		parts := make([]string, len(l.cfg.Color))
		for i, c := range l.cfg.Color {
			parts[i] = strconv.Itoa(c)
		}
		if err := l.write("multi_intensity", strings.Join(parts, " ")); err != nil {
			return err
		}
	}
	max := l.read("max_brightness")
	if max == "" || max == "0" {
		max = "1"
	}
	if err := l.write("brightness", max); err != nil {
		return err
	}
	if p.Off > 0 {
		if err := l.write("trigger", "timer"); err != nil {
			return err
		}
		if err := l.write("delay_on", strconv.FormatInt(p.On.Milliseconds(), 10)); err != nil {
			return err
		}
		if err := l.write("delay_off", strconv.FormatInt(p.Off.Milliseconds(), 10)); err != nil {
			return err
		}
	}
	l.last = p
	return nil
}

// Pattern is what the LED shows, read back from the class: the trigger in
// force and its delays.
func (l *Sysfs) Pattern() Pattern {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.Contains(l.read("trigger"), "[timer]") {
		on, _ := strconv.Atoi(l.read("delay_on"))
		off, _ := strconv.Atoi(l.read("delay_off"))
		return Pattern{On: time.Duration(on) * time.Millisecond, Off: time.Duration(off) * time.Millisecond}
	}
	if b, _ := strconv.Atoi(l.read("brightness")); b > 0 {
		return Pattern{On: l.last.On}
	}
	return Pattern{}
}

// Sim is an LED in memory: the developer machine has none to drive.
type Sim struct {
	name string
	mu   sync.Mutex
	p    Pattern
	sets int
}

func NewSim(name string) *Sim { return &Sim{name: name} }

func (s *Sim) Name() string { return s.name }

func (s *Sim) Set(p Pattern) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p = p
	s.sets++
	return nil
}

func (s *Sim) Pattern() Pattern {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.p
}

// Sets counts the changes asked for.
func (s *Sim) Sets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sets
}

// ErrNoUleds: the kernel has no userspace LEDs (CONFIG_LEDS_USER).
var ErrNoUleds = errors.New("no /dev/uleds")
