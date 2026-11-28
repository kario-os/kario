package location

import (
	"math"
	"strings"
	"testing"
	"time"
)

// The sentences of the NMEA 0183 reference, checksums and all.
const (
	refGGA = "$GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*47"
	refGSA = "$GPGSA,A,3,04,05,,09,12,,,24,,,,,2.5,1.3,2.1*39"
	refGSV = "$GPGSV,2,1,08,01,40,083,46,02,17,308,41,12,07,344,39,14,22,228,45*75"
	refRMC = "$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230394,003.1,W*6A"
	refVTG = "$GPVTG,054.7,T,034.4,M,005.5,N,010.2,K*48"
)

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestParse(t *testing.T) {
	for _, line := range []string{refGGA, refGSA, refGSV, refRMC, refVTG, refGGA + "\r\n", strings.Replace(refRMC, "*6A", "*6a", 1)} {
		if _, err := Parse(line); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
	s, err := Parse(refRMC)
	if err != nil || s.Talker != "GP" || s.Type != "RMC" || len(s.Fields) != 11 || s.Fields[8] != "230394" {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err := Parse(Encode("PMTK001,314,3")); err != nil || s.Talker != "P" || s.Type != "MTK001" {
		t.Fatalf("proprietary: %+v %v", s, err)
	}
	if got := Encode("GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,"); got != refGGA {
		t.Fatalf("Encode: %s", got)
	}
	for line, want := range map[string]error{
		strings.Replace(refGGA, "*47", "*48", 1):       ErrChecksum,
		strings.TrimSuffix(refGGA, "*47"):              ErrChecksum,
		strings.Replace(refGGA, "*47", "*4G", 1):       ErrChecksum,
		"GPGGA" + refGGA[6:]:                           ErrFrame,
		"$" + strings.Repeat("A", MaxSentence) + "*00": ErrFrame,
		Encode("GPGGA,12\x003519"):                     ErrFrame,
		Encode("GPGGA,12$3519"):                        ErrFrame,
		Encode("gpgga,123519"):                         ErrFrame,
		Encode("GPGGAX,123519"):                        ErrFrame,
		"$*00":                                         ErrFrame,
	} {
		if _, err := Parse(line); err != want {
			t.Errorf("%q: %v, want %v", line, err, want)
		}
	}
}

func TestFields(t *testing.T) {
	for _, c := range []struct {
		v, hemi string
		lon     bool
		want    float64
		ok      bool
	}{
		{"4807.038", "N", false, 48.1173, true},
		{"4807.038", "S", false, -48.1173, true},
		{"01131.000", "E", true, 11 + 31.0/60, true},
		{"01131.000", "W", true, -(11 + 31.0/60), true},
		{"0000.0000", "N", false, 0, true},
		{"9000.0000", "N", false, 90, true},
		{"9000.0001", "N", false, 0, false},
		{"18000.000", "E", true, 180, true},
		{"18000.001", "W", true, 0, false},
		{"4860.000", "N", false, 0, false},
		{"4807.038", "E", false, 0, false},
		{"01131.000", "N", true, 0, false},
		{"NaN", "N", false, 0, false},
		{"Inf", "N", false, 0, false},
		{"1e3", "N", false, 0, false},
		{"-4807.038", "N", false, 0, false},
		{"48.07.038", "N", false, 0, false},
		{"", "N", false, 0, false},
	} {
		got, ok := coordinate(c.v, c.hemi, c.lon)
		if ok != c.ok || (ok && !near(got, c.want, 1e-9)) {
			t.Errorf("%s %s: %v %v, want %v %v", c.v, c.hemi, got, ok, c.want, c.ok)
		}
	}
	for v, want := range map[string]time.Duration{"123519": 12*time.Hour + 35*time.Minute + 19*time.Second, "235960.50": 23*time.Hour + 59*time.Minute + 60500*time.Millisecond, "000000.000": 0} {
		if got, ok := timeOfDay(v); !ok || got != want {
			t.Errorf("time %s: %v %v", v, got, ok)
		}
	}
	for _, v := range []string{"", "12351", "243519", "126019", "123561", "12.519", "1235190", "1235a9"} {
		if _, ok := timeOfDay(v); ok {
			t.Errorf("time %q accepted", v)
		}
	}
	if d, ok := date("230394"); !ok || d != time.Date(1994, 3, 23, 0, 0, 0, 0, time.UTC) {
		t.Errorf("date 230394: %v", d)
	}
	if d, ok := date("161126"); !ok || d.Year() != 2026 {
		t.Errorf("date 161126: %v", d)
	}
	for _, v := range []string{"310226", "000126", "011326", "0101", "aa0126"} {
		if _, ok := date(v); ok {
			t.Errorf("date %q accepted", v)
		}
	}
}

func feed(d *Decoder, lines ...string) []Fix {
	var out []Fix
	for _, l := range lines {
		if f, ok := d.Feed(l); ok {
			out = append(out, f)
		}
	}
	return out
}

// One epoch of the reference sentences makes one fix with everything they
// say; it is handed out with the RMC, the second timed sentence.
func TestDecodeReference(t *testing.T) {
	d := &Decoder{}
	got := feed(d, refGGA, refGSA, refGSV, refRMC)
	if len(got) != 1 {
		t.Fatalf("%d fixes", len(got))
	}
	f := got[0]
	if !near(f.Lat, 48.1173, 1e-9) || !near(f.Lon, 11+31.0/60, 1e-9) {
		t.Errorf("position %v %v", f.Lat, f.Lon)
	}
	if !f.HasAltitude || f.AltitudeM != 545.4 || f.Satellites != 8 || f.InView != 8 || f.HDOP != 0.9 || f.Quality != 1 {
		t.Errorf("%+v", f)
	}
	if !near(f.AccuracyM, 4.5, 1e-9) {
		t.Errorf("accuracy %v from HDOP 0.9", f.AccuracyM)
	}
	if !f.HasSpeed || !near(f.SpeedMS, 22.4*1852/3600, 1e-9) || !f.HasBearing || f.Bearing != 84.4 {
		t.Errorf("speed %v bearing %v", f.SpeedMS, f.Bearing)
	}
	if want := time.Date(1994, 3, 23, 12, 35, 19, 0, time.UTC); !f.Time.Equal(want) {
		t.Errorf("time %v, want %v", f.Time, want)
	}
	if more := feed(d, refVTG); len(more) != 0 {
		t.Errorf("the epoch's VTG made another fix: %+v", more)
	}
	if d.Sentences != 5 || d.Bad != 0 {
		t.Errorf("%d sentences, %d bad", d.Sentences, d.Bad)
	}
}

// A receiver that sends only GGA: its first fix comes when the second
// epoch begins, the others as soon as their GGA is in.
func TestDecodeGGAOnly(t *testing.T) {
	d := &Decoder{Now: func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) }}
	gga := func(tod string) string {
		return Encode("GNGGA," + tod + ",3114.4820,N,12128.4220,E,1,12,0.6,4.0,M,,M,,")
	}
	if got := feed(d, gga("120000.00")); len(got) != 0 {
		t.Fatalf("a fix before the epoch was known complete: %+v", got)
	}
	got := feed(d, gga("120001.00"))
	if len(got) != 1 || !got[0].Time.Equal(time.Date(2026, 11, 16, 12, 0, 1, 0, time.UTC)) {
		t.Fatalf("second epoch: %+v", got)
	}
	got = feed(d, gga("120002.00"))
	if len(got) != 1 || got[0].Time.Second() != 2 || !near(got[0].Lat, 31.2413666, 1e-6) || !near(got[0].Lon, 121.4737, 1e-6) {
		t.Fatalf("third epoch: %+v", got)
	}
	if f, ok := d.Flush(); ok {
		t.Fatalf("flushed a fix handed out already: %+v", f)
	}
}

