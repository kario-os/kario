package ui

import (
	"math"
	"testing"
)

// FuzzWAC is an emergency alert's warning area from the network (anyone
// with a cell broadcast transmitter): it reads or is refused, every point
// is on the earth, a polygon has three points or more, and testing a
// position against what it read never panics.
func FuzzWAC(f *testing.F) {
	f.Add(EncodeWAC([]AlertArea{{Points: []GeoPoint{{39.9, 116.4}}, RadiusM: 2000}}, 10))
	f.Add(EncodeWAC([]AlertArea{{Points: []GeoPoint{{31.23, 121.48}, {31.25, 121.48}, {31.25, 121.5}}}}, 0))
	f.Add([]byte{0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 4<<10 {
			return
		}
		areas, wait, err := ParseWAC(b)
		if err != nil {
			return
		}
		if wait < 0 || wait > 255 || len(areas) > maxAreas {
			t.Fatalf("wait %d, %d areas", wait, len(areas))
		}
		for _, a := range areas {
			if len(a.Points) == 2 || len(a.Points) == 0 || math.IsNaN(a.RadiusM) || a.RadiusM < 0 {
				t.Fatalf("%+v", a)
			}
			for _, p := range a.Points {
				if p.Lat < -90 || p.Lat > 90 || p.Lon < -180 || p.Lon >= 180 {
					t.Fatalf("point %+v", p)
				}
			}
			_ = a.InArea(10, 20, 100)
		}
	})
}
