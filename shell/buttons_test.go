package shell

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
	"github.com/kario-os/kario/hal/input"
)

// buttonBoard makes a board with a power button: its input device's key
// capabilities and an event node holding the given KEY_POWER values.
func buttonBoard(t *testing.T, values ...int32) {
	t.Helper()
	node := buttonDevice(t)
	var evs []byte
	for _, v := range values {
		evs = append(evs, powerEvent(v)...)
	}
	if err := os.WriteFile(node, evs, 0o644); err != nil {
		t.Fatal(err)
	}
}

// buttonDevice makes a board's power button device but for its event
// node, whose path it returns.
func buttonDevice(t *testing.T) string {
	t.Helper()
	sys, dev := t.TempDir(), t.TempDir()
	savedSys, savedDev := hal.SysRoot, input.DevRoot
	hal.SysRoot, input.DevRoot = sys, dev
	t.Cleanup(func() { hal.SysRoot, input.DevRoot = savedSys, savedDev })
	bitmap := "10000000000000 0\n"
	if strconv.IntSize == 32 {
		bitmap = "100000 0 0 0\n"
	}
	caps := filepath.Join(sys, "class", "input", "event0", "device", "capabilities", "key")
	if err := os.MkdirAll(filepath.Dir(caps), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caps, []byte(bitmap), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dev, "input"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dev, "input", "event0")
}

func powerEvent(v int32) []byte {
	size := 2*strconv.IntSize/8 + 8
	ev := make([]byte, size)
	binary.NativeEndian.PutUint16(ev[size-8:], 1)
	binary.NativeEndian.PutUint16(ev[size-6:], input.KeyPower)
	binary.NativeEndian.PutUint32(ev[size-4:], uint32(v))
	return ev
}

// livePowerButton is a power button whose events come as a person makes
// them: each (value, after) is written that long after the one before.
// The node is a FIFO held open for writing, so the step's reader opens it
// at once.
func livePowerButton(t *testing.T, events ...[2]int) {
	t.Helper()
	node := buttonDevice(t)
	if err := syscall.Mkfifo(node, 0o600); err != nil {
		t.Skip("no FIFO:", err)
	}
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	go func() {
		time.Sleep(100 * time.Millisecond)
		for _, e := range events {
			time.Sleep(time.Duration(e[1]) * time.Millisecond)
			if _, err := f.Write(powerEvent(int32(e[0]))); err != nil {
				return
			}
		}
	}()
}

// The button step asks for a press on its output and gives the Shell the
// key once the button has gone down and up: on standby the power key turns
// the screen off, the next press on again.
func TestButtonStep(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	buttonBoard(t, 1, 2, 0)
	if err := r.step("button", "power 2000"); err != nil {
		t.Fatal(err)
	}
	if !c.ScreenOff() {
		t.Fatal("the power button left the screen on")
	}
	if err := r.step("button", "power 2000"); err != nil {
		t.Fatal(err)
	}
	if c.ScreenOff() {
		t.Fatal("the second press left the screen off")
	}
	if want := "PHONE-SELFTEST-PRESS: power 1\nPHONE-SELFTEST-PRESS: power 2\n"; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}

	buttonBoard(t)
	if err := r.step("button", "power 200"); err == nil || !strings.Contains(err.Error(), "not pressed") {
		t.Errorf("no press: %v", err)
	}
	buttonBoard(t, 1)
	if err := r.step("button", "power 200"); err == nil || !strings.Contains(err.Error(), "not let go") {
		t.Errorf("held: %v", err)
	}
	if c.ScreenOff() {
		t.Error("a press not let go reached the Shell")
	}
	hal.SysRoot = t.TempDir()
	if err := r.step("button", "power 200"); err == nil || !strings.Contains(err.Error(), "no input device") {
		t.Errorf("no button: %v", err)
	}
	if err := r.step("button", "home"); err == nil {
		t.Error("an unknown button was pressed")
	}
}

// A case that needs a button runs only where the harness says it presses it.
func TestRequireButton(t *testing.T) {
	if !alone(t) {
		return
	}
	buttonBoard(t, 1, 0)
	script := "== press\nrequire-button power\nbutton power 2000\n"
	t.Setenv(ButtonsEnv, "")
	r := &Runner{C: snapCore(t), Out: &strings.Builder{}}
	if !r.Run(script) || len(r.Cases) != 1 || r.Cases[0].Status != "SKIP" {
		t.Fatalf("without %s: %+v", ButtonsEnv, r.Cases)
	}
	t.Setenv(ButtonsEnv, "power")
	r = &Runner{C: snapCore(t), Out: &strings.Builder{}}
	if !r.Run(script) || len(r.Cases) != 1 || r.Cases[0].Status != "PASS" {
		t.Fatalf("with %s=power: %+v", ButtonsEnv, r.Cases)
	}
}

