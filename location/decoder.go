package location

import (
	"math"
	"time"
)

// Fix is a position from the receiver.
type Fix struct {
	// Time is the receiver's (UTC).
	Time     time.Time
	Lat, Lon float64 // WGS-84 degrees
	// AccuracyM is the horizontal accuracy, about one standard deviation:
	// the HDOP times the error a range of this kind of fix has.
	AccuracyM float64
	// AltitudeM is above mean sea level; SpeedMS over ground; Bearing the
	// course over ground in degrees clockwise from true north. Each only
	// with its Has flag (0 is a real altitude, speed and bearing).
	AltitudeM   float64
	SpeedMS     float64
	Bearing     float64
	HasAltitude bool
	HasSpeed    bool
	HasBearing  bool
	// Satellites used in the fix, and in view (all constellations).
	Satellites int
	InView     int
	HDOP       float64
	// Quality is GGA's fix quality (1 GPS, 2 DGPS, 4 RTK fixed, 5 RTK
	// float, 6 dead reckoning...), 0 when no GGA came with the fix.
	Quality int
	// Network: a network location service placed the phone (Locator),
	// not a receiver.
	Network bool
}

// uere is the range error behind each unit of HDOP, by GGA fix quality: a
// consumer receiver's few metres, less with corrections.
func uere(quality int) float64 {
	switch quality {
	case 2:
		return 2
	case 4:
		return 0.05
	case 5:
		return 0.5
	case 6:
		return 15
	}
	return 5
}

// unknownAccuracyM is claimed for a fix whose receiver gave no HDOP.
const unknownAccuracyM = 50

const knot = 1852.0 / 3600

// Most satellites one sentence may claim (every constellation together has
// fewer than this in orbit).
const maxSatellites = 255

// Decoder assembles a receiver's sentences into fixes. The sentences of one
// epoch carry the same UTC time (GGA, RMC, GLL) or none (GSA, GSV, VTG); an
// epoch makes one fix, handed out once it holds every kind of timed sentence
// the receiver sent in the epoch before (or both GGA and RMC), else when the
// next epoch begins (Flush at the end of the stream).
type Decoder struct {
	// Now dates a fix when the receiver sends no date (no RMC yet): the
	// day that puts it closest to Now.
	Now func() time.Time
	// Sentences counts the sentences fed, Bad those refused.
	Sentences, Bad int

	cur      epoch
	lastSeen map[string]bool
	day      time.Time
	lastTOD  time.Duration
	seq      int
	gsaHDOP  float64
	gsaSeq   int
	vtg      struct {
		speed, course float64
		hasSpeed      bool
		hasCourse     bool
		seq           int
	}
	inView map[string]struct{ n, seq int }
}

type epoch struct {
	tod     time.Duration
	timed   bool
	seen    map[string]bool
	fix     Fix
	pos     bool
	void    bool
	emitted bool
}

// Feed takes one line; a fix comes back when it completes one.
func (d *Decoder) Feed(line string) (Fix, bool) {
	d.Sentences++
	s, err := Parse(line)
	if err != nil {
		d.Bad++
		return Fix{}, false
	}
	switch s.Type {
	case "GGA", "RMC", "GLL":
		tod, ok := timeOfDay(s.field(map[string]int{"GGA": 0, "RMC": 0, "GLL": 4}[s.Type]))
		if !ok {
			d.Bad++
			return Fix{}, false
		}
		var out Fix
		var done bool
		if d.cur.timed && d.cur.tod != tod {
			out, done = d.finish()
		}
		if !d.cur.timed {
			d.cur = epoch{tod: tod, timed: true, seen: map[string]bool{}}
		}
		if d.timed(s) {
			d.cur.seen[s.Type] = true
		} else {
			d.Bad++
		}
		// A complete epoch supersedes the one before it that never was.
		if d.cur.pos && !d.cur.void && !d.cur.emitted && d.complete() {
			return d.emit()
		}
		if done {
			return out, true
		}
	case "GSA":
		d.gsaSentence(s)
	case "GSV":
		d.gsvSentence(s)
	case "VTG":
		d.vtgSentence(s)
	}
	return Fix{}, false
}

// Flush hands out the epoch in progress, if it holds a fix not handed out.
func (d *Decoder) Flush() (Fix, bool) {
	out, ok := d.finish()
	d.cur = epoch{}
	return out, ok
}

func (d *Decoder) complete() bool {
	if len(d.lastSeen) > 0 {
		for k := range d.lastSeen {
			if !d.cur.seen[k] {
				return false
			}
		}
		return true
	}
	return d.cur.seen["GGA"] && d.cur.seen["RMC"]
}

func (d *Decoder) finish() (Fix, bool) {
	if !d.cur.timed {
		return Fix{}, false
	}
	d.lastSeen = d.cur.seen
	d.seq++
	if d.cur.pos && !d.cur.void && !d.cur.emitted {
		f, ok := d.emit()
		d.cur = epoch{}
		return f, ok
	}
	d.cur = epoch{}
	return Fix{}, false
}

