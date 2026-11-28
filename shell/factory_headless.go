package shell

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/factory"
	"github.com/kario-os/kario/keystore"
)

// factoryRun is the selftest's factory mode: the runner in this process
// over a fake unit of a test SKU (factory.FakeSKU), so the factory screen's
// cases run on every form factor without the factory boot.
type factoryRun struct {
	dir  string
	fake *factory.Fake
	unit *factory.Unit
}

// factorySKU is the test SKU of the selftest's form factor: the pixel
// renderer's device, else (the text renderer's) the keypad's input mode.
func (r *Runner) factorySKU() string {
	kp := r.C.screenDev.Keypad
	if kp == "" {
		kp = r.C.cfg.Keypad
	}
	switch {
	case r.C.cfg.SKU.Lid != "":
		return "ref-flip-qcm6490-4g"
	case kp == "qwerty" || kp == "full":
		return "ref-qwerty-qcm6490-4g"
	}
	return "ref-t9-qcm6490-4g"
}

func (r *Runner) factoryStep(cmd, arg string) error {
	f := strings.Fields(arg)
	switch {
	case cmd == "factory" && len(f) >= 1 && f[0] == "on":
		id := r.factorySKU()
		if len(f) > 1 {
			id = f[1]
		}
		return r.factoryOn(id)
	case cmd == "factory" && len(f) == 1 && f[0] == "off":
		r.factoryOff()
		return nil
	case cmd == "factory-result" && len(f) >= 2:
		want := f[1]
		if want == "none" {
			want = ""
		}
		ms := 5000
		if len(f) > 2 {
			ms, _ = strconv.Atoi(f[2])
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.FactoryResult(f[0]) == want }, ms); !ok {
			return fmt.Errorf("the factory screen's %s is %q, not %q", f[0], r.C.FactoryResult(f[0]), want)
		}
		return nil
	case cmd == "factory-usb" && len(f) == 1:
		want := f[0] == "up"
		if _, ok := r.waitFor(func(string) bool { up, _, _ := r.C.FactoryUSB(); return up == want }, 5000); !ok {
			return fmt.Errorf("the station's USB link is not %s", f[0])
		}
		up, tcp, udp := r.C.FactoryUSB()
		switch {
		case r.C.usb.prompt != nil:
			return errors.New("the trust prompt is up in factory mode")
		case up && (!slices.Equal(tcp, []uint16{factory.Port}) || !slices.Equal(udp, []uint16{67})):
			return fmt.Errorf("the link reaches tcp %v, udp %v: only tcp %d and udp 67 in factory mode", tcp, udp, factory.Port)
		case !up && (len(tcp) > 0 || len(udp) > 0):
			return fmt.Errorf("the link still reaches tcp %v, udp %v", tcp, udp)
		}
		return nil
	case cmd == "factory-station" && len(f) >= 1:
		return r.factoryStation(f)
	case cmd == "factory-keys" && len(f) <= 1:
		return r.factoryKeys(f)
	case cmd == "factory-screen" && (len(f) == 1 || len(f) == 2 && f[0] == "touch"):
		return r.factoryScreen(f)
	}
	return fmt.Errorf("%s %s: see the Script's factory steps", cmd, arg)
}

func (r *Runner) factoryOn(id string) error {
	r.factoryOff()
	s, err := factory.FakeSKU(id)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kf-")
	if err != nil {
		return err
	}
	run := &factoryRun{dir: dir}
	fail := func(err error) error {
		if run.fake != nil {
			run.fake.Close()
		}
		os.RemoveAll(dir)
		return err
	}
	if run.fake, err = factory.NewFake(dir, s, factory.FakeOptions{Keystore: true}); err != nil {
		return fail(err)
	}
	devID, err := run.fake.Env.Gate()
	if err != nil {
		return fail(err)
	}
	srv := factory.NewServer(run.fake.Env, s, devID)
	sock := filepath.Join(run.fake.Env.RunDir, factory.Sock)
	ln, err := factory.ListenSock(sock)
	if err != nil {
		return fail(err)
	}
	go func() { _ = factory.Serve(ln, srv.Local()) }()
	run.unit = factory.Dial("unix:" + sock)
	r.factory = run
	r.C.EnterFactory(run.unit, func() {
		ln.Close()
		run.fake.Close()
		os.RemoveAll(dir)
	})
	r.C.factory.offAfter = time.Hour
	if _, ok := r.waitFor(func(string) bool { return r.C.factory != nil && r.C.factory.plan != nil }, 5000); !ok {
		return errors.New("the factory screen has no plan from the runner")
	}
	return nil
}

func (r *Runner) factoryOff() {
	r.C.LeaveFactory()
	r.factory = nil
}

