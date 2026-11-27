package shell

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/metered"
	"github.com/kario-os/kario/ui"
)

// meterCore is a shell on the simulated Wi-Fi, connected to ssid, whose
// DHCP server gives hint.
func meterCore(t *testing.T, ssid, hint, addr string) (*Core, *simWifi) {
	c := updateCore(t, "true")
	w := newSimWifi()
	w.addNetwork(ssid, "", hint)
	w.addr = addr
	if err := w.connect(ssid, ui.WifiArgs{}); err != nil {
		t.Fatal(err)
	}
	c.wifi = w
	return c, w
}

func look(t *testing.T, c *Core) netNow {
	t.Helper()
	var n *netNow
	c.lookNetwork(func(x netNow) { n = &x })
	pumpUntil(t, c, func() bool { return n != nil })
	return *n
}

// A phone's hotspot is metered by what its DHCP server or its addresses
// say; the user's choice comes first, and the choice is kept per network
// until the network is forgotten.
func TestMeteredDetection(t *testing.T) {
	if !alone(t) {
		return
	}
	c, w := meterCore(t, "Pixel-Hotspot", ui.MeteredHintDHCP, "192.168.50.23")
	if n := look(t, c); n.Kind != netWifi || !n.Metered || n.Hint != ui.MeteredHintDHCP || unmeteredWhy(n) != "on a metered network (Pixel-Hotspot)" {
		t.Fatalf("%+v %q", n, unmeteredWhy(n))
	}
	if err := c.setMetered("Pixel-Hotspot", ui.MeteredNo); err != nil {
		t.Fatal(err)
	}
	if n := look(t, c); n.Metered || n.Choice != ui.MeteredNo || unmeteredWhy(n) != "" {
		t.Fatalf("set not metered: %+v", n)
	}
	// What Settings shows.
	st, _ := w.state()
	l, _ := w.link()
	c.meteredNets(&st, l, c.meteredChoices())
	for _, n := range st.Networks {
		if n.SSID == "Pixel-Hotspot" && (n.Metered || n.MeteredChoice != ui.MeteredNo || n.MeteredHint != ui.MeteredHintDHCP) {
			t.Fatalf("Settings sees %+v", n)
		}
	}
	if err := c.setMetered("Pixel-Hotspot", "sometimes"); err == nil {
		t.Fatal("a choice that is none")
	}
	if err := c.setMetered("Pixel-Hotspot", ui.MeteredAuto); err != nil || c.settings.WifiMetered["Pixel-Hotspot"] != "" {
		t.Fatalf("back to automatic: %v %v", err, c.settings.WifiMetered)
	}

	// An office network is not metered unless the user says so.
	c2, _ := meterCore(t, "Home-Fibre", "", "192.168.50.23")
	if n := look(t, c2); n.Metered {
		t.Fatalf("an office network: %+v", n)
	}
	_ = c2.setMetered("Home-Fibre", ui.MeteredYes)
	if n := look(t, c2); !n.Metered || n.Choice != ui.MeteredYes {
		t.Fatalf("set metered: %+v", n)
	}
	// An iPhone's hotspot by its addresses.
	c3, _ := meterCore(t, "iPhone", "", "172.20.10.4")
	if n := look(t, c3); !n.Metered || n.Hint != ui.MeteredHintHotspot {
		t.Fatalf("an iPhone's hotspot: %+v", n)
	}
	// No Wi-Fi: mobile data or nothing, never unmetered.
	c3.wifi.(*simWifi).setEnabled(false)
	if n := look(t, c3); n.Kind == netWifi || unmeteredWhy(n) != "not on Wi-Fi" {
		t.Fatalf("Wi-Fi off: %+v", n)
	}
}

// Automatic system updates wait on a metered network; the user's choice
// lets them go.
func TestAutoUpdateMetered(t *testing.T) {
	if !alone(t) {
		return
	}
	c, _ := meterCore(t, "Pixel-Hotspot", ui.MeteredHintDHCP, "192.168.50.23")
	why := func() string {
		var w *string
		c.autoConditions(func(s string) { w = &s })
		pumpUntil(t, c, func() bool { return w != nil })
		return *w
	}
	if got := why(); got != "on a metered network (Pixel-Hotspot)" {
		t.Fatalf("on a phone's hotspot: %q", got)
	}
	_ = c.setMetered("Pixel-Hotspot", ui.MeteredNo)
	if got := why(); got != "" {
		t.Fatalf("set not metered: %q", got)
	}
	c.wifi.(*simWifi).setEnabled(false)
	if got := why(); got != "not on Wi-Fi" {
		t.Fatalf("without Wi-Fi: %q", got)
	}
}

