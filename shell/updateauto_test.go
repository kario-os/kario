package shell

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

func uiAlarm(at time.Time) ui.Alarm { return ui.Alarm{Time: at.Format("15:04"), On: true} }

// updateCore is a shell with A/B slots (dir), RAUC as rauc (a script) and
// the clock, battery and network the test sets.
func updateCore(t *testing.T, rauc string) *Core {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "shell"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644)
	c := &Core{cfg: Config{StateDir: dir}, log: log.New(io.Discard, "", 0), ev: make(chan func(), 64)}
	c.update.slots, c.update.rauc, c.update.deltaLog = dir, rauc, filepath.Join(dir, "phone-ota-delta.log")
	c.update.stopFile = filepath.Join(dir, "phone-ota-delta.stop")
	c.powerOverride = &Power{Level: 82}
	return c
}

// pumpUntil runs the shell's posted work until done says so.
func pumpUntil(t *testing.T, c *Core, done func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !done() {
		select {
		case f := <-c.ev:
			f()
		case <-deadline:
			t.Fatalf("still %s (%s)", c.update.state, c.update.err)
		}
	}
}

// fakeRauc is rauc install as a script: it logs each URL to calls and runs
// the case for it (the first time; again for the times after).
func fakeRauc(t *testing.T, dir, first, again string) (rauc, calls string) {
	rauc, calls = filepath.Join(dir, "rauc"), filepath.Join(dir, "calls")
	first, again = first+"\n:", again+"\n:"
	script := "#!/bin/sh\necho \"$2\" >> " + calls + "\n" +
		"if [ \"$(grep -c . " + calls + ")\" = 1 ]; then\n" + first + "\nfi\n" + again + "\n" +
		"printf 'slot=b\\ngood=a\\ntries=3\\nbooted=a\\n' > " + filepath.Join(dir, "boot") + "\n"
	_ = os.WriteFile(rauc, []byte(script), 0o755)
	return rauc, calls
}

func installOffer(t *testing.T, c *Core, delta string) (*UpdateStatus, error) {
	t.Helper()
	c.update.offer = &updateOffer{Version: "0.8.0", URL: "http://x/full.raucb"}
	if delta != "" {
		c.update.offer.Delta = &updateDelta{From: "0.7.0", URL: "http://x/" + delta}
	}
	var st *UpdateStatus
	var err error
	replied := false
	c.updateInstall(func(v any, e error) {
		s, _ := v.(UpdateStatus)
		st, err, replied = &s, e, true
	})
	pumpUntil(t, c, func() bool { return replied || !c.update.retryAt.IsZero() })
	return st, err
}

// A download that stops is tried again 30 s, 2 min and 8 min later (the
// shell's clock); then the install fails. The status says which attempt
// waits and when.
func TestInstallRetry(t *testing.T) {
	dir := t.TempDir()
	rauc, calls := fakeRauc(t, dir, "", "[ \"$(grep -c . "+filepath.Join(dir, "calls")+")\" -le 2 ] && { echo \"Failed to download bundle $2: Connection timed out\" >&2; exit 1; }")
	c := updateCore(t, rauc)
	c.update.slots = dir
	_ = os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644)
	if st, _ := installOffer(t, c, ""); st != nil {
		t.Fatalf("answered before the retries: %+v", st)
	}
	for i, wait := range []time.Duration{30 * time.Second, 2 * time.Minute} {
		st := c.updateStatus()
		if st.State != "installing" || st.Retry == nil || st.Retry.Attempt != i+1 || st.Retry.Of != updateRetries {
			t.Fatalf("after failure %d: %+v %+v", i+1, st, st.Retry)
		}
		if got := c.update.retryAt.Sub(c.now()).Round(time.Second); got != wait {
			t.Fatalf("retry %d in %s, want %s", i+1, got, wait)
		}
		c.retryInstall()
		if c.update.retryAt.IsZero() {
			t.Fatal("retried before its time")
		}
		c.SetClockSkew(wait)
		c.retryInstall()
		pumpUntil(t, c, func() bool { return c.update.state != "installing" || !c.update.retryAt.IsZero() })
	}
	if c.update.state != "installed" {
		t.Fatalf("after two retries: %s %s", c.update.state, c.update.err)
	}
	if raw, _ := os.ReadFile(calls); strings.Count(string(raw), "full.raucb") != 3 {
		t.Fatalf("rauc ran for %q", raw)
	}

	// Always failing: three retries, then failed.
	dir2 := t.TempDir()
	rauc2, _ := fakeRauc(t, dir2, "", "echo 'Failed to download bundle: Could not resolve host' >&2; exit 1")
	c2 := updateCore(t, rauc2)
	_, _ = installOffer(t, c2, "")
	for c2.update.state == "installing" {
		c2.SetClockSkew(time.Hour)
		c2.retryInstall()
		pumpUntil(t, c2, func() bool { return c2.update.state != "installing" || !c2.update.retryAt.IsZero() })
	}
	if c2.update.state != "failed" || c2.update.retries != updateRetries || c2.update.err != tr.T("Downloading the update failed") {
		t.Fatalf("gave up as %s after %d retries: %s", c2.update.state, c2.update.retries, c2.update.err)
	}
}

