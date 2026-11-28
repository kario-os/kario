package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lidCore is menuCore on a flip (sku.json's lid), its screen on and the
// lid open.
func lidCore(t *testing.T) *Core {
	c := menuCore(t)
	c.cfg.SKU.Lid = "Lid"
	return c
}

// lidAt is the renderer's report of the lid (lid.rs).
func lidAt(shut bool) string { return fmt.Sprintf(`{"t":"lid","shut":%v}`, shut) }

// The handshake names the lid's input device, as the SKU does.
func TestLidSKUDevice(t *testing.T) {
	s, err := SimulatedSKU(filepath.Join("testdata", "sku-lid.json"))
	if err != nil || s.Lid != "Lid" {
		t.Fatalf("the test SKU: %+v, %v", s, err)
	}
	d, _ := SKU{Device: "bar-t9", Lid: "Lid"}.SKUDevice()
	if raw, _ := json.Marshal(d); d.Lid != "Lid" || !strings.Contains(string(raw), `"lid":"Lid"`) {
		t.Fatalf("the handshake's device %s", raw)
	}
	if raw, _ := json.Marshal(Device{}); strings.Contains(string(raw), "lid") {
		t.Fatalf("a device without a lid says %s", raw)
	}
}

// The lid reaches the Shell from a renderer that reads it (feature "lid"):
// shut, the inner display goes off; open, it lights. A report without
// the position, or from a renderer without the feature, moves nothing.
// Before, nothing read the lid's reports.
func TestLidReported(t *testing.T) {
	c := lidCore(t)
	c.gui = &GUI{c: c, done: make(chan struct{}), info: RendererInfo{Features: []string{"lid"}}}
	reported(t, c, lidAt(true))
	if !c.lidShut() || !c.ScreenOff() {
		t.Fatalf("reported shut: shut %v, screen off %v", c.lidShut(), c.ScreenOff())
	}
	reported(t, c, `{"t":"lid"}`, lidAt(true))
	if !c.lidShut() || !c.ScreenOff() {
		t.Fatalf("no position, shut again: shut %v, screen off %v", c.lidShut(), c.ScreenOff())
	}
	reported(t, c, lidAt(false))
	if c.lidShut() || c.ScreenOff() {
		t.Fatalf("reported open: shut %v, screen off %v", c.lidShut(), c.ScreenOff())
	}
	c.gui.info.Features = nil
	reported(t, c, lidAt(true))
	if c.lidShut() || c.ScreenOff() {
		t.Fatalf("from a renderer that does not read the lid: shut %v, screen off %v", c.lidShut(), c.ScreenOff())
	}
}

// A renderer reads the lid's device from the handshake and reports it
// after its ready; a renderer that does not read it (an older one, or one
// without the device) leaves the lid open, though it was shut: the keys
// under a lid counted shut would count for nothing, with nothing to tell
// when it opens.
func TestLidHandshake(t *testing.T) {
	for _, reads := range []bool{true, false} {
		c := loopCore(t, t.TempDir())
		dir := t.TempDir()
		hello, renderer := filepath.Join(dir, "hello.json"), filepath.Join(dir, "renderer")
		features := `"snap"`
		if reads {
			features += `,"lid"`
		}
		script := "#!/bin/sh\nread hello\necho \"$hello\" > " + hello +
			"\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"x\"},\"features\":[" + features + "]}'\n" +
			"echo '" + lidAt(true) + "'\ncat >/dev/null\n"
		if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		dev, _ := SKU{Device: "bar-t9", Lid: "Lid"}.SKUDevice()
		err := c.onLoop(func() error {
			c.cfg.SKU.Lid = "Lid"
			c.lid.shut = !reads
			_, err := StartGUI(c, renderer, dev)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.onLoop(func() error { c.gui.Close(); return nil }) })
		shut := func() bool {
			var s bool
			_ = c.onLoop(func() error { s = c.lidShut(); return nil })
			return s
		}
		end := time.Now().Add(5 * time.Second)
		for reads && !shut() && time.Now().Before(end) {
			time.Sleep(20 * time.Millisecond)
		}
		if !reads {
			time.Sleep(300 * time.Millisecond)
		}
		if shut() != reads {
			t.Errorf("a renderer that reads the lid %v: shut %v", reads, shut())
		}
		var got struct {
			Device struct {
				Lid string `json:"lid"`
			} `json:"device"`
		}
		raw, _ := os.ReadFile(hello)
		if err := json.Unmarshal(raw, &got); err != nil || got.Device.Lid != "Lid" {
			t.Errorf("hello %s: %v", raw, err)
		}
	}
}

// Shut, the lid turns the inner display off at once, and it stays off:
// what would light it (a call, an alarm, a notification, a script, the
// keys under the lid) lights nothing. Opening lights it.
func TestLidTurnsTheScreenOffAndOn(t *testing.T) {
	c := lidCore(t)
	c.lidSwitched(true)
	if !c.ScreenOff() {
		t.Fatal("shut: the screen stayed on")
	}
	for _, why := range []string{"call", "alarm", "notification", "script"} {
		if c.Wake(why) || !c.ScreenOff() {
			t.Fatalf("shut: the %s lit the screen", why)
		}
	}
	c.Press(Runes("5"))
	c.Press(Named(KeyEnter))
	c.Key(Named(KeyDown))
	if !c.ScreenOff() || len(c.overlays) != 0 || c.sys.top().S.ID != "sys.idle" {
		t.Fatalf("keys under the lid: screen off %v, %d overlays, %s", c.ScreenOff(), len(c.overlays), c.sys.top().S.ID)
	}
	c.lidSwitched(false)
	if c.ScreenOff() {
		t.Fatal("opened: the screen stayed off")
	}
	// On a SKU without a lid a report moves nothing.
	c.cfg.SKU.Lid = ""
	c.lidSwitched(true)
	if c.ScreenOff() || c.lidShut() {
		t.Fatalf("without a lid: screen off %v, shut %v", c.ScreenOff(), c.lidShut())
	}
}

// Under the shut lid the keypad's keys count for nothing; the side keys,
// a headset's media keys and the keys the computer sends still count.
func TestLidKeys(t *testing.T) {
	c := lidCore(t)
	c.lidSwitched(true)
	if !c.ScreenOff() {
		t.Fatal("shut: the screen stayed on")
	}
	before := c.settings.RingVolume
	c.Press(Named(KeyVolDown))
	if c.settings.RingVolume == before || !c.ScreenOff() {
		t.Fatalf("a volume key under the lid: volume %v (was %v), screen off %v", c.settings.RingVolume, before, c.ScreenOff())
	}
	for _, k := range []Key{Named(KeyVolUp), Named(KeyPlayPause), Named(KeyNextTrack), Named(KeyPrevTrack), Named(KeyA11y), Named(KeyPower), {Name: KeyRunes, Runes: []rune("5"), Injected: true}} {
		if c.lidDrops(k) {
			t.Errorf("%+v dropped under the lid", k)
		}
	}
	for _, k := range []Key{Runes("5"), Runes("*"), Named(KeyEnter), Named(KeyTab), Named(KeyF4), Named(KeyCall), Named(KeyEsc), Named(KeyUp), Named(KeyBackspace)} {
		if !c.lidDrops(k) {
			t.Errorf("%+v counted under the lid", k)
		}
	}
	c.lidSwitched(false)
	if c.lidDrops(Runes("5")) {
		t.Error("a key dropped with the lid open")
	}
}

// The power key under the shut lid lights nothing and opens neither the
// quick card view nor the power menu; it still silences a ringing call.
func TestLidPowerKey(t *testing.T) {
	if !alone(t) {
		return
	}
	c := lidCore(t)
	c.lidSwitched(true)
	c.PowerKey(false)
	c.PowerKey(false)
	time.Sleep(doublePress + 50*time.Millisecond)
	if got := quickView(t, c); !c.ScreenOff() || got != nil {
		t.Fatalf("two presses under the lid: screen off %v, quick view %q", c.ScreenOff(), got)
	}
	c.PowerKey(true)
	if _, ok := powerMenu(c); ok || !c.ScreenOff() {
		t.Fatalf("a long press under the lid: power menu %v, screen off %v", ok, c.ScreenOff())
	}
	c.sysWant = map[string]bool{"ring:1": true}
	c.PowerKey(false)
	if c.ringing() || !c.ScreenOff() {
		t.Fatalf("a press under the lid with a call ringing: ringing %v, screen off %v", c.ringing(), c.ScreenOff())
	}
	c.lidSwitched(false)
	c.PowerKey(false)
	if !c.ScreenOff() {
		t.Fatal("the power key with the lid open again left the screen on")
	}
}

// An alarm rings under the shut lid as it does with the lid open, and the
// power key snoozes it there, lighting nothing.
func TestLidAlarm(t *testing.T) {
	c, ring := alarmCore(t)
	c.cfg.SKU.Lid = "Lid"
	c.lidSwitched(true)
	e := ring()
	c.PowerKey(false)
	if !c.ScreenOff() || c.alarmRinging() || !e.Snoozed {
		t.Fatalf("a press under the lid with the alarm ringing: screen off %v, sounding %v, snoozed %v", c.ScreenOff(), c.alarmRinging(), e.Snoozed)
	}
}

// Shutting the lid silences a ringing call, which keeps ringing on screen
// for when the lid opens.
func TestLidSilencesTheRing(t *testing.T) {
	c := lidCore(t)
	c.sysWant = map[string]bool{"ring:1": true, "alarm:a1": true}
	c.sysStream = map[string]string{"ring:1": "s1", "alarm:a1": "s2"}
	c.lidSwitched(true)
	if c.ringing() || !c.silenced["ring:1"] || c.sysStream["ring:1"] != "" {
		t.Fatalf("shut while ringing: ringing %v, silenced %v, streams %v", c.ringing(), c.silenced, c.sysStream)
	}
	if !c.sysWant["ring:1"] || c.sysStream["alarm:a1"] != "s2" {
		t.Fatalf("shut while ringing: the call or the alarm ended: want %v, streams %v", c.sysWant, c.sysStream)
	}
}

// Opening the lid releases the keypad lock where there is no PIN, and asks
// for the PIN where there is one; shutting it locks as the screen going
// off does: never without a PIN, after the grace period with one.
func TestLidLocks(t *testing.T) {
	c := lidCore(t)
	c.lockNow("keypad")
	c.lidSwitched(true)
	c.lidSwitched(false)
	if c.locked() || c.ScreenOff() {
		t.Fatalf("opened, no PIN: locked %v, screen off %v", c.locked(), c.ScreenOff())
	}
	c.lidSwitched(true)
	c.SetClockSkew(time.Hour)
	c.lockTick()
	c.lidSwitched(false)
	if c.locked() {
		t.Fatal("shut an hour, no PIN: locked")
	}

	c.lock.pin, c.lock.prefs.Grace = true, 30
	c.lidSwitched(true)
	c.lidSwitched(false)
	if c.locked() || len(c.overlays) != 0 {
		t.Fatalf("opened within the grace period: locked %v, %d overlays", c.locked(), len(c.overlays))
	}
	c.lidSwitched(true)
	c.SetClockSkew(time.Minute)
	c.lidSwitched(false)
	pad, ok := overlayOf[*lockPadOverlay](c)
	if !c.secureLocked() || !ok || pad.digits != "" || c.ScreenOff() {
		t.Fatalf("opened after the grace period: locked %v, PIN pad %v, screen off %v", c.secureLocked(), ok, c.ScreenOff())
	}
	c.unlockNow("test")
	c.lock.prefs.Grace = 0
	c.lidSwitched(true)
	if !c.secureLocked() {
		t.Fatal("shut with no grace period: not locked")
	}
}

// The selftest's steps: require-lid skips the case unless the SKU has a
// lid; lid shuts and opens it, and fails without one.
func TestLidSteps(t *testing.T) {
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	r.Run("== flip\nrequire-lid\nlid shut\n== bar\nlid shut\n")
	if got := caseLine(r.Cases); got != "flip SKIP, bar FAIL" {
		t.Fatalf("without a lid: %s\n%s", got, out.String())
	}
	r.C.cfg.SKU.Lid = "Lid"
	r.Run("== flip\nrequire-lid\nlid shut\nscreen off\n")
	if got := caseLine(r.Cases); got != "flip PASS" || !r.C.lidShut() {
		t.Fatalf("shut: %s, shut %v\n%s", got, r.C.lidShut(), out.String())
	}
	r.Run("== open\nlid open\nscreen on\n== ajar\nlid ajar\n")
	if got := caseLine(r.Cases); got != "open PASS, ajar FAIL" || r.C.lidShut() {
		t.Fatalf("open, a wrong position: %s, shut %v\n%s", got, r.C.lidShut(), out.String())
	}
}
