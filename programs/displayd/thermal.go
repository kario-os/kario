package displayd

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// thermal caps the rate while the phone is hot: the hottest of the zones
// of the given types (the board's skin or battery sensors; /sys/class/
// thermal) at capC or above, until it is coolBy degrees under it. No
// board names its zones yet, so phones have no thermal cap until one does.
type thermal struct {
	root  string
	types []string
	capC  float64
	hot   bool
	// sim: the zone is simulated (no types given); hot only by sim.thermal.
	sim bool
}

const coolBy = 3

func newThermal(root, types string, capC float64) *thermal {
	t := &thermal{root: root, capC: capC}
	for _, s := range strings.Split(types, ",") {
		if s = strings.TrimSpace(s); s != "" {
			t.types = append(t.types, s)
		}
	}
	t.sim = len(t.types) == 0
	return t
}

// hottest is the highest temperature of the zones named, in degrees; ok
// is false when none reads.
func (t *thermal) hottest() (float64, bool) {
	zones, _ := filepath.Glob(filepath.Join(t.root, "thermal_zone*"))
	best, ok := 0.0, false
	for _, z := range zones {
		typ, err := os.ReadFile(filepath.Join(z, "type"))
		if err != nil || !slices.Contains(t.types, strings.TrimSpace(string(typ))) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(z, "temp"))
		if err != nil {
			continue
		}
		milli, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			continue
		}
		if c := float64(milli) / 1000; !ok || c > best {
			best, ok = c, true
		}
	}
	return best, ok
}

// check reads the zones; true when the phone went hot or cooled down.
func (t *thermal) check() bool {
	if t.sim {
		return false
	}
	c, ok := t.hottest()
	hot := t.hot
	switch {
	case !ok:
		hot = false
	case c >= t.capC:
		hot = true
	case c < t.capC-coolBy:
		hot = false
	}
	changed := hot != t.hot
	t.hot = hot
	return changed
}
