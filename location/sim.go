package location

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SimLat and SimLon are where the simulated receiver starts (Tiananmen
// Square, Beijing).
const (
	SimLat = 39.9087
	SimLon = 116.3975
)

// Sim is a receiver that is not there, for the developer machine and the
// selftest. It speaks NMEA as a real one does (RMC, VTG, GGA, GSA, GSV each
// epoch), and its sentences go through the same Decoder, so the parsing
// path is the real one.
type Sim struct {
	// Interval between epochs; TTFF: epochs without a fix after the
	// receiver comes on (a warm start).
	Interval time.Duration
	TTFF     int
	Now      func() time.Time

	mu       sync.Mutex
	lat, lon float64
	alt      float64
	speed    float64
	course   float64
	sky      bool
	on       bool
	starts   int
}

func NewSim(now func() time.Time) *Sim {
	return &Sim{Interval: time.Second, TTFF: 2, Now: now, lat: SimLat, lon: SimLon, alt: 44.5, sky: true}
}

// Set moves the receiver; speed (m/s) and course (degrees) go into RMC and
// VTG as they are.
func (s *Sim) Set(lat, lon, speed, course float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lat, s.lon, s.speed, s.course = lat, lon, speed, course
}

// Position is where the receiver is.
func (s *Sim) Position() (lat, lon float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lat, s.lon
}

// Sky: false takes the satellites away (indoors): epochs without a fix.
func (s *Sim) Sky(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sky = on
}

// On reports whether the receiver is on, Starts how often it was turned on.
func (s *Sim) On() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

func (s *Sim) Starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

func (s *Sim) power(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.on = on
	if on {
		s.starts++
	}
}

func (s *Sim) Run(ctx context.Context, fix func(Fix)) error {
	s.power(true)
	defer s.power(false)
	d := &Decoder{Now: s.Now}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for n := 0; ; n++ {
		for _, l := range s.Epoch(s.now(), n >= s.TTFF) {
			if f, ok := d.Feed(l); ok {
				fix(f)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (s *Sim) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Epoch is the sentences of one epoch at t; without fix (or without the
// sky) they say so, as a receiver still searching does.
func (s *Sim) Epoch(t time.Time, fix bool) []string {
	s.mu.Lock()
	lat, lon, alt, speed, course := s.lat, s.lon, s.alt, s.speed, s.course
	fix = fix && s.sky
	s.mu.Unlock()
	t = t.UTC()
	hms := fmt.Sprintf("%02d%02d%02d.%02d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1e7)
	dmy := t.Format("020106")
	if !fix {
		return []string{
			Encode("GPRMC," + hms + ",V,,,,,,," + dmy + ",,,N"),
			Encode("GPGGA," + hms + ",,,,,0,00,99.9,,M,,M,,"),
			Encode("GPGSA,A,1,,,,,,,,,,,,,99.9,99.9,99.9"),
			Encode("GPGSV,1,1,00"),
		}
	}
	la, lo := nmeaCoord(lat, false), nmeaCoord(lon, true)
	knots := speed / knot
	// Standing still, a receiver has no course to give.
	cog := ""
	if speed >= 0.1 {
		cog = fmt.Sprintf("%.1f", course)
	}
	return []string{
		Encode(fmt.Sprintf("GPRMC,%s,A,%s,%.2f,%s,%s,,,A", hms, la+","+lo, knots, cog, dmy)),
		Encode(fmt.Sprintf("GPVTG,%s,T,,M,%.2f,N,%.2f,K,A", cog, knots, speed*3.6)),
		Encode(fmt.Sprintf("GPGGA,%s,%s,1,08,0.9,%.1f,M,-8.1,M,,", hms, la+","+lo, alt)),
		Encode("GPGSA,A,3,02,05,10,13,15,18,24,29,,,,,1.6,0.9,1.3"),
		Encode("GPGSV,3,1,10,02,47,071,44,05,32,182,41,10,61,322,46,13,22,256,38"),
		Encode("GPGSV,3,2,10,15,73,055,47,18,40,130,43,24,11,038,35,29,55,268,45"),
		Encode("GPGSV,3,3,10,21,05,345,,26,02,201,"),
	}
}

// nmeaCoord writes degrees as NMEA's ddmm.mmmm,N (dddmm.mmmm,E).
func nmeaCoord(v float64, lon bool) string {
	hemi := map[bool]string{true: "N", false: "S"}[v >= 0]
	width := 2
	if lon {
		hemi, width = map[bool]string{true: "E", false: "W"}[v >= 0], 3
	}
	v = math.Abs(v)
	deg := math.Floor(v)
	min := (v - deg) * 60
	if math.Round(min*1e4) >= 60e4 {
		deg, min = deg+1, 0
	}
	return fmt.Sprintf("%0*d%07.4f,%s", width, int(deg), min, hemi)
}

// Feed plays the receiver at the far end of a named pipe until ctx ends:
// an epoch every Interval, written whole (an epoch is less than a pipe's
// atomic write). While nothing reads the pipe (the GNSS is off) epochs are
// dropped, and the receiver starts again, TTFF and all, once something
// does.
func (s *Sim) Feed(ctx context.Context, path string) error {
	if err := MakeFIFO(path); err != nil {
		return err
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	defer s.power(false)
	n := 0
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		switch {
		case errors.Is(err, syscall.ENXIO):
			if n > 0 {
				s.power(false)
			}
			n = 0
		case err != nil:
			return err
		default:
			if n == 0 {
				s.power(true)
			}
			_, _ = f.WriteString(strings.Join(s.Epoch(s.now(), n >= s.TTFF), "\r\n") + "\r\n")
			f.Close()
			n++
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
