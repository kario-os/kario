package ui

import (
	"errors"
	"math"
)

// Warning area coordinates (WAC) as WEA 3.0 sends them with a CMAS or ETWS
// alert (ATIS-0700041 §5, carried as 3GPP TS 23.041 says), read as
// Android's cell broadcast service reads them: the length of what follows
// in two octets, little-endian, then shapes. A shape starts with 4 bits of
// type, 10 bits of length in octets (its header included) and 2 spare
// bits: type 1 the longest wait for a position (8 bits of seconds), 2 a
// polygon (coordinates of 44 bits), 3 a circle (a coordinate and 20 bits
// of radius in 1/64 km). A coordinate is 22 bits of latitude,
// floor((lat+90)/180 * 2^22), and 22 of longitude, floor((lon+180)/360 *
// 2^22).
const (
	wacMaxWait = 1
	wacPolygon = 2
	wacCircle  = 3
	// maxAreas bounds the shapes of one alert.
	maxAreas = 16
)

// ParseWAC reads an alert's warning area coordinates: its shapes, and the
// longest the phone should wait for a position (0: not said).
func ParseWAC(b []byte) ([]AlertArea, int, error) {
	if len(b) < 2 {
		return nil, 0, errors.New("wac: too short")
	}
	n := int(b[0]) | int(b[1])<<8
	if n > len(b)-2 {
		return nil, 0, errors.New("wac: truncated")
	}
	b = b[2 : 2+n]
	var areas []AlertArea
	wait := 0
	for len(b) > 0 {
		if len(b) < 2 {
			return nil, 0, errors.New("wac: truncated shape")
		}
		typ := int(b[0] >> 4)
		size := int(b[0]&0x0f)<<6 | int(b[1]>>2)
		if size < 2 || size > len(b) {
			return nil, 0, errors.New("wac: bad shape length")
		}
		body := bits{b: b[2:size]}
		b = b[size:]
		switch typ {
		case wacMaxWait:
			if len(body.b) < 1 {
				return nil, 0, errors.New("wac: short wait")
			}
			wait = int(body.read(8))
		case wacPolygon:
			count := len(body.b) * 8 / 44
			if count < 3 {
				return nil, 0, errors.New("wac: a polygon of fewer than three points")
			}
			a := AlertArea{}
			for i := 0; i < count; i++ {
				a.Points = append(a.Points, body.point())
			}
			areas = append(areas, a)
		case wacCircle:
			if len(body.b) < 8 {
				return nil, 0, errors.New("wac: short circle")
			}
			c := body.point()
			areas = append(areas, AlertArea{Points: []GeoPoint{c}, RadiusM: float64(body.read(20)) / 64 * 1000})
		default:
			return nil, 0, errors.New("wac: unknown shape")
		}
		if len(areas) > maxAreas {
			return nil, 0, errors.New("wac: too many shapes")
		}
	}
	return areas, wait, nil
}

// EncodeWAC writes shapes (and the longest wait, 0 for none) as ParseWAC
// reads them: the simulated network's alerts and tests.
func EncodeWAC(areas []AlertArea, wait int) []byte {
	var out []byte
	shape := func(typ int, body []byte) {
		size := len(body) + 2
		out = append(out, byte(typ<<4)|byte(size>>6), byte(size<<2))
		out = append(out, body...)
	}
	if wait > 0 {
		shape(wacMaxWait, []byte{byte(min(wait, 255))})
	}
	for _, a := range areas {
		var w bitWriter
		if a.RadiusM > 0 && len(a.Points) == 1 {
			w.point(a.Points[0])
			w.write(uint64(math.Min(a.RadiusM/1000*64, 1<<20-1)), 20)
			shape(wacCircle, w.bytes())
			continue
		}
		for _, p := range a.Points {
			w.point(p)
		}
		shape(wacPolygon, w.bytes())
	}
	return append([]byte{byte(len(out)), byte(len(out) >> 8)}, out...)
}

type bits struct {
	b   []byte
	pos int
}

func (r *bits) read(n int) uint64 {
	var v uint64
	for ; n > 0; n-- {
		bit := 0
		if r.pos/8 < len(r.b) && r.b[r.pos/8]&(0x80>>(r.pos%8)) != 0 {
			bit = 1
		}
		v = v<<1 | uint64(bit)
		r.pos++
	}
	return v
}

func (r *bits) point() GeoPoint {
	lat := float64(r.read(22))*180/(1<<22) - 90
	lon := float64(r.read(22))*360/(1<<22) - 180
	return GeoPoint{Lat: lat, Lon: lon}
}

type bitWriter struct {
	b   []byte
	pos int
}

func (w *bitWriter) write(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.pos/8 == len(w.b) {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 == 1 {
			w.b[w.pos/8] |= 0x80 >> (w.pos % 8)
		}
		w.pos++
	}
}

func (w *bitWriter) point(p GeoPoint) {
	lat := math.Floor((math.Max(-90, math.Min(90, p.Lat)) + 90) / 180 * (1 << 22))
	lon := math.Floor(math.Mod(p.Lon+540, 360) / 360 * (1 << 22))
	w.write(uint64(math.Min(lat, 1<<22-1)), 22)
	w.write(uint64(math.Min(lon, 1<<22-1)), 22)
}

func (w *bitWriter) bytes() []byte { return w.b }

// InArea says whether a position known to within accM metres may be in
// the area: inside it, or that close to its edge.
func (a AlertArea) InArea(lat, lon, accM float64) bool {
	switch {
	case len(a.Points) == 1:
		return earthDistanceM(lat, lon, a.Points[0].Lat, a.Points[0].Lon) <= a.RadiusM+accM
	case len(a.Points) < 3:
		return false
	}
	// Equirectangular metres around the position: alert areas are a few
	// kilometres to a few hundred across.
	const r = 6371008.8
	k := math.Cos(lat * math.Pi / 180)
	xy := func(p GeoPoint) (float64, float64) {
		dLon := math.Mod(p.Lon-lon+540, 360) - 180
		return dLon * math.Pi / 180 * r * k, (p.Lat - lat) * math.Pi / 180 * r
	}
	inside := false
	best := math.Inf(1)
	for i := range a.Points {
		x1, y1 := xy(a.Points[i])
		x2, y2 := xy(a.Points[(i+1)%len(a.Points)])
		if (y1 > 0) != (y2 > 0) && x1+(0-y1)*(x2-x1)/(y2-y1) > 0 {
			inside = !inside
		}
		best = math.Min(best, segmentDistance(x1, y1, x2, y2))
	}
	return inside || best <= accM
}

// segmentDistance is how far the origin is from the segment.
func segmentDistance(x1, y1, x2, y2 float64) float64 {
	dx, dy := x2-x1, y2-y1
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = math.Max(0, math.Min(1, -(x1*dx+y1*dy)/l))
	}
	return math.Hypot(x1+t*dx, y1+t*dy)
}

func earthDistanceM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371008.8
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}