// An incremental bundle whose payload stopped arriving (its applier says
// so in the hook's log) is tried again as the incremental bundle, which
// resumes; one that failed for another reason gives way to the full one.
func TestInstallDeltaRetry(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "phone-ota-delta.log")
	rauc, calls := fakeRauc(t, dir,
		"echo 'phone-ota-delta: the payload stopped arriving; installing it again resumes at operation 40' > "+log+"; echo 'failed to run slot hook: Child process exited with code 1' >&2; exit 1",
		"echo 'phone-ota-delta: resumed at operation 40 of 83: 1000 bytes of payload data not read' > "+log)
	c := updateCore(t, rauc)
	c.update.slots, c.update.deltaLog = dir, log
	_ = os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644)
	_, _ = installOffer(t, c, "delta.raucb")
	if c.update.retryAt.IsZero() || len(c.update.plan) != 2 {
		t.Fatalf("not retried as the incremental bundle: %s %v", c.update.state, c.update.plan)
	}
	c.SetClockSkew(time.Minute)
	c.retryInstall()
	pumpUntil(t, c, func() bool { return c.update.state == "installed" })
	if raw, _ := os.ReadFile(calls); string(raw) != "http://x/delta.raucb\nhttp://x/delta.raucb\n" {
		t.Fatalf("rauc ran for %q", raw)
	}
}

// A retry that comes while the network is still away fails before the
// hook runs, RAUC saying only that the install failed: that is a stopped
// download too, so the incremental bundle is tried again (and resumes)
// rather than given up for the full one.
func TestInstallDeltaRetryStillDown(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "phone-ota-delta.log")
	n := "$(grep -c . " + filepath.Join(dir, "calls") + ")"
	rauc, calls := fakeRauc(t, dir,
		"echo 'phone-ota-delta: the payload stopped arriving; installing it again resumes at operation 40' > "+log+"; echo 'failed to run slot hook: Child process exited with code 1' >&2; exit 1",
		"[ "+n+" = 2 ] && { echo \"Installing \\`$2\\` failed\" >&2; exit 1; }; echo 'phone-ota-delta: resumed at operation 40 of 83: 1000 bytes of payload data not read' > "+log)
	c := updateCore(t, rauc)
	c.update.slots, c.update.deltaLog = dir, log
	_ = os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644)
	_, _ = installOffer(t, c, "delta.raucb")
	for range 2 {
		if c.update.retryAt.IsZero() || len(c.update.plan) != 2 {
			t.Fatalf("not retried as the incremental bundle: %s %v", c.update.state, c.update.plan)
		}
		c.SetClockSkew(time.Hour)
		c.retryInstall()
		pumpUntil(t, c, func() bool { return c.update.state != "installing" || !c.update.retryAt.IsZero() })
	}
	if raw, _ := os.ReadFile(calls); c.update.state != "installed" || string(raw) != "http://x/delta.raucb\nhttp://x/delta.raucb\nhttp://x/delta.raucb\n" {
		t.Fatalf("%s: rauc ran for %q", c.update.state, raw)
	}
	// Not on a first attempt: an incremental bundle that fails at once for
	// no named reason gives way to the full one.
	dir2 := t.TempDir()
	rauc2, calls2 := fakeRauc(t, dir2, "echo \"Installing \\`$2\\` failed\" >&2; exit 1", "")
	c2 := updateCore(t, rauc2)
	c2.update.slots, c2.update.deltaLog = dir2, filepath.Join(dir2, "phone-ota-delta.log")
	_ = os.WriteFile(filepath.Join(dir2, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644)
	_, _ = installOffer(t, c2, "delta.raucb")
	pumpUntil(t, c2, func() bool { return c2.update.state != "installing" })
	if raw, _ := os.ReadFile(calls2); string(raw) != "http://x/delta.raucb\nhttp://x/full.raucb\n" {
		t.Fatalf("rauc ran for %q", raw)
	}
}

