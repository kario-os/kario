// Package sensor reads the phone's sensors through the kernel's Industrial
// I/O subsystem (/sys/bus/iio/devices): the ambient light sensor that
// automatic brightness follows. Phones' light sensors (the proximity and
// light combos: Sensortek stk3x1x, Liteon ltr501, Avago apds9960, AMS
// tsl2772) expose illuminance in lux, or a raw count with a scale.
package sensor

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kario-os/kario/hal"
)

// Light is an ambient light sensor.
type Light interface {
	// Lux is the illuminance now.
	Lux() (float64, error)
	Name() string
}

// IIO is a light sensor of the IIO subsystem.
type IIO struct {
	dir, name string
}

func iioRoot() string { return filepath.Join(hal.SysRoot, "bus", "iio", "devices") }

// OpenLight is the first IIO device that measures illuminance, or the one
// called name (its "name" file, as the board configuration gives it); nil
// when there is none.
func OpenLight(name string) *IIO {
	ents, _ := os.ReadDir(iioRoot())
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "iio:device") {
			continue
		}
		dir := filepath.Join(iioRoot(), e.Name())
		n := read(filepath.Join(dir, "name"))
		if name != "" && n != name {
			continue
		}
		if exists(filepath.Join(dir, "in_illuminance_input")) || exists(filepath.Join(dir, "in_illuminance_raw")) {
			return &IIO{dir: dir, name: n}
		}
	}
	return nil
}

func (s *IIO) Name() string { return s.name }

// Lux reads in_illuminance_input (lux), or in_illuminance_raw with its
// offset and scale: (raw + offset) * scale.
func (s *IIO) Lux() (float64, error) {
	if v, ok := number(filepath.Join(s.dir, "in_illuminance_input")); ok {
		return math.Max(v, 0), nil
	}
	raw, ok := number(filepath.Join(s.dir, "in_illuminance_raw"))
	if !ok {
		return 0, errors.New(s.name + ": no illuminance reading")
	}
	scale, ok := number(filepath.Join(s.dir, "in_illuminance_scale"))
	if !ok {
		scale = 1
	}
	offset, _ := number(filepath.Join(s.dir, "in_illuminance_offset"))
	return math.Max((raw+offset)*scale, 0), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func read(path string) string {
	raw, _ := os.ReadFile(path)
	return strings.TrimSpace(string(raw))
}

func number(path string) (float64, bool) {
	v, err := strconv.ParseFloat(read(path), 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// SimLux is what the simulated sensor reads until set: an ordinary room.
const SimLux = 300

// Sim is a light sensor in memory.
type Sim struct {
	mu  sync.Mutex
	lux float64
}

func NewSim() *Sim { return &Sim{lux: SimLux} }

func (s *Sim) Name() string { return "sim" }

func (s *Sim) Lux() (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lux, nil
}

func (s *Sim) Set(lux float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lux = math.Max(lux, 0)
}
