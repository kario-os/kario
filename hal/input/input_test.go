package input

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/kario-os/kario/hal"
)

func TestWithKey(t *testing.T) {
	root := t.TempDir()
	saved, savedDev := hal.SysRoot, DevRoot
	hal.SysRoot, DevRoot = root, "/dev"
	t.Cleanup(func() { hal.SysRoot, DevRoot = saved, savedDev })
	power := "10000000000000 0\n"
	if strconv.IntSize == 32 {
		power = "100000 0 0 0\n"
	}
	for event, keys := range map[string]string{
		"event0": power,        // gpio-keys: KEY_POWER only
		"event1": "0\n",        // a device without keys
		"event2": "10000000\n", // KEY_ENTER (28)
		"event3": "1 " + power, // KEY_POWER and a key past it
	} {
		p := filepath.Join(root, "class", "input", event, "device", "capabilities", "key")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(keys), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := WithKey(KeyPower), []string{"/dev/input/event0", "/dev/input/event3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("WithKey(KEY_POWER) = %v, want %v", got, want)
	}
	if got := WithKey(28); !reflect.DeepEqual(got, []string{"/dev/input/event2"}) {
		t.Fatalf("WithKey(KEY_ENTER) = %v", got)
	}
	if got := WithKey(-1); got != nil {
		t.Fatalf("WithKey(-1) = %v", got)
	}
}

func event(typ, code uint16, value int32) []byte {
	ev := make([]byte, eventSize)
	at := eventSize - 8
	binary.NativeEndian.PutUint16(ev[at:], typ)
	binary.NativeEndian.PutUint16(ev[at+2:], code)
	binary.NativeEndian.PutUint32(ev[at+4:], uint32(value))
	return ev
}

func TestWatch(t *testing.T) {
	node := filepath.Join(t.TempDir(), "event0")
	var evs []byte
	for _, e := range [][]byte{
		event(evKey, KeyPower, 1),
		event(0, 0, 0), // EV_SYN
		event(evKey, KeyPower, 2),
		event(evKey, KeyPower, 0),
		event(4, 4, 116), // EV_MSC
	} {
		evs = append(evs, e...)
	}
	if err := os.WriteFile(node, evs, 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Watch(node)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var got []Key
	for k := range w.C {
		got = append(got, k)
	}
	if want := []Key{{KeyPower, true}, {KeyPower, false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	if _, err := Watch(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("a missing node opened")
	}
}

// The kernel's key state comes from an event device; a file that is not
// one says so instead of an answer.
func TestDownNeedsADevice(t *testing.T) {
	node := filepath.Join(t.TempDir(), "event0")
	if err := os.WriteFile(node, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Watch(node)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Down(KeyPower); err == nil {
		t.Error("a plain file told a key's state")
	}
	if _, err := w.Down(-1); err == nil {
		t.Error("key -1 has a state")
	}
}