// The power key's decision as the renderer makes it, from the kernel's
// events: short at the release before the hold time, long once the hold
// time passes with the key down, repeats ignored, rapid presses each a
// short press, and a lost release never leaving the key held.
func TestPowerHold(t *testing.T) {
	t0 := time.Now()
	ms := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }
	hold := 600 * time.Millisecond
	down := func() (bool, bool) { return true, true }
	unknown := func() (bool, bool) { return false, false }
	up := func() (bool, bool) { return false, true }

	var p powerHold
	p.down(ms(0))
	if k, ok := p.tick(ms(599), hold, down); ok {
		t.Fatalf("599 ms: %+v", k)
	}
	if k, ok := p.up(ms(599)); !ok || k.Long || k.Held != 599*time.Millisecond {
		t.Fatalf("let go at 599 ms: %+v, %v", k, ok)
	}

	p.down(ms(1000))
	if got := p.due(hold); !got.Equal(ms(1600)) {
		t.Fatalf("the long press due at %v", got.Sub(t0))
	}
	if k, ok := p.tick(ms(1600), hold, down); !ok || !k.Long {
		t.Fatalf("held 600 ms: %+v, %v", k, ok)
	}
	if k, ok := p.tick(ms(1700), hold, down); ok {
		t.Fatalf("a second long press: %+v", k)
	}
	if k, ok := p.up(ms(2000)); ok {
		t.Fatalf("the release after the long press: %+v", k)
	}

	// Repeats (a key that autorepeats) are ticks: nothing before the hold
	// time, the release is the short press.
	p.down(ms(3000))
	for at := 3250; at < 3550; at += 33 {
		if k, ok := p.tick(ms(at), hold, down); ok {
			t.Fatalf("a repeat at %d ms: %+v", at-3000, k)
		}
	}
	if k, ok := p.up(ms(3550)); !ok || k.Long {
		t.Fatalf("let go after repeats: %+v, %v", k, ok)
	}

	// The release was lost (the kernel has the key up at the hold time): a
	// short press, not the power menu; a late release changes nothing.
	p.down(ms(5000))
	if k, ok := p.tick(ms(5600), hold, up); !ok || k.Long {
		t.Fatalf("a lost release: %+v, %v", k, ok)
	}
	if k, ok := p.up(ms(5700)); ok {
		t.Fatalf("the release after it: %+v", k)
	}
	// Where the kernel cannot say, the events decide.
	p.down(ms(6000))
	if k, ok := p.tick(ms(6600), hold, unknown); !ok || !k.Long {
		t.Fatalf("held, the kernel unasked: %+v, %v", k, ok)
	}
	// A release that never comes is forgotten: the key is not held for good.
	if k, ok := p.tick(ms(6000).Add(powerLost), hold, unknown); ok || !p.since.IsZero() {
		t.Fatalf("after %s: %+v, still held %v", powerLost, k, !p.since.IsZero())
	}
	// A press after a lost release starts over.
	p.down(ms(20000))
	p.down(ms(20300))
	if k, ok := p.up(ms(20400)); !ok || k.Long || k.Held != 100*time.Millisecond {
		t.Fatalf("pressed again without a release between: %+v, %v", k, ok)
	}

	// Ten presses 150 ms apart, each down 60 ms: ten short presses.
	shorts := 0
	for i := 0; i < 10; i++ {
		at := 30000 + 150*i
		p.down(ms(at))
		if _, ok := p.tick(ms(at+30), hold, down); ok {
			t.Fatal("a long press in a burst")
		}
		if k, ok := p.up(ms(at + 60)); ok && !k.Long {
			shorts++
		}
	}
	if shorts != 10 {
		t.Fatalf("%d short presses of 10", shorts)
	}
}

// The button held past the long press: the Shell gets the long press (the
// power menu), as from the renderer, once. Before, a held button only
// failed the step.
func TestButtonHeld(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	r := &Runner{C: c, Out: &strings.Builder{}}
	livePowerButton(t, [2]int{1, 0}, [2]int{0, 900})
	if err := r.step("button", "power 3000"); err != nil {
		t.Fatal(err)
	}
	if m, ok := overlayOf[*menuOverlay](c); !ok || m.title != tr.T("Power") {
		t.Fatal("holding the button opened no power menu")
	}
	if c.ScreenOff() {
		t.Fatal("the long press turned the screen off")
	}
}

// The button pressed again and again: each press goes to the Shell as it
// comes, a short press each; five turn the screen off, on, off, on, off.
func TestButtonBurst(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	var evs [][2]int
	for i := 0; i < 5; i++ {
		evs = append(evs, [2]int{1, 80}, [2]int{0, 40})
	}
	livePowerButton(t, evs...)
	if err := r.step("button-burst", "power 5 120 5000"); err != nil {
		t.Fatal(err)
	}
	if !c.ScreenOff() {
		t.Error("five presses left the screen on")
	}
	if _, ok := overlayOf[*menuOverlay](c); ok {
		t.Error("a burst of short presses opened a menu")
	}
	if want := "PHONE-SELFTEST-PRESS: power 1 x5 120ms\n"; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}
}