// ifnamed is the simulated Wi-Fi with the station's interface named, as
// iwd's link has it, so that the shell asks the network's DHCP server.
type ifnamed struct{ *simWifi }

func (w ifnamed) link() (wifiLink, error) {
	l, err := w.simWifi.link()
	if l.ssid != "" {
		l.ifname = "wlan0"
	}
	return l, err
}

// A DHCP question that goes unanswered (a packet lost) leaves the network
// unmetered only until it is asked again: a phone's hotspot whose first
// answer was lost counts as metered once its server answers, and an
// automatic update waits on it. An answer is kept for the address.
func TestMeterProbeAskedAgain(t *testing.T) {
	if !alone(t) {
		return
	}
	c, w := meterCore(t, "Pixel-Hotspot", "", "192.168.50.23")
	c.wifi = ifnamed{w}
	var asked atomic.Int32
	c.radios.meter.probe = func(ifname string, addr net.IP, _ time.Duration) ([]byte, error) {
		if ifname != "wlan0" || !addr.Equal(net.IPv4(192, 168, 50, 23)) {
			t.Errorf("asked on %s for %s", ifname, addr)
		}
		if asked.Add(1) == 1 {
			return nil, errors.New("metered: no answer from a DHCP server")
		}
		return []byte("\x01\x0f" + metered.VendorMetered), nil
	}
	if n := look(t, c); n.Metered || asked.Load() != 1 {
		t.Fatalf("no answer: %+v, asked %d times", n, asked.Load())
	}
	if n := look(t, c); n.Metered || asked.Load() != 1 {
		t.Fatalf("asked again at once: %+v, %d times", n, asked.Load())
	}
	m := &c.radios.meter
	m.mu.Lock()
	for k, h := range m.hints {
		h.again = time.Now().Add(-time.Second)
		m.hints[k] = h
	}
	m.mu.Unlock()
	if n := look(t, c); !n.Metered || n.Hint != ui.MeteredHintDHCP || asked.Load() != 2 {
		t.Fatalf("asked again: %+v, %d times", n, asked.Load())
	}
	var why *string
	c.autoConditions(func(s string) { why = &s })
	pumpUntil(t, c, func() bool { return why != nil })
	if *why != "on a metered network (Pixel-Hotspot)" || asked.Load() != 2 {
		t.Fatalf("an automatic update on the hotspot: %q, asked %d times", *why, asked.Load())
	}
}

// A job that asks for an unmetered network waits on a metered one and on
// mobile data, and runs on unmetered Wi-Fi; the others run as before.
func TestUnmeteredJob(t *testing.T) {
	if !alone(t) {
		return
	}
	c, _ := meterCore(t, "Pixel-Hotspot", ui.MeteredHintDHCP, "192.168.50.23")
	past := c.now().Add(-2 * time.Hour)
	c.jobs = []*job{{App: "com.phone.store", ID: "updates", Every: 60, Unmetered: true, Next: past},
		{App: "com.phone.weather", ID: "refresh", Every: 60, Next: past}}
	m := &c.radios.meter
	for _, tc := range []struct {
		now  netNow
		runs int
	}{
		{netNow{Kind: netWifi, SSID: "Pixel-Hotspot", Metered: true}, 0},
		{netNow{Kind: netMobile, Metered: true}, 0},
		{netNow{Kind: netWifi, SSID: "Home-Fibre"}, 1},
	} {
		m.now, m.at = tc.now, c.now()
		c.jobs[0].Next, c.jobs[1].Next = past, past
		before, _ := c.JobRuns("com.phone.weather", "refresh")
		c.runJobs()
		if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != tc.runs {
			t.Fatalf("%+v: the unmetered job ran %d times", tc.now, runs)
		}
		if after, _ := c.JobRuns("com.phone.weather", "refresh"); after != before+1 {
			t.Fatalf("%+v: the other job did not run", tc.now)
		}
	}
	// A look that is old starts another, off the loop.
	m.at = c.now().Add(-time.Hour)
	c.jobs[0].Next = past
	c.runJobs()
	pumpUntil(t, c, func() bool { return !m.busy })
	if m.now.Kind != netWifi || !m.now.Metered || m.now.SSID != "Pixel-Hotspot" {
		t.Fatalf("the new look: %+v", m.now)
	}
	if err := c.scheduleJob(&App{M: &Manifest{ID: "com.phone.store"}}, ui.JobArgs{ID: "updates", EveryMin: 60, Unmetered: true}); err != nil {
		t.Fatal(err)
	}
	if j := c.jobs[len(c.jobs)-1]; !j.Unmetered {
		t.Fatal("the job forgot it wants an unmetered network")
	}
}