func TestDownloadStopped(t *testing.T) {
	for out, want := range map[string]bool{
		"Failed to download bundle https://u.example/phone.raucb: Couldn't connect to server": true,
		"nbd: request timed out": true,
		"LastError: Failed to stream bundle http://10.0.2.2:8789/back.raucb: Operation not permitted":           true,
		"Installing `https://u.example/phone.raucb` failed: signature verification failed: certificate unknown": false,
		"Installing `http://10.0.2.2:8789/delta.raucb` failed: failed to run slot hook":                         false,
		"Compatible mismatch: Expected 'kario-qemuarm64' but bundle manifest has 'kario-other'":                 false,
		"no space left on device": false,
	} {
		if got := downloadStopped(out, nil); got != want {
			t.Errorf("%q: %v", out, got)
		}
	}
	if !downloadStopped("failed to run slot hook", []byte("phone-ota-delta: the payload stopped arriving; installing it again resumes at operation 3\n")) {
		t.Error("the applier's word not taken")
	}
}

// rauc install's output reaches the shell line by line as it comes,
// progress redrawn in place included; an incremental install's status has
// the applier's operations.
func TestInstallProgress(t *testing.T) {
	dir := t.TempDir()
	rauc := filepath.Join(dir, "rauc")
	_ = os.WriteFile(rauc, []byte("#!/bin/sh\nprintf '  0%% Installing\\n 40%% Copying image to rootfs.1\\r 60%% Copying image to rootfs.1\\n'\necho done >&2\n"), 0o755)
	var lines []string
	out, err := raucInstall(rauc, "http://x/full.raucb", func(l string) { lines = append(lines, l) })
	if err != nil || len(lines) != 4 || lines[2] != " 60% Copying image to rootfs.1" || !strings.Contains(out, "done") {
		t.Fatalf("%q %v: %q", lines, err, out)
	}
	c := updateCore(t, "true")
	c.update.state, c.update.plan, c.update.percent = "installing", []string{"d", "f"}, 40
	_ = os.WriteFile(c.update.deltaLog, []byte("phone-ota-delta: resumed at operation 40 of 83: 9 bytes of payload data not read\n"+
		"phone-ota-delta: progress: operation 52 of 83, 1048576 bytes written\n"), 0o644)
	if p := c.updateStatus().Progress; p == nil || *p != (UpdateProgress{Percent: 40, Done: 52, Total: 83, Resumed: 40}) {
		t.Fatalf("progress %+v", p)
	}
	c.update.plan = []string{"f"}
	if p := c.updateStatus().Progress; p == nil || p.Total != 0 || p.Percent != 40 {
		t.Fatalf("a full bundle's progress %+v", p)
	}
}

// The background install's conditions: no call, the battery at 50 % or
// charging, Wi-Fi.
func TestAutoConditions(t *testing.T) {
	c := updateCore(t, "true")
	wifi := true
	c.update.unmetered = func(then func(bool)) { then(wifi) }
	why := func() string {
		var w string
		c.autoConditions(func(s string) { w = s })
		return w
	}
	for _, tc := range []struct {
		pw   Power
		wifi bool
		ok   bool
	}{
		{Power{Level: 82}, true, true},
		{Power{Level: 49}, true, false},
		{Power{Level: 10, Charging: true}, true, true},
		{Power{Level: 82}, false, false},
	} {
		c.powerOverride, wifi = &tc.pw, tc.wifi
		if got := why(); (got == "") != tc.ok {
			t.Errorf("%+v, Wi-Fi %v: %q", tc.pw, tc.wifi, got)
		}
	}
	c.powerOverride = &Power{Level: 82}
	c.calls = map[string]*callInfo{"x": {}}
	if why() != "in a call" {
		t.Error("installs during a call")
	}
}