// Without a date the fix is put on the day nearest the clock (here just
// after midnight UTC, a fix from just before it is yesterday's); with
// RMC's date, midnight moves the date on.
func TestDecodeDates(t *testing.T) {
	d := &Decoder{Now: func() time.Time { return time.Date(2026, 11, 17, 0, 0, 1, 0, time.UTC) }}
	feed(d, Encode("GPGGA,235959.00,0000.0000,N,00000.0000,E,1,05,1.0,0,M,,M,,"))
	f, ok := d.Flush()
	if !ok || !f.Time.Equal(time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC)) {
		t.Fatalf("%v %v", f.Time, ok)
	}
	d = &Decoder{}
	rmc := func(tod, dmy string) string {
		return Encode("GPRMC," + tod + ",A,0000.0000,N,00000.0000,E,0.0,," + dmy + ",,,A")
	}
	gga := func(tod string) string { return Encode("GPGGA," + tod + ",0000.0000,N,00000.0000,E,1,05,1.0,0,M,,M,,") }
	got := feed(d, rmc("235959.00", "161126"), gga("235959.00"), gga("000000.00"))
	if f, ok := d.Flush(); ok {
		got = append(got, f)
	}
	if len(got) != 2 || !got[0].Time.Equal(time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC)) || !got[1].Time.Equal(time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", got)
	}
}

// Epochs without a fix (the receiver still searching, RMC void, GGA
// quality 0, mode N) give nothing, and malformed sentences are counted.
func TestDecodeNoFix(t *testing.T) {
	s := NewSim(nil)
	d := &Decoder{}
	now := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if got := feed(d, s.Epoch(now.Add(time.Duration(i)*time.Second), false)...); len(got) != 0 {
			t.Fatalf("a fix from a searching receiver: %+v", got)
		}
	}
	if _, ok := d.Flush(); ok {
		t.Fatal("flushed a fix from a searching receiver")
	}
	bad := []string{
		Encode("GPRMC,010000.00,A,4807.038,N,01131.000,E,0.0,,161126,,,N"),
		Encode("GPGGA,010001.00,NaN,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,"),
		Encode("GPGGA,010002.00,4807.038,N,01131.000,E,x,08,0.9,545.4,M,46.9,M,,"),
		Encode("GPGGA,250000.00,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,"),
		"$GPGGA,010003.00,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*00",
		"\x00\xff garbage from a serial line",
	}
	d = &Decoder{}
	if got := feed(d, bad...); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if f, ok := d.Flush(); ok {
		t.Fatalf("flushed %+v", f)
	}
	if d.Bad != 5 {
		t.Errorf("%d bad of %d", d.Bad, d.Sentences)
	}
}

