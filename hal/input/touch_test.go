package input

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/kario-os/kario/hal"
)

// A touchscreen says its axes are the screen's: QEMU's virtio-multitouch
// and the 6T's RMI4 (slots), a single-contact panel (ABS_X); a touchpad
// (slots, no INPUT_PROP_DIRECT) and a keyboard are not ones.
func TestTouchscreens(t *testing.T) {
	root := t.TempDir()
	saved, savedDev := hal.SysRoot, DevRoot
	hal.SysRoot, DevRoot = root, "/dev"
	t.Cleanup(func() { hal.SysRoot, DevRoot = saved, savedDev })
	// ABS_MT_SLOT, _POSITION_X, _POSITION_Y, _TRACKING_ID (0x2f, 0x35, 0x36, 0x39).
	mt := "260800000000000\n"
	if strconv.IntSize == 32 {
		mt = "2608000 0\n"
	}
	for event, d := range map[string][2]string{
		"event0": {"0\n", "0\n"},  // a keyboard
		"event1": {"2\n", mt},     // virtio-multitouch
		"event2": {"1\n", mt},     // a touchpad: INPUT_PROP_POINTER
		"event3": {"2\n", "3\n"},  // ABS_X, ABS_Y
		"event4": {"2\n", "40\n"}, // ABS_RX only: no position
	} {
		dir := filepath.Join(root, "class", "input", event, "device")
		if err := os.MkdirAll(filepath.Join(dir, "capabilities"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "properties"), []byte(d[0]), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "capabilities", "abs"), []byte(d[1]), 0o644)
	}
	if got, want := Touchscreens(), []string{"/dev/input/event1", "/dev/input/event3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Touchscreens() = %v, want %v", got, want)
	}
}

type ev struct {
	typ, code uint16
	value     int32
}

func abs(code uint16, v int32) ev { return ev{evAbs, code, v} }

var syn = ev{evSyn, synReport, 0}

func contacts(c *Contacts, evs ...ev) []Contact {
	var out []Contact
	for _, e := range evs {
		if ct, ok := c.Event(e.typ, e.code, e.value); ok {
			out = append(out, ct)
		}
	}
	return out
}

// The finger followed, as the renderer follows it (paint/src/touch.rs): the
// first down, a second left alone and still left alone once the first has
// lifted, a new one after both are up; lost events cancel the touch.
func TestContactsFollowTheFirstFinger(t *testing.T) {
	c := &Contacts{Multi: true}
	got := contacts(c,
		abs(absMTSlot, 0), abs(absMTTracking, 7), abs(absMTX, 100), abs(absMTY, 200), syn,
		abs(absMTY, 180), syn,
		abs(absMTSlot, 1), abs(absMTTracking, 8), abs(absMTX, 900), syn,
		abs(absMTSlot, 0), abs(absMTTracking, -1), syn,
		abs(absMTSlot, 1), abs(absMTX, 950), syn,
		abs(absMTTracking, -1), syn,
		abs(absMTSlot, 0), abs(absMTTracking, 9), abs(absMTX, 5), abs(absMTY, 6), syn,
		ev{evSyn, synDropped, 0}, abs(absMTX, 7), syn,
		abs(absMTX, 8), syn,
	)
	want := []Contact{{Down, 100, 200}, {Move, 100, 180}, {Up, 100, 180}, {Down, 5, 6}, {Cancel, 5, 6}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}
	one := &Contacts{}
	got = contacts(one, abs(absX, 10), abs(absY, 20), ev{evKey, btnTouch, 1}, syn, abs(absX, 12), syn, ev{evKey, btnTouch, 0}, syn)
	if want := []Contact{{Down, 10, 20}, {Move, 12, 20}, {Up, 12, 20}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("one contact %v, want %v", got, want)
	}
}

// A plain file is no touchscreen: it has no axes to ask about.
func TestWatchTouchNeedsADevice(t *testing.T) {
	node := filepath.Join(t.TempDir(), "event0")
	if err := os.WriteFile(node, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if w, err := WatchTouch(node); err == nil {
		w.Close()
		t.Fatal("a plain file opened as a touchscreen")
	}
}
