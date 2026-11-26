package location

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

// FuzzDecode is what a receiver, a serial line or a modem sends: a fix that
// comes out of it is a position on Earth with sane figures, and a sentence
// that parses is framed again the same way.
func FuzzDecode(f *testing.F) {
	f.Add([]byte(strings.Join([]string{refGGA, refGSA, refGSV, refRMC, refVTG}, "\r\n")))
	sim := NewSim(nil)
	for _, fix := range []bool{false, true} {
		f.Add([]byte(strings.Join(sim.Epoch(time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC), fix), "\r\n")))
	}
	sim.Set(-33.8688, -151.2093, 30, 359.9)
	f.Add([]byte(strings.Join(append(sim.Epoch(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), true), sim.Epoch(time.Date(2026, 11, 16, 0, 0, 1, 0, time.UTC), true)...), "\n")))
	f.Add([]byte(Encode("GNGGA,000000,9000.0000,N,18000.0000,W,4,99,0.01,99999.9,M,,M,,") + "\n" + Encode("GNRMC,000000,A,9000.0000,N,18000.0000,W,1999.9,360.0,290224,,,R")))
	f.Add([]byte("$GPGGA,,,,,,,,,,,,,,*56\n$PMTK001,314,3*36\n\x00\xff$GPGSV,9,9,999*00"))
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		d := &Decoder{Now: func() time.Time { return now }}
		var fixes []Fix
		for _, line := range bytes.Split(data, []byte("\n")) {
			l := string(line)
			if s, err := Parse(l); err == nil {
				addr := s.Talker + s.Type
				if s.Talker == "P" {
					addr = "P" + s.Type
				}
				if got := Encode(strings.Join(append([]string{addr}, s.Fields...), ",")); !strings.EqualFold(got, strings.TrimRight(l, "\r\n")) {
					t.Fatalf("%q framed again as %q", l, got)
				}
			}
			if fx, ok := d.Feed(l); ok {
				fixes = append(fixes, fx)
			}
		}
		if fx, ok := d.Flush(); ok {
			fixes = append(fixes, fx)
		}
		finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
		for _, fx := range fixes {
			switch {
			case !finite(fx.Lat) || !finite(fx.Lon) || math.Abs(fx.Lat) > 90 || math.Abs(fx.Lon) > 180:
				t.Fatalf("position %v %v", fx.Lat, fx.Lon)
			case !finite(fx.AccuracyM) || fx.AccuracyM <= 0:
				t.Fatalf("accuracy %v", fx.AccuracyM)
			case fx.HasSpeed && (!finite(fx.SpeedMS) || fx.SpeedMS < 0 || fx.SpeedMS > 1200):
				t.Fatalf("speed %v", fx.SpeedMS)
			case fx.HasBearing && (!finite(fx.Bearing) || fx.Bearing < 0 || fx.Bearing >= 360):
				t.Fatalf("bearing %v", fx.Bearing)
			case fx.HasAltitude && (!finite(fx.AltitudeM) || math.Abs(fx.AltitudeM) > 100000):
				t.Fatalf("altitude %v", fx.AltitudeM)
			case fx.Satellites < 0 || fx.Satellites > maxSatellites || fx.InView < 0 || fx.InView > maxSatellites:
				t.Fatalf("satellites %d in view %d", fx.Satellites, fx.InView)
			case fx.Time.IsZero() || fx.Time.Year() < 1980 || fx.Time.Year() > 2100:
				t.Fatalf("time %v", fx.Time)
			}
		}
		if d.Bad > d.Sentences {
			t.Fatalf("%d bad of %d", d.Bad, d.Sentences)
		}
	})
}