// Once a day, on Wi-Fi with the battery fine, the shell checks the update
// server and installs in the background; the user picks tonight, and the
// phone restarts at night with the screen off, no call and no alarm soon.
func TestAutoUpdate(t *testing.T) {
	checks := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/phone.raucb" {
			fmt.Fprint(w, "the bundle")
			return
		}
		checks++
		fmt.Fprint(w, `{"version":"9.0.0","url":"phone.raucb"}`)
	}))
	defer srv.Close()
	c := updateCore(t, "true")
	c.cfg.UpdateURL = srv.URL
	var urls []string
	var got string
	c.update.install = func(url string, line func(string)) (string, error) {
		urls = append(urls, url)
		if resp, err := http.Get(url); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			got = string(b)
		}
		line(" 50% Copying image to rootfs.1")
		return "", os.WriteFile(filepath.Join(c.update.slots, "boot"), []byte("slot=b\ngood=a\ntries=3\nbooted=a\n"), 0o644)
	}
	restarts := 0
	c.update.restart = func() { restarts++ }
	wifi := false
	c.update.unmetered = func(then func(bool)) { then(wifi) }

	c.updateTick()
	if checks != 0 || c.update.auto.busy {
		t.Fatal("checked with automatic updates off")
	}
	c.settings.AutoSystemUpdate = true
	c.updateTick()
	pumpUntil(t, c, func() bool { return !c.update.auto.busy })
	if checks != 0 || !strings.Contains(c.update.auto.last, "not on Wi-Fi") {
		t.Fatalf("on mobile data: %d checks, %q", checks, c.update.auto.last)
	}
	wifi = true
	c.updateTick()
	if c.update.auto.busy {
		t.Fatal("tried again within the hour")
	}
	c.SetClockSkew(autoUpdateRetry)
	c.updateTick()
	pumpUntil(t, c, func() bool { return c.update.state == "installed" })
	// Through the shell's proxy on the loopback, which an automatic
	// install's stop cuts (updateproxy.go).
	if checks != 1 || len(urls) != 1 || !strings.HasPrefix(urls[0], "http://127.0.0.1:") || path.Base(urls[0]) != "phone.raucb" || got != "the bundle" || !c.updateStatus().Automatic {
		t.Fatalf("%d checks, installed %v (%q)", checks, urls, got)
	}
	c.updateTick()
	c.SetClockSkew(2 * autoUpdateEvery)
	c.updateTick()
	if checks != 1 {
		t.Fatal("checked again with an update waiting for its restart")
	}

	c.updateNotifAction("tonight")
	if !c.updateStatus().Tonight {
		t.Fatal("tonight not kept")
	}
	night := func(hhmm string) {
		now := c.now()
		at, _ := time.Parse("15:04", hhmm)
		next := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		c.SetClockSkew(next.Sub(now))
		c.updateTick()
	}
	c.screen.off = true
	night("23:00")
	c.screen.off = false
	night("03:00")
	if restarts != 0 {
		t.Fatal("restarted in the evening or with the screen on")
	}
	c.screen.off = true
	c.alarms = &alarmStore{path: filepath.Join(t.TempDir(), "alarms.json")}
	_, _ = c.alarms.set("com.phone.clock", uiAlarm(c.now().Add(30*time.Minute)), c.now())
	c.updateTick()
	if restarts != 0 {
		t.Fatal("restarted with an alarm due in 30 minutes")
	}
	c.alarms.list = nil
	c.updateTick()
	if restarts != 1 || c.update.auto.Tonight {
		t.Fatalf("at night, not in use: %d restarts", restarts)
	}
}