func (d *Decoder) emit() (Fix, bool) {
	d.cur.emitted = true
	f := d.cur.fix
	if f.HDOP == 0 && d.gsaHDOP > 0 && d.seq-d.gsaSeq <= 1 {
		f.HDOP = d.gsaHDOP
	}
	if f.HDOP > 0 {
		f.AccuracyM = math.Max(0.01, f.HDOP*uere(f.Quality))
	} else {
		f.AccuracyM = unknownAccuracyM
	}
	if d.seq-d.vtg.seq <= 1 {
		if !f.HasSpeed && d.vtg.hasSpeed {
			f.SpeedMS, f.HasSpeed = d.vtg.speed, true
		}
		if !f.HasBearing && d.vtg.hasCourse {
			f.Bearing, f.HasBearing = d.vtg.course, true
		}
	}
	for _, v := range d.inView {
		if d.seq-v.seq <= 2 {
			f.InView += v.n
		}
	}
	f.InView = min(f.InView, maxSatellites)
	f.Time = d.stamp(d.cur.tod)
	return f, true
}

// stamp dates a time of day: the receiver's last date, moved on past
// midnight, else the day nearest Now.
func (d *Decoder) stamp(tod time.Duration) time.Time {
	if !d.day.IsZero() {
		if tod+12*time.Hour < d.lastTOD {
			d.day = d.day.AddDate(0, 0, 1)
		}
		d.lastTOD = tod
		return d.day.Add(tod)
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	n := now().UTC()
	base := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	best := base.Add(tod)
	for _, off := range []int{-1, 1} {
		if t := base.AddDate(0, 0, off).Add(tod); absDur(t.Sub(n)) < absDur(best.Sub(n)) {
			best = t
		}
	}
	return best
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// timed merges a GGA, RMC or GLL into the epoch; false for a malformed one.
func (d *Decoder) timed(s Sentence) bool {
	e := &d.cur
	switch s.Type {
	case "GGA":
		q, ok := integer(s.field(5), 9)
		if !ok {
			return false
		}
		if q == 0 {
			e.void = true
			return true
		}
		lat, lon, ok := latLon(s, 1)
		if !ok {
			return false
		}
		e.fix.Lat, e.fix.Lon, e.fix.Quality, e.pos = lat, lon, q, true
		if n, ok := integer(s.field(6), maxSatellites); ok {
			e.fix.Satellites = n
		}
		if h, ok := decimal(s.field(7), false); ok && h > 0 && h < 100 {
			e.fix.HDOP = h
		}
		if alt, ok := decimal(s.field(8), true); ok && alt > -1000 && alt < 100000 && (s.field(9) == "M" || s.field(9) == "") {
			e.fix.AltitudeM, e.fix.HasAltitude = alt, true
		}
	case "RMC":
		if s.field(1) != "A" || s.field(11) == "N" {
			e.void = true
			return s.field(1) == "V" || s.field(1) == "A"
		}
		if day, ok := date(s.field(8)); ok {
			d.day, d.lastTOD = day, e.tod
		}
		lat, lon, ok := latLon(s, 2)
		if !ok {
			return false
		}
		if !e.pos {
			e.fix.Lat, e.fix.Lon, e.pos = lat, lon, true
		}
		if v, ok := decimal(s.field(6), false); ok && v < 2000 {
			e.fix.SpeedMS, e.fix.HasSpeed = v*knot, true
		}
		if c, ok := decimal(s.field(7), false); ok && c <= 360 {
			e.fix.Bearing, e.fix.HasBearing = math.Mod(c, 360), true
		}
	case "GLL":
		if s.field(5) != "A" || s.field(6) == "N" {
			e.void = true
			return s.field(5) == "V" || s.field(5) == "A"
		}
		lat, lon, ok := latLon(s, 0)
		if !ok {
			return false
		}
		if !e.pos {
			e.fix.Lat, e.fix.Lon, e.pos = lat, lon, true
		}
	}
	return true
}

func (d *Decoder) gsaSentence(s Sentence) {
	if mode, ok := integer(s.field(1), 3); !ok || mode < 2 {
		return
	}
	if h, ok := decimal(s.field(15), false); ok && h > 0 && h < 100 {
		d.gsaHDOP, d.gsaSeq = h, d.seq
	}
}

// maxTalkers bounds the constellations counted in view.
const maxTalkers = 8

func (d *Decoder) gsvSentence(s Sentence) {
	n, ok := integer(s.field(2), maxSatellites)
	if !ok {
		d.Bad++
		return
	}
	if d.inView == nil {
		d.inView = map[string]struct{ n, seq int }{}
	}
	if _, known := d.inView[s.Talker]; known || len(d.inView) < maxTalkers {
		d.inView[s.Talker] = struct{ n, seq int }{n, d.seq}
	}
}

func (d *Decoder) vtgSentence(s Sentence) {
	if s.field(8) == "N" {
		return
	}
	d.vtg.hasSpeed, d.vtg.hasCourse, d.vtg.seq = false, false, d.seq
	if v, ok := decimal(s.field(6), false); ok && v < 4000 {
		d.vtg.speed, d.vtg.hasSpeed = v/3.6, true
	} else if v, ok := decimal(s.field(4), false); ok && v < 2000 {
		d.vtg.speed, d.vtg.hasSpeed = v*knot, true
	}
	if c, ok := decimal(s.field(0), false); ok && c <= 360 {
		d.vtg.course, d.vtg.hasCourse = math.Mod(c, 360), true
	}
}
