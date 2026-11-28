package factory

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/keystore"
)

func loadSKU(t *testing.T, name string) SKU {
	t.Helper()
	s, err := LoadSKU(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newFake builds a unit in a short directory (unix socket paths have a
// length limit that t.TempDir's can pass).
func newFake(t *testing.T, s SKU, withKeystore bool) *Fake {
	t.Helper()
	dir, err := os.MkdirTemp("", "ft")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f, err := NewFake(dir, s, FakeOptions{Keystore: withKeystore})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

type planWant struct {
	required, optional []string // applicable checks
}

func applicable(plan []Check) (required, optional []string) {
	for _, c := range plan {
		switch {
		case c.Skip != "":
		case c.Required:
			required = append(required, c.ID)
		default:
			optional = append(optional, c.ID)
		}
	}
	return
}

func TestPlanPerSKU(t *testing.T) {
	cases := map[string]planWant{
		// The emulator (bar-t9 with a touchscreen): its keyboard, the
		// virtual parts, telephonyd's simulated modem; Bluetooth optional
		// (btvirt runs only in the selftest).
		"qemu-t9": {
			required: []string{"input.keypad", "input.touch", "display.mode", "audio.card", "rtc", "memory", "wifi", "modem", "sim", "keystore",
				"keys", "display.patterns", "touch", "speaker", "camera.back"},
			optional: []string{"bluetooth"},
		},
		// A bar-t9 reference phone: the TCA8418 keypad, no touch.
		"ref-t9-qcm6490-4g": {
			required: []string{"input.keypad", "display.mode", "audio.card", "vibrator", "sensor.light", "sensor.proximity", "sensor.accelerometer",
				"battery", "rtc", "memory", "wifi", "bluetooth", "modem", "sim", "keystore",
				"keys", "display.patterns", "speaker", "vibrator.felt", "camera.back", "camera.front"},
		},
		// The flip: its lid's switch; one camera.
		"ref-flip-qcm6490-4g": {
			required: []string{"input.keypad", "input.lid", "display.mode", "audio.card", "vibrator", "sensor.light", "sensor.proximity",
				"sensor.accelerometer", "battery", "rtc", "memory", "wifi", "bluetooth", "modem", "sim", "keystore",
				"keys", "display.patterns", "speaker", "vibrator.felt", "camera.back"},
		},
		// The OnePlus 6T: buttons and the alert slider, no keypad of its
		// own (a Bluetooth keyboard), touch, its sound card's UCM verbs and
		// microphone; the cameras optional.
		"oneplus-fajita-t9": {
			required: []string{"input.buttons", "input.sound-switch", "input.touch", "display.mode", "audio.card", "audio.ucm", "vibrator",
				"battery", "rtc", "storage", "memory", "wifi", "bluetooth", "modem", "sim", "keystore",
				"keys", "display.patterns", "touch", "speaker", "microphone", "vibrator.felt"},
			optional: []string{"camera.back", "camera.front"},
		},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			plan := Plan(loadSKU(t, name))
			if len(plan) != len(catalogue) {
				t.Fatalf("plan has %d checks, the catalogue %d", len(plan), len(catalogue))
			}
			req, opt := applicable(plan)
			if !slices.Equal(req, want.required) {
				t.Errorf("required:\n got %v\nwant %v", req, want.required)
			}
			if !slices.Equal(opt, want.optional) {
				t.Errorf("optional: got %v, want %v", opt, want.optional)
			}
			for _, c := range plan {
				if c.Skip != "" && c.Required {
					t.Errorf("%s: skipped yet required", c.ID)
				}
			}
		})
	}
}

func TestPlanKeys(t *testing.T) {
	for name, want := range map[string][]string{
		"oneplus-fajita-t9": {"power", "vol-down", "vol-up"},
		"qemu-t9":           {"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "back", "call", "clear", "down", "end", "left", "ok", "pound", "right", "soft-left", "soft-right", "star", "up"},
	} {
		plan := Plan(loadSKU(t, name))
		i := slices.IndexFunc(plan, func(c Check) bool { return c.ID == "keys" })
		if !slices.Equal(plan[i].Keys, want) {
			t.Errorf("%s: keys %v, want %v", name, plan[i].Keys, want)
		}
	}
	flip := Plan(loadSKU(t, "ref-flip-qcm6490-4g"))
	i := slices.IndexFunc(flip, func(c Check) bool { return c.ID == "keys" })
	for _, k := range []string{"vol-up", "vol-down", "power", "ok", "5"} {
		if !slices.Contains(flip[i].Keys, k) {
			t.Errorf("flip: no key %s in %v", k, flip[i].Keys)
		}
	}
}

// tools/sku.py checks an SKU's optional against the same ids.
func TestCheckIDsMatchSkuPy(t *testing.T) {
	raw, err := os.ReadFile("../tools/sku.py")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)FACTORY_CHECKS = \((.*?)\)\n`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no FACTORY_CHECKS in tools/sku.py")
	}
	var ids []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		ids = append(ids, string(q[1]))
	}
	if !slices.Equal(ids, CheckIDs()) {
		t.Errorf("tools/sku.py FACTORY_CHECKS:\n%v\nfactory's catalogue:\n%v", ids, CheckIDs())
	}
	if len(autos) != len(Only(Plan(SKU{}), Auto)) {
		t.Errorf("%d automatic measures for %d automatic checks", len(autos), len(Only(Plan(SKU{}), Auto)))
	}
}

func TestOptional(t *testing.T) {
	for _, c := range []struct {
		names []string
		id    string
		want  bool
	}{
		{[]string{"camera.*"}, "camera.back", true},
		{[]string{"camera.*"}, "camera", false},
		{[]string{"vibrator.*"}, "vibrator", false},
		{[]string{"vibrator.*"}, "vibrator.felt", true},
		{[]string{"bluetooth"}, "bluetooth", true},
		{[]string{"bluetooth"}, "wifi", false},
	} {
		if got := optional(c.names, c.id); got != c.want {
			t.Errorf("optional(%v, %s) = %v", c.names, c.id, got)
		}
	}
}

func TestRequested(t *testing.T) {
	for cmdline, want := range map[string]bool{
		"root=/dev/vda kario.factory=1 quiet":    true,
		"root=/dev/vda":                          false,
		"kario.factory=0":                        false,
		"kario.factory=1 kario.factory=0":        false,
		"kario.factory=0 kario.factory=1":        true,
		"xkario.factory=1":                       false,
		"kario.factory=10":                       false,
		"console=ttyMSM0 kario.factory=1\n":      true,
		"phone.gui=1 vt.global_cursor_default=0": false,
	} {
		if got := Requested(cmdline); got != want {
			t.Errorf("Requested(%q) = %v", cmdline, got)
		}
	}
}

func TestExitCodes(t *testing.T) {
	pass := func(id string, req bool) CheckResult { return CheckResult{ID: id, Required: req, Result: Pass} }
	for _, c := range []struct {
		name   string
		checks []CheckResult
		refuse string
		want   int
	}{
		{"all pass", []CheckResult{pass("a", true), pass("b", true)}, "", ExitPass},
		{"required fails", []CheckResult{pass("a", true), {ID: "b", Required: true, Result: Fail}}, "", ExitFail},
		{"optional fails", []CheckResult{pass("a", true), {ID: "b", Result: Fail}}, "", ExitPass},
		{"required not done", []CheckResult{pass("a", true), {ID: "b", Required: true, Result: Skip}}, "", ExitFail},
		{"not applicable", []CheckResult{pass("a", true), {ID: "b", Result: Skip, Reason: "no such part"}}, "", ExitPass},
		{"refused", nil, "provisioned", ExitRefused},
	} {
		r := &Report{Checks: c.checks, Refused: c.refuse}
		r.Finish()
		if got := r.ExitCode(); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
		if r.Passed != (c.want == ExitPass) {
			t.Errorf("%s: passed %v", c.name, r.Passed)
		}
	}
}

func keysOf(m map[string]any) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// The report's fields are its schema (docs/40-testing.md §10.3): a change
// to them is a change to kario.factory-report/1.
func TestReportSchema(t *testing.T) {
	f := newFake(t, loadSKU(t, "oneplus-fajita-t9"), true)
	srv := NewServer(f.Env, f.SKU, "")
	rep := srv.Run(context.Background(), ModeAuto, 0)
	if rep.Schema != "kario.factory-report/1" || rep.SKU != "oneplus-fajita-t9" || rep.Board != "oneplus-fajita" || rep.Mode != "auto" {
		t.Fatalf("report header %+v", rep)
	}
	if id, _, _ := f.Store.CSR(); rep.DeviceID != id || id == "" {
		t.Errorf("device id %q, the keystore's %q", rep.DeviceID, id)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got, want := keysOf(doc), []string{"board", "checks", "device_id", "duration_ms", "mode", "passed", "schema", "sku", "started"}; !slices.Equal(got, want) {
		t.Errorf("report fields %v, want %v", got, want)
	}
	checks := doc["checks"].([]any)
	if len(checks) != len(Only(Plan(f.SKU), Auto)) {
		t.Errorf("%d checks in an auto report", len(checks))
	}
	allowed := []string{"duration_ms", "id", "kind", "reason", "required", "result", "value"}
	for _, c := range checks {
		m := c.(map[string]any)
		for _, k := range keysOf(m) {
			if !slices.Contains(allowed, k) {
				t.Errorf("check field %s", k)
			}
		}
		for _, k := range []string{"id", "kind", "required", "result", "duration_ms"} {
			if _, ok := m[k]; !ok {
				t.Errorf("check %v lacks %s", m["id"], k)
			}
		}
		if m["result"] == "skip" && m["reason"] == nil {
			t.Errorf("skipped check %v gives no reason", m["id"])
		}
	}
	doc40, err := os.ReadFile("../docs/40-testing.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range append(keysOf(doc), allowed...) {
		if !strings.Contains(string(doc40), "`"+k+"`") {
			t.Errorf("docs/40-testing.md does not document the report's %s", k)
		}
	}
	if !strings.Contains(string(doc40), Schema) {
		t.Errorf("docs/40-testing.md does not name %s", Schema)
	}
}

func TestReportWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "report.json")
	r := &Report{Schema: Schema, Checks: []CheckResult{{ID: "rtc", Result: Pass}}}
	r.Finish()
	if err := r.Write(path); err != nil {
		t.Fatal(err)
	}
	var back Report
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &back); err != nil || !back.Passed || back.Checks[0].ID != "rtc" {
		t.Fatalf("read back %+v %v", back, err)
	}
}

func provision(t *testing.T, f *Fake) {
	t.Helper()
	ca, err := keystore.LoadCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(KeystoreSock(filepath.Join(f.Env.RunDir, "keystored.sock")), ca, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestGateRefusesProvisioned(t *testing.T) {
	f := newFake(t, loadSKU(t, "qemu-t9"), true)
	mark := filepath.Join(f.Dir, "run/factory/on")
	id, err := f.Env.Enter(mark)
	if err != nil || id == "" || !Active(mark) {
		t.Fatalf("unprovisioned: Enter = %q, %v; mark %v", id, err, Active(mark))
	}
	provision(t, f)
	if _, err := f.Env.Gate(); !errors.Is(err, ErrProvisioned) {
		t.Fatalf("provisioned: Gate = %v", err)
	}
	if _, err := f.Env.Enter(mark); !errors.Is(err, ErrProvisioned) || Active(mark) {
		t.Fatalf("provisioned: Enter = %v, mark left %v", err, Active(mark))
	}
	rep := NewServer(f.Env, f.SKU, id).Run(context.Background(), ModeAuto, 0)
	if rep.Refused == "" || rep.ExitCode() != ExitRefused || len(rep.Checks) != 0 {
		t.Fatalf("provisioned: report refused %q exit %d with %d checks", rep.Refused, rep.ExitCode(), len(rep.Checks))
	}
}

func TestGateFailsClosed(t *testing.T) {
	f := newFake(t, loadSKU(t, "qemu-t9"), false)
	mark := filepath.Join(f.Dir, "run/factory/on")
	if err := os.MkdirAll(filepath.Dir(mark), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mark, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Env.Enter(mark); err == nil || errors.Is(err, ErrProvisioned) || Active(mark) {
		t.Fatalf("no keystored: Enter = %v, mark %v", err, Active(mark))
	}
	rep := NewServer(f.Env, f.SKU, "").Run(context.Background(), ModeFull, 0)
	if rep.ExitCode() != ExitRefused {
		t.Fatalf("no keystored: exit %d", rep.ExitCode())
	}
}

var allSKUs = []string{"qemu-t9", "ref-t9-qcm6490-4g", "ref-flip-qcm6490-4g", "oneplus-fajita-t9", "ref-qwerty-qcm6490-4g"}

// Every automatic check passes on a unit with its SKU's hardware, and
// fails with a reason once that hardware is gone; the exit code follows
// whether the check is required.
func TestAutoChecksAgainstFakes(t *testing.T) {
	for _, name := range allSKUs {
		t.Run(name, func(t *testing.T) {
			s := loadSKU(t, name)
			plan := Plan(s)
			f := newFake(t, s, true)
			srv := NewServer(f.Env, s, "")
			rep := srv.Run(context.Background(), ModeAuto, 0)
			for _, c := range rep.Checks {
				if c.Result == Fail {
					t.Errorf("%s fails on a whole unit: %s", c.ID, c.Reason)
				}
				if c.Result == Pass && c.Value == "" {
					t.Errorf("%s passes without a value", c.ID)
				}
			}
			if rep.ExitCode() != ExitPass {
				t.Fatalf("whole unit: exit %d", rep.ExitCode())
			}
			for _, c := range Only(plan, Auto) {
				if c.Skip != "" {
					continue
				}
				t.Run(c.ID, func(t *testing.T) {
					g := newFake(t, s, true)
					if err := g.Remove(c.ID); err != nil {
						t.Fatal(err)
					}
					rep := NewServer(g.Env, s, "").Run(context.Background(), ModeAuto, 0)
					if c.ID == "keystore" {
						// No keystore, no gate: nothing runs.
						if rep.ExitCode() != ExitRefused || !strings.Contains(rep.Refused, "keystored") {
							t.Errorf("no keystored: exit %d, refused %q", rep.ExitCode(), rep.Refused)
						}
						return
					}
					r, _ := rep.Result(c.ID)
					if r.Result != Fail || r.Reason == "" {
						t.Fatalf("missing hardware: %s %s %q", c.ID, r.Result, r.Reason)
					}
					want := ExitFail
					if !c.Required {
						want = ExitPass
					}
					if rep.ExitCode() != want {
						t.Errorf("missing %s: exit %d, want %d", c.ID, rep.ExitCode(), want)
					}
				})
			}
		})
	}
}

func TestAutoCheckDetails(t *testing.T) {
	s := loadSKU(t, "oneplus-fajita-t9")
	run := func(f *Fake, id string) CheckResult {
		t.Helper()
		res, _ := f.Env.RunAuto(context.Background(), s, Plan(s))
		for _, r := range res {
			if r.ID == id {
				return r
			}
		}
		t.Fatalf("no %s", id)
		return CheckResult{}
	}
	write := func(f *Fake, path, v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.Dir, path), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("display mode", func(t *testing.T) {
		f := newFake(t, s, true)
		write(f, "sys/class/drm/card0-DSI-1/modes", "1080x2280")
		if r := run(f, "display.mode"); r.Result != Fail || !strings.Contains(r.Reason, "1080x2340") {
			t.Errorf("wrong mode: %+v", r)
		}
	})
	t.Run("not charging", func(t *testing.T) {
		f := newFake(t, s, true)
		write(f, "sys/class/power_supply/battery/status", "Discharging")
		if r := run(f, "battery"); r.Result != Fail || !strings.Contains(r.Reason, "not charging") {
			t.Errorf("discharging on USB: %+v", r)
		}
		write(f, "sys/class/power_supply/usb/online", "0")
		if r := run(f, "battery"); r.Result != Fail || !strings.Contains(r.Reason, "plug USB in") {
			t.Errorf("no charger: %+v", r)
		}
	})
	t.Run("rtc stopped", func(t *testing.T) {
		f := newFake(t, s, true)
		f.Env.Sleep = func(time.Duration) {}
		if r := run(f, "rtc"); r.Result != Fail || !strings.Contains(r.Reason, "not running") {
			t.Errorf("stopped RTC: %+v", r)
		}
	})
	t.Run("storage small", func(t *testing.T) {
		f := newFake(t, s, true)
		write(f, "sys/block/mmcblk0/size", "1000000")
		if r := run(f, "storage"); r.Result != Fail || !strings.Contains(r.Reason, "less than the SKU's 100 GB") {
			t.Errorf("small disk: %+v", r)
		}
	})
	t.Run("sim locked", func(t *testing.T) {
		f := newFake(t, s, true)
		f.SetTelephony("fake", "pin")
		if r := run(f, "sim"); r.Result != Pass || r.Value != "SIM pin" {
			t.Errorf("PIN-locked SIM: %+v", r)
		}
	})
	t.Run("memory from meminfo", func(t *testing.T) {
		f := newFake(t, s, true)
		os.RemoveAll(filepath.Join(f.Dir, "sys/firmware/devicetree"))
		if r := run(f, "memory"); r.Result != Pass || !strings.Contains(r.Value, "MemTotal") {
			t.Errorf("meminfo: %+v", r)
		}
	})
	t.Run("ucm verb missing", func(t *testing.T) {
		f := newFake(t, s, true)
		f.verbs = []string{"HiFi"}
		if r := run(f, "audio.ucm"); r.Result != Fail || !strings.Contains(r.Reason, "Voice Call") {
			t.Errorf("no call verb: %+v", r)
		}
	})
}

func TestBitmap(t *testing.T) {
	set := []int{1, 63, 64, 116, 0x210}
	b := parseBitmap(formatBitmap(set...))
	for n := 0; n < 0x300; n++ {
		if b.has(n) != slices.Contains(set, n) {
			t.Errorf("bit %d", n)
		}
	}
	if parseBitmap("").has(0) || parseBitmap("0").has(0) {
		t.Error("empty bitmap has bits")
	}
}

func TestMicrophone(t *testing.T) {
	s := loadSKU(t, "oneplus-fajita-t9")
	f := newFake(t, s, false)
	if v, err := f.Env.Microphone(context.Background(), s, false); err != nil || !strings.Contains(v, "dBFS") {
		t.Errorf("speech: %q %v", v, err)
	}
	if v, err := f.Env.Microphone(context.Background(), s, true); err != nil || !strings.Contains(v, "with the tone") {
		t.Errorf("loopback: %q %v", v, err)
	}
	f.SetMic(-70)
	if _, err := f.Env.Microphone(context.Background(), s, false); err == nil || !strings.Contains(err.Error(), "heard nothing") {
		t.Errorf("silence: %v", err)
	}
	f.mu.Lock()
	f.micDBFS, f.silent = -40, true
	f.mu.Unlock()
	if _, err := f.Env.Microphone(context.Background(), s, true); err == nil || !strings.Contains(err.Error(), "raised the level by 0.0 dB") {
		t.Errorf("a speaker that plays nothing: %v", err)
	}
	if got := levelDBFS(sine(-20, 16000)); got < -20.5 || got > -19.5 {
		t.Errorf("level of a -20 dBFS sine: %.2f", got)
	}
	if got := capturePCM("  CapturePCM/Mic=hw:0,1\n"); got != "plughw:0,1" {
		t.Errorf("capturePCM: %q", got)
	}
}

func TestServerFlow(t *testing.T) {
	s := loadSKU(t, "qemu-t9")
	f := newFake(t, s, true)
	srv := NewServer(f.Env, s, "")
	ts := httptest.NewServer(srv.Local())
	defer ts.Close()
	u := Dial(ts.URL)

	info, err := u.Info()
	if err != nil || info.Schema != UnitSchema || info.SKU != "qemu-t9" || info.Provisioned || info.DeviceID == "" {
		t.Fatalf("unit %+v %v", info, err)
	}
	plan, err := u.Plan()
	if err != nil || len(plan.Checks) != len(catalogue) || plan.SKU.ID != "qemu-t9" {
		t.Fatalf("plan %+v %v", plan, err)
	}

	// Nothing done on the factory screen: a full test fails on it.
	rep, err := u.Test(ModeFull, 0)
	if err != nil || rep.ExitCode() != ExitFail {
		t.Fatalf("full test before the screen: %v exit %d", err, rep.ExitCode())
	}
	if r, _ := rep.Result("keys"); r.Result != Skip || r.Reason != "not done on the factory screen" {
		t.Errorf("keys before the screen: %+v", r)
	}
	auto, err := u.Test(ModeAuto, 0)
	if err != nil || auto.ExitCode() != ExitPass || len(auto.Checks) != len(Only(plan.Checks, Auto)) {
		t.Fatalf("auto test: %v %+v", err, auto)
	}

	if err := u.Record(CheckResult{ID: "rtc", Result: Pass}); err == nil {
		t.Error("an automatic check's result taken from the screen")
	}
	if err := u.Record(CheckResult{ID: "keys", Result: "maybe"}); err == nil {
		t.Error("a result neither pass, fail nor skip taken")
	}
	// The station waits for the operator: the last result ends the wait.
	go func() {
		time.Sleep(100 * time.Millisecond)
		for _, c := range Only(Plan(s), Interactive) {
			if c.Skip == "" {
				res := CheckResult{ID: c.ID, Result: Pass, Value: "operator", DurationMS: 1200}
				if c.ID == "camera.back" {
					res = CheckResult{ID: c.ID, Result: Skip, Reason: "synthetic camera"}
				}
				_ = u.Record(res)
			}
		}
	}()
	t0 := time.Now()
	rep, err = u.Test(ModeFull, 30*time.Second)
	if err != nil || time.Since(t0) > 20*time.Second {
		t.Fatalf("waited test: %v after %s", err, time.Since(t0))
	}
	if r, _ := rep.Result("keys"); r.Result != Pass || !r.Required || r.Kind != Interactive {
		t.Errorf("keys after the screen: %+v", r)
	}
	if rep.ExitCode() != ExitFail {
		t.Errorf("a required camera skipped: exit %d", rep.ExitCode())
	}
	if err := u.Record(CheckResult{ID: "camera.back", Result: Pass, Value: "vivid"}); err != nil {
		t.Fatal(err)
	}
	if rep, _ = u.Test(ModeFull, 0); rep.ExitCode() != ExitPass {
		t.Fatalf("all done: exit %d %+v", rep.ExitCode(), rep.Checks)
	}
	if res, err := http.Get(ts.URL + PathReport); err != nil || res.StatusCode != 200 {
		t.Errorf("report: %v", err)
	}

	// The station's provisioning over the API ends factory mode.
	ca, err := keystore.LoadCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := Provision(u, ca, "station-1:nonce")
	if err != nil || id != info.DeviceID {
		t.Fatalf("provision: %q %v", id, err)
	}
	if info, _ := u.Info(); !info.Provisioned {
		t.Error("unit not provisioned after install_cert")
	}
	rep, err = u.Test(ModeAuto, 0)
	if err != nil || rep.ExitCode() != ExitRefused {
		t.Fatalf("test after provisioning: %v %+v", err, rep)
	}
	if _, err := u.CSR(); !errors.Is(err, ErrRefused) {
		t.Errorf("csr after provisioning: %v", err)
	}
	if _, err := u.Attest("again"); !errors.Is(err, ErrRefused) {
		t.Errorf("attest after provisioning: %v", err)
	}
	if err := u.InstallCerts(keystore.DeviceCerts{Cert: "x", Root: "y"}); !errors.Is(err, ErrRefused) {
		t.Errorf("install_cert after provisioning: %v", err)
	}
}

// The USB link serves only the station's part: the factory screen's is
// on the socket.
func TestStationAPI(t *testing.T) {
	s := loadSKU(t, "qemu-t9")
	f := newFake(t, s, true)
	ts := httptest.NewServer(NewServer(f.Env, s, "").Station())
	defer ts.Close()
	for _, p := range []string{PathPlan, PathReport} {
		res, err := http.Get(ts.URL + p)
		if err != nil || res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s on the station's API: %v %v", p, err, res.StatusCode)
		}
	}
	for _, p := range []string{PathResult, PathMicrophone} {
		res, err := http.Post(ts.URL+p, "application/json", strings.NewReader(`{"id":"keys","result":"pass"}`))
		if err != nil || res.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s on the station's API: %v %v", p, err, res.StatusCode)
		}
	}
	u := Dial(ts.URL)
	if _, err := u.Info(); err != nil {
		t.Error(err)
	}
	att, err := u.Attest("n1")
	if err != nil || att.AppID != keystore.ProvisionApp || att.Nonce != "n1" {
		t.Errorf("attest: %+v %v", att, err)
	}
}

// The unit is always on a link of its own: a station's HTTP_PROXY is
// never used to reach it. Go reads the proxy settings once per process,
// so the client runs in a child with only a recording proxy set, and
// dials "localhost." (Go exempts only "localhost" and loopback addresses).
func TestDialNoProxy(t *testing.T) {
	if addr := os.Getenv("FACTORY_NOPROXY_CHILD"); addr != "" {
		if _, err := Dial(addr).Info(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := net.LookupHost("localhost."); err != nil {
		t.Skipf("localhost. does not resolve: %v", err)
	}
	var mu sync.Mutex
	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied = append(proxied, r.Method+" "+r.URL.String())
		mu.Unlock()
		http.Error(w, "proxied", http.StatusBadGateway)
	}))
	defer proxy.Close()
	s := loadSKU(t, "qemu-t9")
	f := newFake(t, s, true)
	runner := httptest.NewServer(NewServer(f.Env, s, "").Station())
	defer runner.Close()
	_, port, err := net.SplitHostPort(runner.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	var env []string
	for _, kv := range os.Environ() {
		switch name, _, _ := strings.Cut(kv, "="); strings.ToUpper(name) {
		case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "REQUEST_METHOD":
		default:
			env = append(env, kv)
		}
	}
	for _, target := range []string{"localhost.:" + port, "http://localhost.:" + port} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDialNoProxy$")
		cmd.Env = append(slices.Clip(env), "HTTP_PROXY="+proxy.URL, "http_proxy="+proxy.URL, "FACTORY_NOPROXY_CHILD="+target)
		out, err := cmd.CombinedOutput()
		mu.Lock()
		got := proxied
		proxied = nil
		mu.Unlock()
		if len(got) != 0 {
			t.Errorf("Dial(%q) went through HTTP_PROXY: %q", target, got)
		}
		if err != nil {
			t.Errorf("Dial(%q): %v\n%s", target, err, out)
		}
	}
}

func TestUnixSocket(t *testing.T) {
	s := loadSKU(t, "qemu-t9")
	f := newFake(t, s, true)
	path := filepath.Join(f.Env.RunDir, Sock)
	ln, err := ListenSock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v %v", st.Mode(), err)
	}
	go Serve(ln, NewServer(f.Env, s, "").Local())
	if p, err := Dial("unix:" + path).Plan(); err != nil || p.SKU.ID != "qemu-t9" {
		t.Fatalf("plan over the socket: %v", err)
	}
}
