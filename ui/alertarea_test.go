package ui

import (
	"encoding/hex"
	"math"
	"testing"
)

func TestWAC(t *testing.T) {
	// A circle of 2 km around Beijing's Tiananmen and a polygon around
	// Shanghai's Bund, with a 10-second wait for a position.
	circle := AlertArea{Points: []GeoPoint{{39.9087, 116.3975}}, RadiusM: 2000}
	poly := AlertArea{Points: []GeoPoint{{31.230, 121.480}, {31.250, 121.480}, {31.250, 121.500}, {31.230, 121.500}}}
	raw := EncodeWAC([]AlertArea{circle, poly}, 10)
	areas, wait, err := ParseWAC(raw)
	if err != nil || wait != 10 || len(areas) != 2 || len(areas[1].Points) != 4 {
		t.Fatalf("%x: %+v %d %v", raw, areas, wait, err)
	}
	c := areas[0]
	if math.Abs(c.Points[0].Lat-39.9087) > 1e-4 || math.Abs(c.Points[0].Lon-116.3975) > 1e-4 || c.RadiusM != 2000 {
		t.Fatalf("circle %+v", c)
	}
	for _, x := range []struct {
		a             AlertArea
		lat, lon, acc float64
		want          bool
		what          string
	}{
		{c, 39.915, 116.40, 0, true, "inside the circle"},
		{c, 39.95, 116.40, 0, false, "4.6 km north"},
		{c, 39.95, 116.40, 3000, true, "4.6 km north, known to 3 km"},
		{areas[1], 31.24, 121.49, 0, true, "inside the polygon"},
		{areas[1], 31.24, 121.52, 0, false, "east of it"},
		{areas[1], 31.24, 121.502, 500, true, "just outside, known to 500 m"},
	} {
		if got := x.a.InArea(x.lat, x.lon, x.acc); got != x.want {
			t.Errorf("%s: %v", x.what, got)
		}
	}
	// A WAC as Android's tests write one: a circle at 37.4,-122.1 of 0.5 km
	// is the bytes below (length 10, then type 3 length 10).
	b, _ := hex.DecodeString("0a00" + "3028" + EncodeWACCircleHex(37.4, -122.1, 0.5))
	if areas, _, err := ParseWAC(b); err != nil || len(areas) != 1 || math.Abs(areas[0].RadiusM-500) > 1 {
		t.Errorf("%+v %v", areas, err)
	}
	for _, bad := range []string{"", "05", "0300302800", "0200f008", "02001008"} {
		b, _ := hex.DecodeString(bad)
		if _, _, err := ParseWAC(b); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}

// EncodeWACCircleHex is a circle's 8 octets, written bit by bit here.
func EncodeWACCircleHex(lat, lon, radiusKm float64) string {
	var w bitWriter
	w.write(uint64(math.Floor((lat+90)/180*(1<<22))), 22)
	w.write(uint64(math.Floor((lon+180)/360*(1<<22))), 22)
	w.write(uint64(radiusKm*64), 20)
	return hex.EncodeToString(w.bytes())
}
