package shell

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// reads is the process's read system calls so far (/proc/self/io).
func reads() (int, bool) {
	raw, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return 0, false
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(l, "syscr: "); ok {
			n, err := strconv.Atoi(v)
			return n, err == nil
		}
	}
	return 0, false
}

// settings.get lists every zone, on the event loop: the tz database is read
// once, not on each call (a disk that stalls held Settings' first page).
func TestZonesReadOnce(t *testing.T) {
	c := &Core{}
	c.settings.Zone = "Asia/Shanghai"
	first, _ := c.zoneInForce()
	if again, _ := c.zoneInForce(); again != first {
		t.Error("the zone in force was read again")
	}
	zoneList("en", time.Now())
	before, ok := reads()
	if !ok {
		t.Skip("no /proc/self/io")
	}
	list := zoneList("en", time.Now())
	after, _ := reads()
	if n := after - before; len(list) < 100 || n > len(list)/4 {
		t.Errorf("listing %d zones again took %d reads", len(list), n)
	}
}

// A Shell that boots reads the zones off the event loop, before Settings
// asks for them.
func TestZonesReadAtBoot(t *testing.T) {
	if !alone(t) {
		return
	}
	loopCore(t, t.TempDir())
	deadline := time.Now().Add(10 * time.Second)
	for {
		missing := 0
		for id := range regionData().zoneOf {
			if _, ok := zones.Load(id); !ok {
				missing++
			}
		}
		if missing == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d zones not read 10 s after boot", missing, len(regionData().zoneOf))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A new time zone goes to the apps running (they keep TZ from their start
// otherwise); one that did not change sends nothing.
func TestZoneChangeTellsApps(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		a := &App{M: &Manifest{ID: "com.test.clock", Name: "Clock"}, running: true, ready: true, out: make(chan outbound, 4)}
		c.apps = append(c.apps, a)
		c.settings.Zone = "America/Mexico_City"
		c.applyZone()
		select {
		case o := <-a.out:
			if o.m.T != ui.MsgEvent || o.m.Event != ui.EventTimeZone || o.m.Value != "America/Mexico_City" {
				t.Errorf("sent %+v", o.m)
			}
		default:
			t.Error("no time zone event")
		}
		c.applyZone()
		if len(a.out) != 0 {
			t.Error("an unchanged zone was sent again")
		}
		return nil
	})
}
