package shell

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
)

// sysTree points hal.SysRoot at a tree of its own for the test.
func sysTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	saved := hal.SysRoot
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = saved })
	return root
}

// supply writes a power supply's attributes into the tree at root.
func supply(t *testing.T, root, name string, attrs map[string]string) string {
	t.Helper()
	d := filepath.Join(root, "class", "power_supply", name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range attrs {
		if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// The OnePlus 6T's supplies: the bq27411 fuel gauge, which says "Full" at
// 100% plugged in or not, and the PMI8998 charger.
func fajitaSupplies(t *testing.T, root string, online bool) {
	t.Helper()
	charger := map[string]string{"type": "USB", "online": "0", "status": "Discharging", "usb_type": "[Unknown] SDP DCP CDP"}
	if online {
		charger = map[string]string{"type": "USB", "online": "1", "status": "Full", "usb_type": "Unknown [SDP] DCP CDP"}
	}
	supply(t, root, "bq27411-0", map[string]string{"type": "Battery", "capacity": "100", "status": "Full"})
	supply(t, root, "pmi8998-charger", charger)
}

func chargerToast(c *Core) string {
	if c.toast == nil {
		return ""
	}
	return c.toast.body
}

// Report 006 (docs/39-oneplus-6t.md §16.10): on the 6T at 100%, the
// charger pulled out and plugged in again with the screen off did
// nothing. Its fuel gauge kept saying "Full", which counted as charging,
// so the Shell never saw the charger go; now the charger's online decides.
func TestPluggingInAtAFullBatteryLightsTheScreen(t *testing.T) {
	root := sysTree(t)
	fajitaSupplies(t, root, true)
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.screenOff("test")
		return nil
	})
	fajitaSupplies(t, root, false)
	var off bool
	_ = c.onLoop(func() error {
		c.checkScreen()
		off = c.ScreenOff()
		return nil
	})
	if !off {
		t.Fatal("pulling the charger out lit the screen")
	}
	fajitaSupplies(t, root, true)
	var toast string
	_ = c.onLoop(func() error {
		c.checkScreen()
		off, toast = c.ScreenOff(), chargerToast(c)
		return nil
	})
	if off || toast != tr.T("Charging") {
		t.Fatalf("plugged in again: screen off %v, toast %q", off, toast)
	}
}

// Report 006: the Shell's loop stopped for good once, the screen on, while
// it read the charger's usb_type: the PMI8998 answers EAGAIN until it has
// detected the charger, and Go parked the read on its poller, waiting for
// the attribute to say it was ready, which sysfs never does (a FIFO with
// nothing in it stands in). The housekeeping now goes on; the screen
// lights at the plug and the toast waits until the charger is known.
func TestTheHousekeepingGoesOnWhileTheChargerIsDetected(t *testing.T) {
	sysTree(t)
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.screenOff("test")
		return nil
	})
	root := sysTree(t)
	supply(t, root, "bq27411-0", map[string]string{"type": "Battery", "capacity": "100", "status": "Full"})
	d := supply(t, root, "pmi8998-charger", map[string]string{"type": "USB", "online": "1", "status": "Not charging"})
	fifo := filepath.Join(d, "usb_type")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFO:", err)
	}
	fd, err := syscall.Open(fifo, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Before loopCore's cleanup, which needs the loop: closing the last
	// writer ends a read parked on it.
	t.Cleanup(func() { syscall.Close(fd) })
	type state struct {
		off   bool
		toast string
	}
	done := make(chan state, 1)
	go func() {
		_ = c.onLoop(func() error {
			c.expire()
			done <- state{c.ScreenOff(), chargerToast(c)}
			return nil
		})
	}()
	select {
	case s := <-done:
		if s.off || s.toast != "" {
			t.Fatalf("the charger still detected: screen off %v, toast %q (want on, none yet)", s.off, s.toast)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the housekeeping has not returned in 5 s: the loop is stuck reading usb_type")
	}
	syscall.Close(fd)
	os.Remove(fifo)
	supply(t, root, "pmi8998-charger", map[string]string{"usb_type": "Unknown SDP [DCP] CDP"})
	var toast string
	_ = c.onLoop(func() error {
		c.checkScreen()
		toast = chargerToast(c)
		return nil
	})
	if toast != tr.T("Fast charging") {
		t.Fatalf("the charger detected as a dedicated one: toast %q", toast)
	}
}

// A charger whose type never comes still gets its toast, chargeToastWait
// after the plug.
func TestTheChargersToastWaitsNoLongerThanItsTime(t *testing.T) {
	c := loopCore(t, t.TempDir())
	var toast string
	_ = c.onLoop(func() error {
		c.screenOff("test")
		c.powerOverride = &Power{Level: 50, Charging: true, Detecting: true}
		c.checkCharger()
		toast = chargerToast(c)
		return nil
	})
	if toast != "" {
		t.Fatalf("toast %q before the charger is known", toast)
	}
	_ = c.onLoop(func() error {
		c.screen.pluggedAt = time.Now().Add(-chargeToastWait)
		c.checkCharger()
		toast = chargerToast(c)
		return nil
	})
	if toast != tr.T("Charging") {
		t.Fatalf("toast %q after %s", toast, chargeToastWait)
	}
}

// The kernel's uevent for the charger has the Shell check it at once, not
// at the next housekeeping (every 5 s with the screen off).
func TestAChargerUeventChecksTheChargerAtOnce(t *testing.T) {
	root := sysTree(t)
	fajitaSupplies(t, root, false)
	var changed func()
	saved := watchSupplies
	watchSupplies = func(f func()) error {
		changed = f
		return nil
	}
	t.Cleanup(func() { watchSupplies = saved })
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.watchPower(SKU{ID: "oneplus-fajita"})
		c.screenOff("test")
		return nil
	})
	if changed == nil {
		t.Fatal("no uevent watch on a phone with a battery")
	}
	fajitaSupplies(t, root, true)
	changed()
	var off bool
	var toast string
	_ = c.onLoop(func() error {
		off, toast = c.ScreenOff(), chargerToast(c)
		return nil
	})
	if off || toast != tr.T("Charging") {
		t.Fatalf("after the uevent: screen off %v, toast %q", off, toast)
	}
}