// A system on trial (booted, not yet marked good) gets no automatic
// update: it would be written over the slot the phone falls back to.
func TestAutoUpdateWaitsForGood(t *testing.T) {
	checks := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks++
		fmt.Fprint(w, `{"version":"9.0.0","url":"phone.raucb"}`)
	}))
	defer srv.Close()
	c := updateCore(t, "true")
	c.cfg.UpdateURL, c.settings.AutoSystemUpdate = srv.URL, true
	c.update.unmetered = func(then func(bool)) { then(true) }
	c.update.install = func(string, func(string)) (string, error) { return "", nil }
	_ = os.WriteFile(filepath.Join(c.update.slots, "boot"), []byte("slot=b\ngood=a\ntries=2\nbooted=b\n"), 0o644)
	c.updateTick()
	pumpUntil(t, c, func() bool { return !c.update.auto.busy })
	if checks != 0 || c.update.state != "idle" {
		t.Fatalf("on trial: %d checks, %s", checks, c.update.state)
	}
	_ = os.WriteFile(filepath.Join(c.update.slots, "boot"), []byte("slot=b\ngood=b\ntries=0\nbooted=b\n"), 0o644)
	c.SetClockSkew(autoUpdateRetry)
	c.updateTick()
	pumpUntil(t, c, func() bool { return c.update.state == "installed" })
	if checks != 1 {
		t.Fatalf("marked good: %d checks", checks)
	}
}

// Tonight is forgotten once the phone restarted some other way.
func TestTonightForgotten(t *testing.T) {
	c := updateCore(t, "true")
	c.update.auto.Tonight = true
	c.saveAutoUpdate()
	c.update.auto = autoUpdate{}
	c.loadAutoUpdate()
	if c.update.auto.Tonight {
		t.Fatal("tonight kept with no slot waiting")
	}
}

// stoppableRauc is rauc install as a script whose first install runs until
// it is stopped: an incremental bundle's applier (delta) ends once the stop
// file appears, or else (and a full bundle at once) once the shell ends
// the download (the cut file, which cutStream writes); an applier that
// ignores the stop file (an older one) waits for the cut. The installs
// after it complete, an incremental one resuming.
func stoppableRauc(t *testing.T, c *Core, ignoreStop bool) (calls, cut string) {
	dir := t.TempDir()
	calls, cut = filepath.Join(dir, "calls"), filepath.Join(dir, "cut")
	stop := c.update.stopFile
	if ignoreStop {
		stop = filepath.Join(dir, "never")
	}
	log := c.update.deltaLog
	script := "#!/bin/sh\necho \"$2\" >> " + calls + "\n" +
		"if [ \"$(grep -c . " + calls + ")\" = 1 ]; then\n" +
		"  case \"$2\" in *delta*) echo 'phone-ota-delta: progress: operation 3 of 83, 4096 bytes written' > " + log + " ;; esac\n" +
		"  i=0; while [ ! -e " + stop + " ] && [ ! -e " + cut + " ] && [ $i -lt 400 ]; do sleep 0.02; i=$((i + 1)); done\n" +
		"  if [ -e " + cut + " ]; then echo \"Failed to stream bundle $2: Input/output error\" >&2; exit 1; fi\n" +
		"  echo 'phone-ota-delta: stopped on request; installing it again resumes at operation 40' >> " + log + "\n" +
		"  echo 'failed to run slot hook: Child process exited with code 1' >&2; exit 1\n" +
		"fi\n" +
		"case \"$2\" in *delta*) echo 'phone-ota-delta: resumed at operation 40 of 83: 1000 bytes of payload data not read' > " + log + " ;; esac\n" +
		"printf 'slot=b\\ngood=a\\ntries=3\\nbooted=a\\n' > " + filepath.Join(c.update.slots, "boot") + "\n"
	_ = os.WriteFile(c.update.rauc, []byte(script), 0o755)
	c.update.cutStream = func() error { return os.WriteFile(cut, nil, 0o644) }
	return calls, cut
}