// jobEvents are the job and job.stop events queued for the app.
func jobEvents(a *App) []string {
	var out []string
	for {
		m, ok := next(a)
		if !ok {
			return out
		}
		if m.T == ui.MsgEvent && (m.Event == ui.EventJob || m.Event == ui.EventJobStop) {
			out = append(out, m.Event+" "+m.Value)
		}
	}
}

// A job that asked for an unmetered network and runs when the network
// becomes metered is told to stop, with the reason; its wake ends after
// the grace, it does not run again while the network is metered, and
// runs again once it is not, keeping its schedule and its conditions. A
// job whose wake is over, or one that did not ask, is not stopped.
func TestUnmeteredJobStops(t *testing.T) {
	if !alone(t) {
		return
	}
	c, w := meterCore(t, "Home-Fibre", "", "192.168.50.23")
	store := &App{M: &Manifest{ID: "com.phone.store"}, running: true, ready: true, out: make(chan outbound, 64)}
	weather := &App{M: &Manifest{ID: "com.phone.weather"}, running: true, ready: true, out: make(chan outbound, 64)}
	c.apps = []*App{store, weather}
	past := c.now().Add(-2 * time.Hour)
	c.jobs = []*job{{App: "com.phone.store", ID: "updates", Every: 60, Unmetered: true, Next: past},
		{App: "com.phone.weather", ID: "refresh", Every: 60, Next: past}}
	look(t, c)
	c.runJobs()
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 1 {
		t.Fatalf("the job did not run on unmetered Wi-Fi: %d", runs)
	}
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != "job updates" {
		t.Fatalf("the store got %q", ev)
	}
	if ev := jobEvents(weather); len(ev) != 1 || ev[0] != "job refresh" {
		t.Fatalf("the weather got %q", ev)
	}

	if err := c.setMetered("Home-Fibre", ui.MeteredYes); err != nil {
		t.Fatal(err)
	}
	c.runJobs()
	pumpUntil(t, c, func() bool { return !c.radios.meter.jobs.looking })
	if c.JobStops("com.phone.store", "updates") != 1 || c.JobStops("com.phone.weather", "refresh") != 0 {
		t.Fatalf("stops: store %d, weather %d", c.JobStops("com.phone.store", "updates"), c.JobStops("com.phone.weather", "refresh"))
	}
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != `job.stop {"id":"updates","reason":"metered"}` {
		t.Fatalf("the store got %q", ev)
	}
	if len(jobEvents(weather)) != 0 {
		t.Fatal("the weather's job was stopped")
	}
	c.runJobs()
	if store.bgSince.Before(time.Now().Add(-c.freezeAfter())) {
		t.Fatal("the job's wake ended before the grace")
	}
	j := c.jobs[0]
	j.stopAt = time.Now().Add(-jobStopGrace)
	c.runJobs()
	if !j.until.IsZero() || store.bgSince.After(time.Now().Add(-c.freezeAfter())) {
		t.Fatalf("the job's wake did not end: until %v, the app freezes after %v", j.until, store.bgSince)
	}
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 1 || !j.Retry {
		t.Fatalf("ran again on a metered network: %d runs, retry %v", runs, j.Retry)
	}

	if err := c.setMetered("Home-Fibre", ui.MeteredNo); err != nil {
		t.Fatal(err)
	}
	c.runJobs()
	pumpUntil(t, c, func() bool { return !c.radios.meter.busy })
	c.runJobs()
	if runs, _ := c.JobRuns("com.phone.store", "updates"); runs != 2 || j.Retry || !j.Unmetered || j.Next.Sub(c.now()) < 59*time.Minute {
		t.Fatalf("back on unmetered Wi-Fi: %d runs, retry %v, %+v", runs, j.Retry, j)
	}
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != "job updates" {
		t.Fatalf("the store got %q", ev)
	}

	// No network at all: stopped as offline. A run whose wake is over is
	// not stopped.
	w.setEnabled(false)
	c.radios.meter.jobs.at = time.Time{}
	c.runJobs()
	pumpUntil(t, c, func() bool { return !c.radios.meter.jobs.looking })
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != `job.stop {"id":"updates","reason":"offline"}` {
		t.Fatalf("offline: the store got %q", ev)
	}
	j.until, j.stopAt, j.Retry = time.Now().Add(-time.Second), time.Time{}, false
	c.radios.meter.jobs.at = time.Time{}
	c.runJobs()
	if c.radios.meter.jobs.looking || j.Stops != 2 {
		t.Fatalf("a run that is over watched or stopped: %+v", j)
	}
}