// Southern and western hemispheres, the course and speed from VTG when
// RMC has none, the HDOP from GSA when GGA has none, and the
// satellites in view of every constellation.
func TestDecodeMixed(t *testing.T) {
	d := &Decoder{}
	lines := []string{
		Encode("GNGGA,101010.00,3351.5820,S,15112.5760,W,2,14,,58.0,M,,M,,"),
		Encode("GNGSA,A,3,01,02,03,,,,,,,,,,1.8,1.2,1.4,1"),
		Encode("GPGSV,1,1,04,01,40,083,46,02,17,308,41,03,07,344,39,04,22,228,45"),
		Encode("GLGSV,1,1,03,65,40,083,46,66,17,308,41,67,07,344,39"),
		Encode("GNVTG,270.0,T,,M,10.0,N,18.5,K,D"),
		Encode("GNRMC,101010.00,A,3351.5820,S,15112.5760,W,,,161126,,,D"),
	}
	got := feed(d, lines...)
	if len(got) != 1 {
		t.Fatalf("%d fixes", len(got))
	}
	f := got[0]
	if !near(f.Lat, -(33+51.582/60), 1e-9) || !near(f.Lon, -(151+12.576/60), 1e-9) {
		t.Errorf("position %v %v", f.Lat, f.Lon)
	}
	if f.HDOP != 1.2 || !near(f.AccuracyM, 2.4, 1e-9) || f.Quality != 2 {
		t.Errorf("HDOP %v accuracy %v quality %d", f.HDOP, f.AccuracyM, f.Quality)
	}
	if !f.HasSpeed || !near(f.SpeedMS, 18.5/3.6, 1e-9) || !f.HasBearing || f.Bearing != 270 {
		t.Errorf("speed %v bearing %v", f.SpeedMS, f.Bearing)
	}
	if f.InView != 7 || f.Satellites != 14 {
		t.Errorf("in view %d used %d", f.InView, f.Satellites)
	}
}

func TestSimRoundTrip(t *testing.T) {
	s := NewSim(nil)
	for _, p := range [][2]float64{{SimLat, SimLon}, {-33.8688, 151.2093}, {51.4779, -0.0015}, {-0.00001, -179.99999}, {89.99999, 0}} {
		s.Set(p[0], p[1], 12.5, 45)
		d := &Decoder{}
		got := feed(d, s.Epoch(time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC), true)...)
		if len(got) != 1 {
			t.Fatalf("%v: %d fixes", p, len(got))
		}
		f := got[0]
		if !near(f.Lat, p[0], 2e-6) || !near(f.Lon, p[1], 2e-6) || !near(f.SpeedMS, 12.5, 0.01) || f.Bearing != 45 || f.AltitudeM != 44.5 {
			t.Errorf("%v: %+v", p, f)
		}
	}
	s.Set(SimLat, SimLon, 0, 0)
	f := feed(&Decoder{}, s.Epoch(time.Now(), true)...)[0]
	if f.HasBearing || !f.HasSpeed || f.SpeedMS != 0 {
		t.Errorf("standing still: %+v", f)
	}
}

func TestParseSpec(t *testing.T) {
	for v, want := range map[string]Spec{
		"sim":                       {Kind: KindSim},
		"modemmanager":              {Kind: KindModemManager},
		"nmea:/dev/gnss0":           {Kind: KindNMEA, Path: "/dev/gnss0"},
		"nmea:/dev/ttyHS1@115200":   {Kind: KindNMEA, Path: "/dev/ttyHS1", Baud: 115200},
		"fifo:/run/phone/gnss.nmea": {Kind: KindFIFO, Path: "/run/phone/gnss.nmea"},
		"fifo:gnss.nmea":            {Kind: KindFIFO, Path: "gnss.nmea"},
	} {
		got, err := ParseSpec(v)
		if err != nil || got != want || got.String() != v {
			t.Errorf("%s: %+v %v", v, got, err)
		}
	}
	for _, v := range []string{"", "gps", "sim:x", "nmea:", "nmea:dev/gnss0", "nmea:gnss0", "fifo:", "fifo:..", "fifo:a/b", "fifo:/run/../etc/x", "nmea:/dev/ttyS0@1234", "fifo:/run/x@9600", "modemmanager:/x"} {
		if _, err := ParseSpec(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}
