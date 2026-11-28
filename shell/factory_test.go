package shell

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/factory"
	"github.com/kario-os/kario/keystore"
)

// factoryCore is a Core in factory mode for a test SKU, its runner over a
// fake unit; it returns the runner's client too.
func factoryCore(t *testing.T, sku string) (*Core, *factory.Unit, *factory.Fake) {
	t.Helper()
	saved := factoryPoll
	factoryPoll = 50 * time.Millisecond
	t.Cleanup(func() { factoryPoll = saved })
	c := loopCore(t, t.TempDir())
	s, err := factory.FakeSKU(sku)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "kf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	fake, err := factory.NewFake(dir, s, factory.FakeOptions{Keystore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	id, err := fake.Env.Gate()
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(fake.Env.RunDir, factory.Sock)
	ln, err := factory.ListenSock(sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = factory.Serve(ln, factory.NewServer(fake.Env, s, id).Local()) }()
	u := factory.Dial("unix:" + sock)
	_ = c.onLoop(func() error {
		c.booted = time.Time{}
		c.EnterFactory(u, func() { ln.Close() })
		return nil
	})
	t.Cleanup(func() { _ = c.onLoop(func() error { c.LeaveFactory(); return nil }) })
	waitLoop(t, c, "the plan", func() bool { return c.factory.plan != nil })
	return c, u, fake
}