// An automatic install stops when its conditions go away while it runs:
// the battery low off the charger, a call, a metered network. RAUC cannot
// pause: an incremental bundle's applier is asked to stop at a checkpoint
// (the stop file), a full bundle's download ends, and so does an applier's
// that does not stop in time. The install waits, paused, with the offer
// kept, says why in system.update, and goes on by itself once the
// conditions hold: the same bundle again (the incremental one resumes),
// never the full bundle for an incremental one that was stopped.
func TestAutoInstallStops(t *testing.T) {
	for _, tc := range []struct {
		name       string
		delta      string
		ignoreStop bool
		change     func(c *Core, wifi *bool)
		why        string
	}{
		{"battery", "delta.raucb", false, func(c *Core, _ *bool) { c.powerOverride = &Power{Level: 30} }, "the battery is at 30%"},
		{"call", "delta.raucb", false, func(c *Core, _ *bool) { c.calls = map[string]*callInfo{"x": {}} }, "in a call"},
		{"off Wi-Fi", "delta.raucb", false, func(_ *Core, wifi *bool) { *wifi = false }, "not on Wi-Fi"},
		{"full bundle", "", false, func(c *Core, _ *bool) { c.powerOverride = &Power{Level: 30} }, "the battery is at 30%"},
		{"older applier", "delta.raucb", true, func(c *Core, _ *bool) { c.powerOverride = &Power{Level: 30} }, "the battery is at 30%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := updateCore(t, filepath.Join(t.TempDir(), "rauc"))
			c.settings.AutoSystemUpdate, c.cfg.UpdateURL = true, "http://x"
			wifi := true
			c.update.unmetered = func(then func(bool)) { then(wifi) }
			calls, cut := stoppableRauc(t, c, tc.ignoreStop)
			c.update.offer = &updateOffer{Version: "0.8.0", URL: "http://x/full.raucb"}
			if tc.delta != "" {
				c.update.offer.Delta = &updateDelta{From: "0.7.0", URL: "http://x/" + tc.delta}
			}
			c.startInstall(nil)
			c.updateTick()
			if c.update.stopping != "" {
				t.Fatal("stopped with the conditions met")
			}
			tc.change(c, &wifi)
			c.SetClockSkew(autoWatchNetwork)
			c.updateTick()
			pumpUntil(t, c, func() bool { return c.update.stopping != "" })
			if tc.ignoreStop {
				if _, err := os.Stat(cut); err == nil {
					t.Fatal("the download ended before the applier had its time")
				}
				c.SetClockSkew(autoWatchNetwork + updateStopGrace)
				c.updateTick()
			}
			pumpUntil(t, c, func() bool { return c.update.state != "installing" })
			st := c.updateStatus()
			if st.State != "idle" || !strings.Contains(c.update.paused, tc.why) || st.Paused == "" || st.Available == nil || st.Error != "" {
				t.Fatalf("after the stop: %+v (paused %q)", st, c.update.paused)
			}
			if _, err := os.Stat(cut); (err == nil) != (tc.delta == "" || tc.ignoreStop) {
				t.Fatalf("the download ended: %v", err == nil)
			}
			if _, err := os.Stat(c.update.stopFile); err == nil {
				t.Fatal("the stop file stayed")
			}
			if !strings.Contains(c.update.auto.last, "paused") {
				t.Fatalf("the automatic update says %q", c.update.auto.last)
			}
			// Not yet: the conditions are as they were.
			c.SetClockSkew(autoWatchNetwork + updateStopGrace + autoPauseCheck)
			c.updateTick()
			pumpUntil(t, c, func() bool { return !c.update.auto.busy })
			if c.update.state != "idle" {
				t.Fatalf("went on with the conditions still unmet: %s", c.update.state)
			}
			c.powerOverride, c.calls, wifi = &Power{Level: 82}, nil, true
			_ = os.Remove(cut)
			c.SetClockSkew(autoWatchNetwork + updateStopGrace + 2*autoPauseCheck)
			c.updateTick()
			pumpUntil(t, c, func() bool { return c.update.state == "installed" })
			url := "http://x/full.raucb"
			if tc.delta != "" {
				url = "http://x/" + tc.delta
			}
			raw, _ := os.ReadFile(calls)
			ran := strings.Fields(string(raw))
			if len(ran) != 2 || path.Base(ran[0]) != path.Base(url) || path.Base(ran[1]) != path.Base(url) || !strings.HasPrefix(ran[1], "http://127.0.0.1:") {
				t.Fatalf("rauc ran for %q, want %s twice through the shell", raw, url)
			}
			if st := c.updateStatus(); st.Paused != "" || !st.Automatic {
				t.Fatalf("installed: %+v", st)
			}
		})
	}
}

