package location

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Source is a receiver. Run turns it on, hands each fix to fix (from Run's
// goroutine) until ctx ends, then turns it off; it returns early, with the
// reason, when the receiver fails or is not there.
type Source interface {
	Run(ctx context.Context, fix func(Fix)) error
}

// The kinds of receiver a SKU names (sku.json "gnss").
const (
	// KindSim: a simulated receiver (the developer machine).
	KindSim = "sim"
	// KindNMEA: a device that streams NMEA: the kernel GNSS subsystem's
	// /dev/gnssN (opening it powers the receiver), or a serial port.
	KindNMEA = "nmea"
	// KindFIFO: a named pipe the Shell makes; whatever writes NMEA into it
	// is the receiver (the emulator, where the selftest writes it).
	KindFIFO = "fifo"
	// KindModemManager: the GPS of the modem, through ModemManager's
	// Location interface (its GPS NMEA source).
	KindModemManager = "modemmanager"
)

// Spec is a receiver as a SKU names it: "sim", "modemmanager",
// "nmea:<device>[@<baud>]" or "fifo:<path>"; a pipe's path without a
// directory is in the Shell's run directory (the selftest's own on the
// emulator).
type Spec struct {
	Kind string
	Path string
	Baud int
}

func (s Spec) String() string {
	switch {
	case s.Path == "":
		return s.Kind
	case s.Baud > 0:
		return fmt.Sprintf("%s:%s@%d", s.Kind, s.Path, s.Baud)
	}
	return s.Kind + ":" + s.Path
}

// ParseSpec reads a receiver spec.
func ParseSpec(v string) (Spec, error) {
	kind, rest, _ := strings.Cut(v, ":")
	switch kind {
	case KindSim, KindModemManager:
		if rest != "" {
			return Spec{}, fmt.Errorf("gnss %q: %s takes no path", v, kind)
		}
		return Spec{Kind: kind}, nil
	case KindNMEA, KindFIFO:
		path, baud, hasBaud := strings.Cut(rest, "@")
		name := kind == KindFIFO && path != "" && path != "." && path != ".." && !strings.Contains(path, "/")
		if !name && (!strings.HasPrefix(path, "/") || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..")) {
			return Spec{}, fmt.Errorf("gnss %q: %s needs an absolute path", v, kind)
		}
		s := Spec{Kind: kind, Path: path}
		if hasBaud {
			n, err := strconv.Atoi(baud)
			if err != nil || kind != KindNMEA || termiosSpeed(n) == 0 {
				return Spec{}, fmt.Errorf("gnss %q: no such serial speed", v)
			}
			s.Baud = n
		}
		return s, nil
	}
	return Spec{}, fmt.Errorf("gnss %q is not sim, modemmanager, nmea:<device> or fifo:<path>", v)
}

// New is the receiver of a spec; now dates fixes that come without a date.
func New(s Spec, now func() time.Time) Source {
	switch s.Kind {
	case KindSim:
		return NewSim(now)
	case KindNMEA, KindFIFO:
		return &Stream{Path: s.Path, FIFO: s.Kind == KindFIFO, Baud: s.Baud, Now: now}
	case KindModemManager:
		return &ModemManager{Now: now}
	}
	return nil
}
