package shell

import (
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// A look at the network the shell's clock read after now (the clock set
// back since: the network's time, a selftest's next part) is stale: a job
// waiting for an unmetered network looks again and runs on the Wi-Fi it
// finds, not two days later when the clock is back there.
func TestJobLooksAgainAfterClockSetBack(t *testing.T) {
	if !alone(t) {
		return
	}
	c, _, _ := netCore(t, "2026-11-20")
	w := newSimWifi()
	w.addNetwork("Kario-Home", "", "")
	if err := w.connect("Kario-Home", ui.WifiArgs{}); err != nil {
		t.Fatal(err)
	}
	c.wifi = w
	c.powerOverride = &Power{Level: 80, Charging: true}
	c.jobs = []*job{{App: "com.phone.store", ID: "updates", Every: 360, Charging: true, Unmetered: true, Next: c.now().Add(-time.Hour)}}
	m := &c.radios.meter
	m.now, m.at = netNow{Kind: netMobile, Metered: true}, c.now().Add(48*time.Hour)
	c.runJobs()
	pumpUntil(t, c, func() bool { return !m.busy })
	c.runJobs()
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 1 {
		t.Fatalf("on Wi-Fi after the clock was set back: %d runs, held: %q", runs, c.JobHeld("com.phone.store", "updates"))
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for then, want := range map[time.Time]time.Duration{
		now.Add(-time.Minute):   time.Minute,
		now:                     0,
		{}:                      now.Sub(time.Time{}),
		now.Add(48 * time.Hour): 1<<63 - 1,
	} {
		if got := ago(now, then); got != want {
			t.Errorf("ago(%v, %v) = %v, want %v", now, then, got, want)
		}
	}
}
