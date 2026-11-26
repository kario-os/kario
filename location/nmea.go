// Package location turns a GNSS receiver into fixes for the Shell: an NMEA
// 0183 decoder, and the receivers it reads (a device or a named pipe that
// streams NMEA, ModemManager's location interface, a simulated receiver).
package location

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxSentence bounds a sentence: NMEA 0183 allows 82 characters, receivers
// with NMEA 4.x fields and long GSV lines go somewhat beyond.
const MaxSentence = 256

var (
	ErrFrame    = errors.New("nmea: not a sentence")
	ErrChecksum = errors.New("nmea: bad checksum")
)

// Sentence is one checked sentence: its talker (GP, GL, GA, GB, BD, GN...;
// "P" for a proprietary one), its type and its data fields.
type Sentence struct {
	Talker string
	Type   string
	Fields []string
}

// Checksum is the XOR of the bytes between "$" and "*".
func Checksum(body string) byte {
	var x byte
	for i := 0; i < len(body); i++ {
		x ^= body[i]
	}
	return x
}

// Encode frames a sentence body ("GPGGA,...") with its checksum.
func Encode(body string) string { return fmt.Sprintf("$%s*%02X", body, Checksum(body)) }

// Parse checks a sentence's frame and checksum and splits its fields. A
// sentence without a checksum is refused: on a serial line it is the only
// sign of a corrupted byte.
func Parse(line string) (Sentence, error) {
	line = strings.TrimRight(line, "\r\n")
	if len(line) > MaxSentence || len(line) < 10 || line[0] != '$' {
		return Sentence{}, ErrFrame
	}
	star := len(line) - 3
	if line[star] != '*' {
		return Sentence{}, ErrChecksum
	}
	body := line[1:star]
	for i := 0; i < len(body); i++ {
		if b := body[i]; b < 0x20 || b > 0x7e || b == '$' || b == '*' || b == '!' {
			return Sentence{}, ErrFrame
		}
	}
	sum, err := strconv.ParseUint(line[star+1:], 16, 8)
	if err != nil || byte(sum) != Checksum(body) {
		return Sentence{}, ErrChecksum
	}
	fields := strings.Split(body, ",")
	addr := fields[0]
	for i := 0; i < len(addr); i++ {
		if b := addr[i]; (b < 'A' || b > 'Z') && (b < '0' || b > '9') {
			return Sentence{}, ErrFrame
		}
	}
	switch {
	case len(addr) >= 2 && addr[0] == 'P':
		return Sentence{Talker: "P", Type: addr[1:], Fields: fields[1:]}, nil
	case len(addr) == 5:
		return Sentence{Talker: addr[:2], Type: addr[2:], Fields: fields[1:]}, nil
	}
	return Sentence{}, ErrFrame
}

func (s Sentence) field(i int) string {
	if i < len(s.Fields) {
		return s.Fields[i]
	}
	return ""
}

// decimal reads an unsigned (or, with signed, optionally negative) decimal
// number as receivers write them: digits and at most one point, nothing
// strconv would also take (exponents, "NaN", "Inf", hex).
func decimal(s string, signed bool) (float64, bool) {
	if len(s) == 0 || len(s) > 20 {
		return 0, false
	}
	t := s
	if signed && t[0] == '-' {
		t = t[1:]
	}
	digits, dot := 0, false
	for i := 0; i < len(t); i++ {
		switch c := t[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot:
			dot = true
		default:
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func integer(s string, max int) (int, bool) {
	if len(s) == 0 || len(s) > 6 {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > max || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	return n, true
}

// coordinate reads ddmm.mmmm (latitude) or dddmm.mmmm (longitude) with its
// hemisphere, in signed degrees.
func coordinate(v, hemi string, lon bool) (float64, bool) {
	f, ok := decimal(v, false)
	if !ok {
		return 0, false
	}
	deg := float64(int(f / 100))
	min := f - deg*100
	limit := 90.0
	if lon {
		limit = 180
	}
	d := deg + min/60
	if min >= 60 || d > limit {
		return 0, false
	}
	switch {
	case !lon && hemi == "N", lon && hemi == "E":
	case !lon && hemi == "S", lon && hemi == "W":
		d = -d
	default:
		return 0, false
	}
	return d, true
}

func latLon(s Sentence, at int) (lat, lon float64, ok bool) {
	lat, ok1 := coordinate(s.field(at), s.field(at+1), false)
	lon, ok2 := coordinate(s.field(at+2), s.field(at+3), true)
	return lat, lon, ok1 && ok2
}

// timeOfDay reads hhmmss[.sss] (UTC).
func timeOfDay(v string) (time.Duration, bool) {
	if len(v) < 6 || len(v) > 12 {
		return 0, false
	}
	h, ok1 := integer(v[0:2], 23)
	m, ok2 := integer(v[2:4], 59)
	sec, ok3 := decimal(v[4:], false)
	if !ok1 || !ok2 || !ok3 || sec >= 61 || v[4] == '.' || (len(v) > 6 && v[6] != '.') {
		return 0, false
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second)).Round(time.Millisecond), true
}

// date reads ddmmyy as UTC midnight; receivers count years from 1980.
func date(v string) (time.Time, bool) {
	if len(v) != 6 {
		return time.Time{}, false
	}
	d, ok1 := integer(v[0:2], 31)
	m, ok2 := integer(v[2:4], 12)
	y, ok3 := integer(v[4:6], 99)
	if !ok1 || !ok2 || !ok3 || d == 0 || m == 0 {
		return time.Time{}, false
	}
	if y < 80 {
		y += 2000
	} else {
		y += 1900
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d {
		return time.Time{}, false
	}
	return t, true
}