func waitLoop(t *testing.T, c *Core, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		var done bool
		_ = c.onLoop(func() error { done = ok(); return nil })
		if done {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func factoryPressKeys(c *Core, keys ...Key) {
	_ = c.onLoop(func() error {
		for _, k := range keys {
			c.Press(k)
		}
		return nil
	})
}

// Every key a test SKU's keys check asks for comes to the Shell as a key
// it counts: one it could not would fail the check on every phone.
func TestFactoryKeyNames(t *testing.T) {
	for _, id := range []string{"qemu-t9", "ref-t9-qcm6490-4g", "ref-qwerty-qcm6490-4g", "ref-flip-qcm6490-4g", "oneplus-fajita-t9"} {
		s, err := factory.FakeSKU(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Factory.Keys) == 0 {
			t.Fatalf("%s: no keys", id)
		}
		for _, k := range s.Factory.Keys {
			if got := factoryKeyName(factoryKeyFor(k)); got != k {
				t.Errorf("%s: key %s arrives as %q", id, k, got)
			}
		}
	}
	if got := factoryKeyName(Runes("Q")); got != "q" {
		t.Errorf("a shifted letter: %q", got)
	}
}

// The keys check passes once every key was pressed, the result goes to
// the runner, and the station's full test sees it.
func TestFactoryKeysCheck(t *testing.T) {
	c, u, _ := factoryCore(t, "ref-t9-qcm6490-4g")
	var keys []string
	_ = c.onLoop(func() error {
		ch, _ := c.factory.check("keys")
		keys = ch.Keys
		c.factoryOpen(c.factory, "keys")
		return nil
	})
	if len(keys) != 26 {
		t.Fatalf("keys %v", keys)
	}
	for _, k := range keys[:len(keys)-1] {
		factoryPressKeys(c, factoryKeyFor(k))
	}
	if got := c.FactoryResult("keys"); got != "" {
		t.Fatalf("one key left: %q", got)
	}
	factoryPressKeys(c, factoryKeyFor(keys[len(keys)-1]))
	waitLoop(t, c, "the result", func() bool { return c.FactoryResult("keys") == "pass" && c.factory.screen == "" })
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		p, err := u.Plan()
		if err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(p.Done, func(r factory.CheckResult) bool { return r.ID == "keys" && r.Result == factory.Pass }) {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the runner has not the result: %v", p.Done)
		}
	}
	rep, err := u.Test(factory.ModeFull, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := rep.Result("keys"); r.Result != factory.Pass {
		t.Fatalf("the station's report: %+v", r)
	}
}

// A keys check that stops getting new keys fails, naming the keys left.
func TestFactoryKeysTimeout(t *testing.T) {
	saved := factoryIdle
	factoryIdle = 100 * time.Millisecond
	t.Cleanup(func() { factoryIdle = saved })
	c, _, _ := factoryCore(t, "ref-t9-qcm6490-4g")
	_ = c.onLoop(func() error { c.factoryOpen(c.factory, "keys"); return nil })
	factoryPressKeys(c, Runes("5"))
	waitLoop(t, c, "the failure", func() bool { return c.FactoryResult("keys") == "fail" })
	_ = c.onLoop(func() error {
		if r := c.factory.done["keys"].Reason; !strings.Contains(r, "not pressed:") || strings.Contains(r, " 5 ") {
			t.Errorf("reason %q", r)
		}
		return nil
	})
}

// The display check shows each pattern on the whole panel, then asks; the
// operator's answer is the result.
func TestFactoryPatterns(t *testing.T) {
	c, _, _ := factoryCore(t, "ref-qwerty-qcm6490-4g")
	if err := c.onLoop(func() error {
		f := c.factory
		c.factoryOpen(f, "display.patterns")
		if c.Frame(Device{Width: 320, Height: 240}).Pattern != nil {
			t.Error("a pattern before the start")
		}
		c.Press(Key{Name: KeyEnter})
		for i, want := range factoryPatterns {
			fr := c.Frame(Device{Width: 320, Height: 240})
			if fr.Pattern == nil || fr.Pattern.Color != want.Color || fr.Pattern.Grid != want.Grid {
				return fmt.Errorf("pattern %d: %+v", i, fr.Pattern)
			}
			c.Press(Runes("x"))
		}
		if fr := c.Frame(Device{Width: 320, Height: 240}); fr.Pattern != nil || fr.Overlay == nil || !strings.Contains(strings.Join(fr.Overlay.Lines, " "), tr.T("Did every pattern fill the screen evenly, with no dead or stuck pixels?")) {
			return fmt.Errorf("after the patterns: %+v", fr.Overlay)
		}
		c.Press(Key{Name: KeyEsc})
		if c.FactoryResult("display.patterns") != "fail" {
			return fmt.Errorf("result %q", c.FactoryResult("display.patterns"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The touch check passes once every square was touched, wherever the
// panel's orientation puts them.
func TestFactoryTouch(t *testing.T) {
	c, _, _ := factoryCore(t, "qemu-t9")
	if err := c.onLoop(func() error {
		c.screenDev = Device{Width: 480, Height: 960, Scale: 2}
		c.factoryOpen(c.factory, "touch")
		w, h := 240.0, 480.0
		for row := 0; row < factoryTouchRows; row++ {
			for col := 0; col < factoryTouchCols; col++ {
				if c.FactoryResult("touch") != "" {
					return fmt.Errorf("passed with square %d,%d untouched", col, row)
				}
				x, y := (float64(col)+0.5)*w/factoryTouchCols, (float64(row)+0.5)*h/factoryTouchRows
				c.Touch("down", x, y, nil, time.Now())
				if p := c.factoryPattern(); c.factory.screen == "touch" && (p == nil || !slices.Contains(p.Done, row*factoryTouchCols+col)) {
					return fmt.Errorf("square %d,%d not marked: %+v", col, row, p)
				}
				c.Touch("up", x, y, nil, time.Now())
			}
		}
		if c.FactoryResult("touch") != "pass" {
			return fmt.Errorf("result %q", c.FactoryResult("touch"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The microphone check measures through the runner, the speaker's tone
// playing (the fake unit's audiod makes it louder).
func TestFactoryMicrophone(t *testing.T) {
	c, _, fake := factoryCore(t, "oneplus-fajita-t9")
	_ = c.onLoop(func() error { c.factoryOpen(c.factory, "microphone"); return nil })
	factoryPressKeys(c, Key{Name: KeyPower})
	waitLoop(t, c, "the level", func() bool { return c.FactoryResult("microphone") == "pass" })
	_ = c.onLoop(func() error {
		if !strings.Contains(c.factory.note, "with the tone") {
			t.Errorf("note %q", c.factory.note)
		}
		return nil
	})
	fake.SetMic(-90)
	factoryPressKeys(c, Key{Name: KeyEnter})
	_ = c.onLoop(func() error { c.factoryOpen(c.factory, "microphone"); return nil })
	factoryPressKeys(c, Key{Name: KeyEnter})
	waitLoop(t, c, "the failure", func() bool { return c.FactoryResult("microphone") == "fail" })
}

// The 6T has only its buttons: the volume keys move and answer, power
// opens.
func TestFactoryButtonsOnly(t *testing.T) {
	c, _, _ := factoryCore(t, "oneplus-fajita-t9")
	if err := c.onLoop(func() error {
		f := c.factory
		if !f.buttons() {
			return errors.New("not a buttons-only phone")
		}
		items := f.homeItems()
		i := slices.Index(items, "vibrator.felt")
		if i < 0 {
			return fmt.Errorf("no vibrator.felt in %v", items)
		}
		for range i {
			c.Press(Key{Name: KeyVolDown})
		}
		c.Press(Key{Name: KeyPower})
		if f.screen != "vibrator.felt" {
			return fmt.Errorf("screen %q", f.screen)
		}
		c.Press(Key{Name: KeyVolUp})
		if c.FactoryResult("vibrator.felt") != "pass" {
			return fmt.Errorf("result %q", c.FactoryResult("vibrator.felt"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The cameras the factory screen cannot open are skipped with a reason
// (mediad drives the back camera only, and without a driver none).
func TestFactoryCameraSkipped(t *testing.T) {
	c, _, _ := factoryCore(t, "oneplus-fajita-t9")
	_ = c.onLoop(func() error { c.factoryOpen(c.factory, "camera.front"); return nil })
	waitLoop(t, c, "the skip", func() bool { return c.FactoryResult("camera.front") == "skip" })
	_ = c.onLoop(func() error {
		if r := c.factory.done["camera.front"].Reason; !strings.Contains(r, "front") {
			t.Errorf("reason %q", r)
		}
		return nil
	})
}

// Factory mode: no app starts, the status bar says so, the screen is the
// factory screen and stays on.
func TestFactoryModeScreen(t *testing.T) {
	c, _, _ := factoryCore(t, "ref-t9-qcm6490-4g")
	if err := c.onLoop(func() error {
		if err := c.spawn(&App{M: &Manifest{ID: "com.example.notes"}}); err == nil || !strings.Contains(err.Error(), "factory mode") {
			t.Errorf("an app started: %v", err)
		}
		if mid, kind := c.statusMid(); kind != "factory" || mid == "" {
			t.Errorf("status %q %q", mid, kind)
		}
		fr := c.Frame(Device{Width: 240, Height: 320})
		if fr.Screen != "sys/factory" || fr.Overlay == nil || fr.Overlay.Kind != "menu" || fr.Status.MidKind != "factory" {
			return fmt.Errorf("frame %q %+v %+v", fr.Screen, fr.Overlay, fr.Status)
		}
		if c.screenTimeout() != factoryScreenOff*time.Second {
			t.Errorf("screen off after %s", c.screenTimeout())
		}
		if txt := c.Render().Plain(); !strings.Contains(txt, tr.T("Factory test")) || !strings.Contains(txt, tr.T("Keys")) {
			t.Errorf("text screen:\n%s", txt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The USB link: in factory mode the station's (the runner's port and DHCP
// only) without the trust prompt, closed when the cable goes; out of it
// the trust prompt as always.
func TestFactoryUSB(t *testing.T) {
	c, _, _ := factoryCore(t, "ref-t9-qcm6490-4g")
	_ = c.onLoop(func() error {
		c.usb.sim, c.usb.simOn = true, true
		c.checkUSB()
		up, tcp, udp := c.FactoryUSB()
		if !up || c.usb.prompt != nil || !slices.Equal(tcp, []uint16{factory.Port}) || !slices.Equal(udp, []uint16{67}) {
			return fmt.Errorf("configured in factory mode: up %v, prompt %v, tcp %v, udp %v", up, c.usb.prompt != nil, tcp, udp)
		}
		if c.usb.trusted {
			return fmt.Errorf("the computer is trusted")
		}
		c.usb.simOn = false
		c.checkUSB()
		if up, tcp, udp := c.FactoryUSB(); up || len(tcp) > 0 || len(udp) > 0 {
			return fmt.Errorf("the cable out: up %v, tcp %v, udp %v", up, tcp, udp)
		}
		c.LeaveFactory()
		c.usb.simOn = true
		c.checkUSB()
		if c.usb.prompt == nil || len(c.usb.link.TCP) > 0 {
			return fmt.Errorf("out of factory mode: prompt %v, tcp %v", c.usb.prompt != nil, c.usb.link.TCP)
		}
		return nil
	})
}

// Provisioned by the station, the factory screen says so and powers the
// phone off after a while.
func TestFactoryProvisioned(t *testing.T) {
	saved := factoryOffAfter
	factoryOffAfter = 1500 * time.Millisecond
	t.Cleanup(func() { factoryOffAfter = saved })
	c, u, _ := factoryCore(t, "ref-t9-qcm6490-4g")
	ca, err := keystore.LoadCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	if _, err := factory.Provision(u, ca, hex.EncodeToString(b[:])); err != nil {
		t.Fatal(err)
	}
	waitLoop(t, c, "the provisioned screen", func() bool { return c.factory.screen == "provisioned" })
	_ = c.onLoop(func() error {
		if txt := c.Render().Plain(); !strings.Contains(txt, tr.T("OK power off now")) {
			t.Errorf("text screen:\n%s", txt)
		}
		return nil
	})
	// No SKU: the developer machine's Core says it stays on.
	waitLoop(t, c, "the power off", func() bool { return c.toast != nil && c.toast.body == tr.T("Not a phone: it stays on") })
}