// factoryStation is the station's side over the runner's API: a test
// (its exit code wanted, every check printed as PHONE-FACTORY), the
// provisioning, or hardware taken away from the fake unit.
func (r *Runner) factoryStation(f []string) error {
	run := r.factory
	if run == nil {
		return errors.New("factory-station: not in factory mode (factory on)")
	}
	switch {
	case f[0] == "test" && len(f) == 3:
		want, _ := strconv.Atoi(f[2])
		rep, err := run.unit.Test(f[1], 0)
		if err != nil && !errors.Is(err, factory.ErrRefused) {
			return err
		}
		if rep == nil {
			rep = &factory.Report{Refused: "refused"}
		}
		for _, ch := range rep.Checks {
			fmt.Fprintf(r.Out, "PHONE-FACTORY: %s %s %s %s\n", ch.ID, ch.Result, ch.Value, ch.Reason)
		}
		if rep.Refused != "" {
			fmt.Fprintf(r.Out, "PHONE-FACTORY: refused: %s\n", rep.Refused)
		}
		if got := rep.ExitCode(); got != want {
			return fmt.Errorf("the test's exit code is %d, not %d", got, want)
		}
	case f[0] == "provision" && len(f) == 1:
		dir := filepath.Join(run.dir, "ca")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		ca, err := keystore.LoadCA(dir)
		if err != nil {
			return err
		}
		var b [16]byte
		_, _ = rand.Read(b[:])
		if _, err := factory.Provision(run.unit, ca, hex.EncodeToString(b[:])); err != nil {
			return err
		}
	case f[0] == "remove" && len(f) == 2:
		return run.fake.Remove(f[1])
	default:
		return errors.New("factory-station test auto|full <exit> | provision | remove <check>")
	}
	return nil
}

// factoryKeys presses n (or all) of the keys the keys check still waits
// for, as the keypad sends them: the SKUs' keys differ per form factor.
func (r *Runner) factoryKeys(f []string) error {
	fm := r.C.factory
	if fm == nil || fm.screen != "keys" {
		return errors.New("factory-keys: the factory screen's keys check is not open")
	}
	ch, _ := fm.check("keys")
	left := fm.keysLeft(ch)
	if len(f) == 1 {
		n, err := strconv.Atoi(f[0])
		if err != nil || n < 0 || n > len(left) {
			return fmt.Errorf("factory-keys: %d keys left", len(left))
		}
		left = left[:n]
	}
	for _, k := range left {
		r.press(factoryKeyFor(k))
		r.pump(40 * time.Millisecond)
	}
	return nil
}

// factoryScreen waits until the factory screen shows check id (home: its
// list) and, for touch, n squares touched: what the full-panel patterns
// show is not text the wait step can find.
func (r *Runner) factoryScreen(f []string) error {
	id, n := f[0], -1
	if id == "home" {
		id = ""
	}
	if len(f) == 2 {
		var err error
		if n, err = strconv.Atoi(f[1]); err != nil || n < 0 {
			return fmt.Errorf("factory-screen touch %s: a number of squares", f[1])
		}
	}
	at := func(string) bool {
		fm := r.C.factory
		return fm != nil && fm.screen == id && (n < 0 || len(fm.cells)-fm.untouched() == n)
	}
	if _, ok := r.waitFor(at, 5000); !ok {
		if fm := r.C.factory; fm != nil && fm.screen == "touch" {
			return fmt.Errorf("the factory screen is touch, %d of %d squares touched", len(fm.cells)-fm.untouched(), len(fm.cells))
		} else if fm != nil {
			return fmt.Errorf("the factory screen is %q, not %q", fm.screen, id)
		}
		return errors.New("factory-screen: not in factory mode")
	}
	return nil
}

// factoryKeyFor is the Shell's key for a key of keys.toml, as the keypad
// sends it.
func factoryKeyFor(name string) Key {
	switch name {
	case "soft-left":
		return Key{Name: KeyTab}
	case "soft-right":
		return Key{Name: KeyF2}
	case "ok":
		return Key{Name: KeyEnter}
	case "back":
		return Key{Name: KeyEsc}
	case "clear":
		return Key{Name: KeyBackspace}
	case "end":
		return Key{Name: KeyF4}
	case "vol-up":
		return Key{Name: KeyVolUp}
	case "vol-down":
		return Key{Name: KeyVolDown}
	case "shift":
		return Key{Name: KeyShiftOnce}
	case "fn":
		return Key{Name: KeyFnOnce}
	case "star":
		return Runes("*")
	case "pound":
		return Runes("#")
	case "space":
		return Runes(" ")
	case "comma":
		return Runes(",")
	case "dot":
		return Runes(".")
	}
	if len(name) == 1 {
		return Runes(name)
	}
	return Key{Name: name}
}
