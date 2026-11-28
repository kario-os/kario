package shell

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/hal/input"
)

// ButtonsEnv names the board's buttons someone presses for the selftest
// when it asks (the button step): QEMU's harness presses the virt
// machine's power button from its monitor (yocto/qemu-selftest.sh). The
// cases that need a button skip where it is not named.
const ButtonsEnv = "PHONE_SELFTEST_BUTTONS"

// buttons are the board's buttons the button step knows: the kernel's code
// and the key the renderer gives the Shell for it.
var buttons = map[string]struct {
	code int
	key  string
}{
	"power": {input.KeyPower, KeyPower},
}

// powerHold tells the power key's short press from its long one by the
// kernel's events, as phone-paint does (paint/src/kms.rs, PowerKey): short
// at the release before the hold time, long once the hold time has passed
// with the key still down (most boards' power key does not repeat, so the
// time alone decides), never both; repeats change nothing else. A press
// whose release never comes does not leave the key held: the long press
// goes only if the kernel, asked, has the key down still (else the
// release was lost and it was a short press), a new press starts over,
// and one held past powerLost is forgotten.
type powerHold struct {
	since time.Time // down since; zero while up
	long  bool      // the long press has gone
}

// powerLost: no release this long after a press: it was lost.
const powerLost = 10 * time.Second

func (p *powerHold) down(now time.Time) { p.since, p.long = now, false }

// up: the release, the short press unless the long one went.
func (p *powerHold) up(now time.Time) (Key, bool) {
	if p.since.IsZero() {
		return Key{}, false
	}
	held, long := now.Sub(p.since), p.long
	p.since, p.long = time.Time{}, false
	if long {
		return Key{}, false
	}
	return Key{Name: KeyPower, Held: held}, true
}

// due is when tick has something to do, zero while the key is up.
func (p *powerHold) due(hold time.Duration) time.Time {
	switch {
	case p.since.IsZero():
		return time.Time{}
	case p.long:
		return p.since.Add(powerLost)
	}
	return p.since.Add(hold)
}

// tick: time has passed (a repeat is one too). isDown is the kernel's word
// on the key (known false: it cannot say).
func (p *powerHold) tick(now time.Time, hold time.Duration, isDown func() (down, known bool)) (Key, bool) {
	if p.since.IsZero() {
		return Key{}, false
	}
	held := now.Sub(p.since)
	switch {
	case !p.long && held >= hold:
		if down, known := isDown(); known && !down {
			p.since = time.Time{}
			return Key{Name: KeyPower}, true
		}
		p.long = true
		return Key{Name: KeyPower, Long: true, Held: held}, true
	case p.long && held >= powerLost:
		p.since, p.long = time.Time{}, false
	}
	return Key{}, false
}

// button <name> [ms]: the board's own button pressed for real, given to
// the Shell as the renderer gives it (powerHold). The headless renderer
// reads no keys, so the step reads the kernel's input devices that have
// the key, from before it asks for the press (a PHONE-SELFTEST-PRESS
// request on the output, which the harness watches; ask, said again until
// the key goes down) until the press has gone to the Shell and the key is
// up.
func (r *Runner) button(arg string) error {
	name, ms := lastNum(strings.TrimSpace(arg), 20000)
	return r.buttonPresses(strings.TrimSpace(name), 1, 0, ms)
}

// button-burst <name> <presses> <gap ms> [ms]: the button pressed that
// many times that far apart (from one press to the next), each given to
// the Shell as it comes: a person pressing again and again.
func (r *Runner) buttonBurst(arg string) error {
	f := strings.Fields(arg)
	if len(f) < 3 {
		return fmt.Errorf("button-burst <name> <presses> <gap ms> [ms]")
	}
	n, err1 := strconv.Atoi(f[1])
	gap, err2 := strconv.Atoi(f[2])
	ms := 20000
	if len(f) > 3 {
		ms, _ = strconv.Atoi(f[3])
	}
	if err1 != nil || err2 != nil || n < 1 || gap < 0 {
		return fmt.Errorf("button-burst <name> <presses> <gap ms> [ms]")
	}
	return r.buttonPresses(f[0], n, time.Duration(gap)*time.Millisecond, ms)
}

func (r *Runner) buttonPresses(name string, n int, gap time.Duration, ms int) error {
	b, ok := buttons[name]
	if !ok {
		return fmt.Errorf("button: no button %q", name)
	}
	nodes := input.WithKey(b.code)
	if len(nodes) == 0 {
		return fmt.Errorf("button %s: no input device has key %d", name, b.code)
	}
	type event struct {
		k input.Key
		w *input.Watcher
	}
	keys := make(chan event, 64)
	var watchers []*input.Watcher
	for _, node := range nodes {
		w, err := input.Watch(node)
		if err != nil {
			return fmt.Errorf("button %s: %w", name, err)
		}
		defer w.Close()
		watchers = append(watchers, w)
		go func() {
			for k := range w.C {
				select {
				case keys <- event{k, w}:
				default:
				}
			}
		}()
	}
	r.presses++
	line := fmt.Sprintf("PHONE-SELFTEST-PRESS: %s %d", name, r.presses)
	if n > 1 {
		line += fmt.Sprintf(" x%d %dms", n, gap.Milliseconds())
	}
	again := r.ask("press", r.presses, line)
	isDown := func() (bool, bool) {
		for _, w := range watchers {
			if d, err := w.Down(b.code); err == nil {
				return d, true
			}
		}
		return false, false
	}
	var hold powerHold
	given, downs := 0, 0
	give := func(k Key, how string) {
		given++
		r.C.Trace("button %s: %s, given to the Shell (%d of %d)", name, how, given, n)
		r.press(k)
	}
	if _, ok := r.waitFor(func(string) bool {
		for {
			select {
			case e := <-keys:
				if e.k.Code != b.code {
					continue
				}
				now := time.Now()
				if e.k.Down {
					downs++
					hold.down(now)
					continue
				}
				if k, ok := hold.up(now); ok {
					give(k, fmt.Sprintf("short press (held %d ms)", k.Held.Milliseconds()))
				}
			default:
				if downs == 0 {
					again()
				}
				if k, ok := hold.tick(time.Now(), r.C.powerLong(), isDown); ok {
					how := "long press"
					if !k.Long {
						how = "short press (its release lost)"
					}
					give(k, how)
				}
				return given >= n && hold.since.IsZero()
			}
		}
	}, ms); !ok {
		if downs == 0 {
			return fmt.Errorf("button %s: not pressed (%s)", name, strings.Join(nodes, ", "))
		}
		if !hold.since.IsZero() {
			return fmt.Errorf("button %s: pressed and not let go (%d of %d presses given to the Shell)", name, given, n)
		}
		return fmt.Errorf("button %s: %d of %d presses", name, given, n)
	}
	r.pump(40 * time.Millisecond)
	return nil
}
