// Package backlight drives the Linux backlight class
// (/sys/class/backlight): the panel's brightness, dimmed before the screen
// goes off. Turning the panel itself off is the renderer's (it owns the
// display: DRM), and the panel driver usually switches its backlight with
// it; bl_power covers backlights the panel driver does not own.
package backlight

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kario-os/kario/hal"
)

// Backlight is one panel's backlight.
type Backlight interface {
	// Set is the brightness in percent of the maximum (0 is the dimmest
	// level that still lights, not off).
	Set(percent int) error
	// Power switches the backlight on or off without forgetting its level.
	Power(on bool) error
	Level() (percent int, on bool)
}

// Sysfs is a backlight of the class, named as the board names it
// (sku.json backlight) or, when the board names none, the first there is.
type Sysfs struct {
	name string
	mu   sync.Mutex
}

// Open is the backlight called name (empty: the first); nil when the
// machine has none.
func Open(name string) *Sysfs {
	if name == "" {
		ents, _ := os.ReadDir(filepath.Join(hal.SysRoot, "class", "backlight"))
		if len(ents) == 0 {
			return nil
		}
		name = ents[0].Name()
	}
	if _, err := os.Stat(filepath.Join(hal.SysRoot, "class", "backlight", name)); err != nil {
		return nil
	}
	return &Sysfs{name: name}
}

func (b *Sysfs) path(f string) string {
	return filepath.Join(hal.SysRoot, "class", "backlight", b.name, f)
}

func (b *Sysfs) int(f string) int {
	raw, _ := os.ReadFile(b.path(f))
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

func (b *Sysfs) Set(percent int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	top := b.int("max_brightness")
	if top <= 0 {
		return fmt.Errorf("backlight %s: no max_brightness", b.name)
	}
	percent = min(max(percent, 0), 100)
	v := max(1, (top*percent+50)/100)
	return os.WriteFile(b.path("brightness"), []byte(strconv.Itoa(v)), 0o644)
}

// bl_power takes the framebuffer blanking levels: 0 unblank, 4 powerdown.
func (b *Sysfs) Power(on bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := "4"
	if on {
		v = "0"
	}
	return os.WriteFile(b.path("bl_power"), []byte(v), 0o644)
}

func (b *Sysfs) Level() (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	top := b.int("max_brightness")
	if top <= 0 {
		return 0, false
	}
	return (b.int("brightness")*100 + top/2) / top, b.int("bl_power") == 0
}

// Sim is a backlight in memory.
type Sim struct {
	mu      sync.Mutex
	percent int
	on      bool
}

func NewSim() *Sim { return &Sim{percent: 100, on: true} }

func (s *Sim) Set(percent int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.percent = min(max(percent, 0), 100)
	return nil
}

func (s *Sim) Power(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.on = on
	return nil
}

func (s *Sim) Level() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.percent, s.on
}
