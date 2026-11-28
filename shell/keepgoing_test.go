package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kario-os/kario/hal/fingerprint"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

const keepGoingScript = "== one\nsleep 10\n== two\nwait not-on-the-screen 50\nsleep 10\n== three\nsleep 10\n== four\nwait nothing-here 50\n"

func caseLine(cases []CaseResult) string {
	var s []string
	for _, c := range cases {
		s = append(s, c.Name+" "+c.Status)
	}
	return strings.Join(s, ", ")
}

// By default a run stops at its first failure.
func TestSelftestStopsAtTheFirstFailure(t *testing.T) {
	if !alone(t) {
		return
	}
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	if r.Run(keepGoingScript) {
		t.Fatal("passed")
	}
	if got := caseLine(r.Cases); got != "one PASS, two FAIL" {
		t.Errorf("cases %s\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "PHONE-SELFTEST-FAILED: two: line 4: wait not-on-the-screen 50: ") ||
		!strings.Contains(out.String(), "PHONE-SELFTEST-RESULT: FAIL (1/2 cases)") {
		t.Errorf("summary:\n%s", out.String())
	}
}

// --keep-going: after a failed case the phone starts over (here: a new
// Core, as a new process would have) and the next case runs; the run
// still fails, the summary and the JUnit report naming every failure. A
// failure in the last case starts nothing over.
func TestSelftestKeepsGoing(t *testing.T) {
	if !alone(t) {
		return
	}
	resume := filepath.Join(t.TempDir(), "resume.json")
	var out strings.Builder
	starts := 0
	runner := func(res *Resume) *Runner {
		out.Reset()
		r := &Runner{C: snapCore(t), Out: &out, ResumeFile: resume, Resume: res}
		r.KeepGoing = func(path string) error {
			starts++
			if path != resume {
				t.Errorf("resume file %s", path)
			}
			return os.ErrProcessDone
		}
		return r
	}
	r := runner(nil)
	r.Run(keepGoingScript)
	if got := caseLine(r.Cases); starts != 1 || got != "one PASS, two FAIL" {
		t.Fatalf("%d starts, cases %s\n%s", starts, got, out.String())
	}
	if !strings.Contains(out.String(), "PHONE-SELFTEST-KEEP-GOING: the phone starts over for the case at line 6") {
		t.Errorf("no note of the start over:\n%s", out.String())
	}
	res, err := LoadResume(resume)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rebooted || res.Line != 5 || res.Case != "(setup)" {
		t.Errorf("resume %+v", res)
	}

	r = runner(res)
	ok := r.Run(keepGoingScript)
	if got := caseLine(r.Cases); ok || starts != 1 || got != "one PASS, two FAIL, three PASS, four FAIL" {
		t.Fatalf("ok %v, %d starts, cases %s\n%s", ok, starts, got, out.String())
	}
	for _, want := range []string{"PHONE-SELFTEST-FAILED: two: line 4", "PHONE-SELFTEST-FAILED: four: line 9", "PHONE-SELFTEST-RESULT: FAIL (2/4 cases)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out.String())
		}
	}
	junit := filepath.Join(t.TempDir(), "junit.xml")
	if err := WriteJUnit(junit, "phone-selftest", r.Cases); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(junit)
	if !strings.Contains(string(raw), `tests="4" failures="2"`) {
		t.Errorf("junit:\n%s", raw)
	}
}

// keepPhone is the phone the keep-going tests start over: a Shell on the
// same state and run directories each time, with phone-keystored's store
// (served on its socket) opened afresh from the state directory, as the
// start over does.
type keepPhone struct {
	t                *testing.T
	root, run, state string
	ks               atomic.Pointer[keystore.Store]
}

func newKeepPhone(t *testing.T) *keepPhone {
	p := &keepPhone{t: t, root: t.TempDir()}
	run, err := os.MkdirTemp("", "keepgoing-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	p.run, p.state = run, filepath.Join(p.root, "state")
	app := filepath.Join(p.root, "apps", "test.still")
	_ = os.MkdirAll(app, 0o755)
	if err := os.WriteFile(filepath.Join(app, "manifest.json"), []byte(`{"id":"test.still","name":"Still","exec":"none"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p.openKeystore()
	go wire.Serve(filepath.Join(run, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var a struct {
			Old, PIN  string
			Challenge uint64
		}
		_ = json.Unmarshal(params, &a)
		ks := p.ks.Load()
		switch method {
		case keystore.MethodLockStatus:
			return ks.LockStatus(), nil
		case keystore.MethodLockSetPIN:
			return ks.SetLockPIN(a.Old, a.PIN)
		case keystore.MethodLockClearPIN:
			return ks.ClearLockPIN(a.Old)
		case keystore.MethodLockVerify:
			return ks.VerifyLockPIN(a.PIN, a.Challenge)
		}
		return nil, errors.New("unknown method " + method)
	})
	return p
}

func (p *keepPhone) openKeystore() {
	ks, err := keystore.Open(p.state)
	if err != nil {
		p.t.Fatal(err)
	}
	p.ks.Store(ks)
}

func (p *keepPhone) core() (*Core, func()) {
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(p.root, "apps"), AppBinDir: filepath.Join(p.root, "bin"),
		RunDir: p.run, StateDir: p.state, FreezeAfter: -1})
	if err != nil {
		p.t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(c.Shutdown) }
	p.t.Cleanup(stop)
	return c, stop
}

// keepGoing runs the script with --keep-going as phone-shell does it: after a
// failed case everything stops, the state is put back as the case found
// it, and a new Shell (and keystored) goes on at the next case. The script
// is made once the first Shell has set the language.
func (p *keepPhone) keepGoing(script func() string) (*Runner, string) {
	keep := &StateCopy{Dirs: []string{p.state}, Dir: filepath.Join(p.root, "case-start")}
	resume := filepath.Join(p.root, "resume.json")
	var res *Resume
	var out strings.Builder
	text := ""
	for starts := 0; ; starts++ {
		c, stop := p.core()
		if text == "" {
			text = script()
		}
		r := &Runner{C: c, Out: &out, ResumeFile: resume, Resume: res, Keep: keep}
		again := false
		r.KeepGoing = func(string) error {
			again = true
			return os.ErrProcessDone
		}
		r.Run(text)
		stop()
		if !again || starts == 5 {
			return r, out.String()
		}
		if _, err := keep.Restore(); err != nil {
			p.t.Fatal(err)
		}
		p.openKeystore()
		var err error
		if res, err = LoadResume(resume); err != nil {
			p.t.Fatal(err)
		}
	}
}

// --keep-going starts the phone over from the state as the failed case
// found it: a lock-screen PIN the failed case set is gone, a file it put
// into Files too; a PIN an earlier case set stays, so the phone starts
// locked, and the next case unlocks it with the PIN (unlock).
func TestKeepGoingStartsFromTheStateTheCaseFound(t *testing.T) {
	if !alone(t) {
		return
	}
	p := newKeepPhone(t)
	r, out := p.keepGoing(func() string {
		setPIN := fmt.Sprintf("setting lock_pin 1\nwait %s\ntype 2468\nkey enter\nwait %s\ntype 2468\nkey enter\nwait %s\n",
			tr.T("Set a lock-screen PIN"), tr.T("The new PIN once more"), tr.T("Lock-screen PIN set: the phone locks when the screen goes off"))
		return "== sets a PIN and fails\n" + setPIN + "file-put during.txt y\nwait not-on-the-screen 50\n" +
			"== no PIN\nlocked off 100\nfiles during absent 100\nfile-put before.txt x\n" +
			"== sets a PIN\n" + setPIN +
			"== fails with the PIN set\nfile-put during.txt y\nwait not-on-the-screen 50\n" +
			"== starts locked\nlocked on 100\nunlock 2468\nlocked off 100\nfiles during absent 100\nfiles before present 100\n"
	})
	want := "sets a PIN and fails FAIL, no PIN PASS, sets a PIN PASS, fails with the PIN set FAIL, starts locked PASS"
	if got := caseLine(r.Cases); got != want {
		t.Fatalf("cases %s, want %s\n%s", got, want, out)
	}
	if !p.ks.Load().LockStatus().PIN {
		t.Error("the PIN set before the failed case is gone")
	}
	// Counted across the start overs.
	if !strings.Contains(out, "PHONE-SELFTEST-KEPT: the state at 5 case starts") {
		t.Errorf("no report of the copies:\n%s", out)
	}
}

// require-previous: a case going on where the one before it stopped is
// skipped when that one failed, or was skipped for it; cases skipped for
// the environment in between do not count, nor does a case that ran
// through with snapshots different from the baselines.
func TestRequirePrevious(t *testing.T) {
	if !alone(t) {
		return
	}
	script := "== one\nsleep 10\n== two\nrequire-previous\nsleep 10\n== three\nrequire-env KARIO_TEST_NEVER_SET\n" +
		"== four\nrequire-previous\nsleep 10\n== five\nwait nothing-here 50\n== six\nrequire-previous\nsleep 10\n" +
		"== seven\nrequire-previous\nsleep 10\n== eight\nsleep 10\n"
	resume := filepath.Join(t.TempDir(), "resume.json")
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out, ResumeFile: resume}
	r.KeepGoing = func(string) error { return os.ErrProcessDone }
	r.Run(script)
	res, err := LoadResume(resume)
	if err != nil {
		t.Fatal(err)
	}
	r = &Runner{C: snapCore(t), Out: &out, ResumeFile: resume, Resume: res}
	r.Run(script)
	want := "one PASS, two PASS, three SKIP, four PASS, five FAIL, six SKIP, seven SKIP, eight PASS"
	if got := caseLine(r.Cases); got != want {
		t.Fatalf("cases %s, want %s\n%s", got, want, out.String())
	}
	if m := r.Cases[5].Message; m != previousFailed+": five" {
		t.Errorf("six skipped for %q", m)
	}
	if !ranThrough(CaseResult{Status: "FAIL", Message: snapsDiffer + ": 02-chat"}) {
		t.Error("a case whose snapshots differ did not run through")
	}
}

// The requirements on what a board simulates and its kernel: the
// fingerprint sensor (keystored's), the light sensor, the location
// receiver, the protections the kernel probe found missing.
func TestRequireWhatTheBoardSimulates(t *testing.T) {
	if !alone(t) {
		return
	}
	p := newKeepPhone(t)
	c, _ := p.core()
	c.kernel.Missing = map[string]string{"landlock": "no Landlock (Linux 5.13)"}
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	r.Run("== finger\nrequire-fingerprint\n== kernel\nrequire-kernel cgroup-freezer landlock\n== kernel ok\nrequire-kernel cgroup-freezer\n" +
		"== gnss\nrequire-gnss sim fifo\n== gnss other\nrequire-gnss fifo modemmanager\n== lux\nrequire-lux\n")
	want := "finger SKIP, kernel SKIP, kernel ok PASS, gnss PASS, gnss other SKIP, lux PASS"
	if got := caseLine(r.Cases); got != want {
		t.Fatalf("cases %s, want %s\n%s", got, want, out.String())
	}
	if m := r.Cases[0].Message; !strings.Contains(m, "no sensor") {
		t.Errorf("finger skipped for %q", m)
	}
	if m := r.Cases[1].Message; m != "needs kernel landlock: no Landlock (Linux 5.13)" {
		t.Errorf("kernel skipped for %q", m)
	}
	p.ks.Load().SetSensor(fingerprint.NewSim())
	c.lockRefresh(nil)
	c.screen.alsSim = false
	r.Run("== finger\nrequire-fingerprint\nsleep 200\n== lux\nrequire-lux\n")
	if got := caseLine(r.Cases); got != "finger PASS, lux SKIP" {
		t.Fatalf("with the simulated sensor: %s\n%s", got, out.String())
	}
}
