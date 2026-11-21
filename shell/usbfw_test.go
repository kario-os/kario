package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
)

// Switching developer mode on a trusted cable changes what the link lets
// through at once: the debug shell (22) closes when developer mode goes
// off, not at the next unplugging, and opens when it comes on.
func TestUSBFirewallFollowsDeveloperMode(t *testing.T) {
	c := loopCore(t, t.TempDir())
	tcp := func(dev float64) ([]uint16, error) {
		var got []uint16
		err := c.onLoop(func() error {
			err := c.settingsSet(ui.SettingDeveloperMode, dev)
			got = c.usb.link.TCP
			return err
		})
		return got, err
	}
	if err := c.onLoop(func() error {
		c.settings.DeveloperMode, c.usb.trusted = true, true
		c.usbFirewall(true)
		if !slices.Contains(c.usb.link.TCP, 22) || !slices.Contains(c.usb.link.TCP, 80) {
			return fmt.Errorf("trusted in developer mode: tcp %v", c.usb.link.TCP)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := tcp(0); err != nil || slices.Contains(got, 22) || !slices.Contains(got, 80) {
		t.Fatalf("developer mode off: tcp %v %v", got, err)
	}
	if got, err := tcp(1); err != nil || !slices.Contains(got, 22) {
		t.Fatalf("developer mode on again: tcp %v %v", got, err)
	}
	_ = c.onLoop(func() error { c.usb.trusted = false; c.usbFirewall(false); return nil })
	if got, err := tcp(1); err != nil || len(got) != 0 {
		t.Fatalf("no computer trusted: tcp %v %v", got, err)
	}
}

// The drain is over when its count stood still for the idle time, or at
// its limit however busy; a count that went back (the rules applied
// again) counts as traffic.
func TestUSBDrainDone(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	d := linkDrain{since: t0, moved: t0}
	for _, s := range []struct {
		ms      int
		packets uint64
		done    bool
	}{
		{500, 0, false}, {1000, 4, false}, {3500, 9, false}, {5000, 2, false}, {7900, 2, false}, {8000, 2, true},
	} {
		if why := d.done(at(s.ms), s.packets, 3*time.Second, 30*time.Second); (why != "") != s.done {
			t.Fatalf("at %d ms with %d packets: %q, want done %v", s.ms, s.packets, why, s.done)
		}
	}
	busy := linkDrain{since: t0, moved: t0}
	for ms := 500; ms < 30000; ms += 500 {
		if why := busy.done(at(ms), uint64(ms), 3*time.Second, 30*time.Second); why != "" {
			t.Fatalf("busy, over at %d ms: %q", ms, why)
		}
	}
	if why := busy.done(at(30000), 30000, 3*time.Second, 30*time.Second); !strings.Contains(why, "busy") {
		t.Fatalf("busy at the limit: %q", why)
	}
}

// linkRecorder is a firewall that keeps what the link was given; packets
// is what the drain rule counted, refuse a kernel without its match.
type linkRecorder struct {
	mu      sync.Mutex
	applied []netfw.Link
	packets uint64
	refuse  bool
}

func (f *linkRecorder) ApplyLink(l netfw.Link) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse && len(l.Drain) > 0 {
		return errors.New("Operation not supported")
	}
	f.applied = append(f.applied, l)
	return nil
}

func (f *linkRecorder) LinkDrained(string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.packets, nil
}

func (f *linkRecorder) last() netfw.Link {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied[len(f.applied)-1]
}

func (f *linkRecorder) count(n uint64) {
	f.mu.Lock()
	f.packets += n
	f.mu.Unlock()
}

// Developer mode going off refuses new connections to the debug shell at
// once, but its open ones keep coming in while they are busy, so the
// session's end reaches the computer; once they are quiet, or at the
// limit, port 22 closes for them too. Developer mode on again, or the
// cable out, ends the drain; a kernel that refuses the drain's match gets
// the port closed at once.
func TestUSBFirewallDrains(t *testing.T) {
	c := loopCore(t, t.TempDir())
	fw := &linkRecorder{}
	set := func(dev float64) {
		_ = c.onLoop(func() error { return c.settingsSet(ui.SettingDeveloperMode, dev) })
	}
	_ = c.onLoop(func() error {
		c.usb.fw, c.usb.drainIdle, c.usb.drainMax = fw, 300*time.Millisecond, 3*time.Second
		c.settings.DeveloperMode, c.usb.trusted = true, true
		c.usb.simOn, c.usb.state = true, "configured"
		c.usbFirewall(true)
		return nil
	})
	if l := fw.last(); !slices.Contains(l.TCP, 22) || len(l.Drain) != 0 {
		t.Fatalf("developer mode on: %+v", l)
	}
	closed := func(within time.Duration) time.Duration {
		t.Helper()
		start := time.Now()
		for l := fw.last(); len(l.Drain) > 0; l = fw.last() {
			if time.Since(start) > within {
				t.Fatalf("still draining after %s: %+v", within, l)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if l := fw.last(); slices.Contains(l.TCP, 22) || !slices.Contains(l.TCP, 80) {
			t.Fatalf("after the drain: %+v", l)
		}
		return time.Since(start)
	}

	set(0)
	if l := fw.last(); slices.Contains(l.TCP, 22) || !slices.Equal(l.Drain, []uint16{22}) || !slices.Contains(l.TCP, 80) {
		t.Fatalf("developer mode off: new connections refused, open ones draining; got %+v", l)
	}
	for range 10 {
		fw.count(3)
		time.Sleep(100 * time.Millisecond)
	}
	if l := fw.last(); !slices.Equal(l.Drain, []uint16{22}) {
		t.Fatalf("closed while the connections were busy: %+v", l)
	}
	closed(2 * time.Second)

	set(1)
	set(0)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				fw.count(1)
			}
		}
	}()
	if d := closed(5 * time.Second); d < 2*time.Second {
		t.Errorf("busy connections cut after %s, before the limit", d)
	}
	close(stop)

	set(1)
	set(0)
	set(1)
	time.Sleep(time.Second)
	if l := fw.last(); !slices.Contains(l.TCP, 22) || len(l.Drain) != 0 {
		t.Fatalf("developer mode on again during the drain: %+v", l)
	}
	set(0)
	_ = c.onLoop(func() error { c.endSharing("the cable is out"); return nil })
	if l := fw.last(); len(l.TCP) != 0 || len(l.Drain) != 0 {
		t.Fatalf("the cable out during the drain: %+v", l)
	}

	_ = c.onLoop(func() error {
		c.usb.trusted, c.settings.DeveloperMode = true, true
		c.usbFirewall(true)
		return nil
	})
	fw.mu.Lock()
	fw.refuse = true
	fw.mu.Unlock()
	set(0)
	if l := fw.last(); slices.Contains(l.TCP, 22) || len(l.Drain) != 0 {
		t.Fatalf("a kernel without the drain's match: %+v", l)
	}
}

// A controller another system's gadget holds (Nura's USB network, which a
// phone's chroot is reached over) is left alone: no gadget of ours beside
// it, and no firewall on usb0, which would cut that network off.
func TestUSBLeavesAForeignGadgetAlone(t *testing.T) {
	c := loopCore(t, t.TempDir())
	root := t.TempDir()
	gadgets := filepath.Join(root, "kernel", "config", "usb_gadget")
	for _, d := range []string{filepath.Join(root, "class", "udc", "a600000.usb"), filepath.Join(gadgets, "g1")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gadgets, "g1", "UDC"), []byte("a600000.usb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	if err := c.onLoop(func() error {
		c.usb = usbState{}
		c.startUSB()
		if c.HasUSB() || c.usb.link.Name != "" {
			return fmt.Errorf("took the controller: udc %q, link %+v", c.usb.udc, c.usb.link)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(gadgets, "kario")); err == nil {
		t.Fatal("made a gadget of its own beside g1")
	}
}