// The user marks the Wi-Fi an automatic install downloads on as metered
// (Settings › Wi-Fi): the shell looks at the network at once, not
// autoWatchNetwork later, and the install stops at its checkpoint and
// waits; it stays paused while the network is metered and goes on,
// resuming the incremental bundle, once the user says it is not.
func TestAutoInstallStopsOnMeteredWifi(t *testing.T) {
	if !alone(t) {
		return
	}
	c, _ := meterCore(t, "Home-Fibre", "", "192.168.50.23")
	c.settings.AutoSystemUpdate, c.cfg.UpdateURL = true, "http://x"
	c.update.rauc = filepath.Join(t.TempDir(), "rauc")
	calls, cut := stoppableRauc(t, c, false)
	c.update.offer = &updateOffer{Version: "0.8.0", URL: "http://x/full.raucb", Delta: &updateDelta{From: "0.7.0", URL: "http://x/delta.raucb"}}
	c.startInstall(nil)
	c.updateTick()
	pumpUntil(t, c, func() bool { return !c.update.watching })
	if c.update.stopping != "" || c.update.state != "installing" {
		t.Fatalf("stopped on unmetered Wi-Fi: %q %s", c.update.stopping, c.update.state)
	}
	if err := c.setMetered("Home-Fibre", ui.MeteredYes); err != nil {
		t.Fatal(err)
	}
	c.updateTick()
	pumpUntil(t, c, func() bool { return c.update.stopping != "" })
	pumpUntil(t, c, func() bool { return c.update.state != "installing" })
	st := c.updateStatus()
	if st.State != "idle" || c.update.paused != "on a metered network (Home-Fibre)" || st.Paused != pauseText(c.update.paused) || st.Available == nil {
		t.Fatalf("after the stop: %+v (paused %q)", st, c.update.paused)
	}
	if _, err := os.Stat(cut); err == nil {
		t.Fatal("the download was cut: the applier stops at its checkpoint")
	}
	c.SetClockSkew(autoPauseCheck)
	c.updateTick()
	pumpUntil(t, c, func() bool { return !c.update.auto.busy })
	if c.update.state != "idle" || !strings.Contains(c.update.paused, "metered") {
		t.Fatalf("went on on a metered network: %s, %q", c.update.state, c.update.paused)
	}
	if err := c.setMetered("Home-Fibre", ui.MeteredNo); err != nil {
		t.Fatal(err)
	}
	c.SetClockSkew(autoPauseCheck)
	c.updateTick()
	pumpUntil(t, c, func() bool { return c.update.state == "installed" })
	raw, _ := os.ReadFile(calls)
	if ran := strings.Fields(string(raw)); len(ran) != 2 || path.Base(ran[0]) != "delta.raucb" || path.Base(ran[1]) != "delta.raucb" {
		t.Fatalf("rauc ran for %q", raw)
	}
	if log, _ := os.ReadFile(c.update.deltaLog); !strings.Contains(string(log), "resumed at operation 40") {
		t.Fatalf("the incremental bundle did not resume: %s", log)
	}
}

// What the user installs is not stopped: the conditions are automatic
// updates' own.
func TestUserInstallNotStopped(t *testing.T) {
	c := updateCore(t, filepath.Join(t.TempDir(), "rauc"))
	c.settings.AutoSystemUpdate, c.cfg.UpdateURL = true, "http://x"
	c.update.unmetered = func(then func(bool)) { then(false) }
	stoppableRauc(t, c, false)
	c.powerOverride = &Power{Level: 10}
	c.update.offer = &updateOffer{Version: "0.8.0", URL: "http://x/full.raucb", Delta: &updateDelta{From: "0.7.0", URL: "http://x/delta.raucb"}}
	c.startInstall(func(any, error) {})
	for range 3 {
		c.SetClockSkew(autoWatchNetwork + updateStopGrace)
		c.updateTick()
	}
	if c.update.stopping != "" {
		t.Fatalf("stopped the user's install: %s", c.update.stopping)
	}
	if _, err := os.Stat(c.update.stopFile); err == nil {
		t.Fatal("asked the user's install to stop")
	}
	_ = c.update.cutStream()
	pumpUntil(t, c, func() bool { return c.update.state != "installing" || !c.update.retryAt.IsZero() })
}
