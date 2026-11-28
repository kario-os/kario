package factorytest

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/factory"
)

const qemuSKU = "../../factory/testdata/qemu-t9.json"

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ftp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func report(t *testing.T, out []byte) factory.Report {
	t.Helper()
	var r factory.Report
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("stdout is not a report: %v\n%s", err, out)
	}
	return r
}

// On the phone (here a made-up one): the report on stdout and in the file.
func TestLocalRun(t *testing.T) {
	dir := shortDir(t)
	out := filepath.Join(dir, "report.json")
	var stdout bytes.Buffer
	code := run([]string{"--fake", filepath.Join(dir, "unit"), "--run-dir", filepath.Join(dir, "run"), "--sku", qemuSKU, "--auto", "--out", out}, &stdout)
	if code != factory.ExitPass {
		t.Fatalf("auto on a whole unit: exit %d\n%s", code, stdout.String())
	}
	r := report(t, stdout.Bytes())
	if r.Mode != factory.ModeAuto || r.SKU != "qemu-t9" || r.DeviceID == "" {
		t.Errorf("report %+v", r)
	}
	if raw, err := os.ReadFile(out); err != nil || report(t, raw).DeviceID != r.DeviceID {
		t.Errorf("report file: %v", err)
	}
	stdout.Reset()
	code = run([]string{"--fake", filepath.Join(dir, "unit"), "--run-dir", filepath.Join(dir, "run"), "--sku", qemuSKU, "--out", out}, &stdout)
	if code != factory.ExitFail {
		t.Errorf("full test with no factory screen: exit %d", code)
	}
	if code := run([]string{"--no-such-flag"}, &stdout); code != factory.ExitError {
		t.Errorf("usage: exit %d", code)
	}
	if code := run([]string{"--sku", filepath.Join(dir, "none.json"), "--auto"}, &stdout); code != factory.ExitError {
		t.Errorf("no SKU: exit %d", code)
	}
}

// The station's command line against a unit's runner: the exit code
// follows the unit's hardware, its identity and its keystore.
func TestStation(t *testing.T) {
	sku, err := factory.LoadSKU(qemuSKU)
	if err != nil {
		t.Fatal(err)
	}
	dir := shortDir(t)
	f, err := factory.NewFake(filepath.Join(dir, "unit"), sku, factory.FakeOptions{Keystore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ts := httptest.NewServer(factory.NewServer(f.Env, sku, "").Station())
	defer ts.Close()
	id, _, _ := f.Store.CSR()
	out := filepath.Join(dir, "r.json")
	station := func(extra ...string) (int, string) {
		var stdout bytes.Buffer
		code := run(append([]string{"--unit", ts.URL, "--out", out}, extra...), &stdout)
		return code, stdout.String()
	}

	if code, s := station("--auto", "--device", id); code != factory.ExitPass {
		t.Fatalf("whole unit: exit %d\n%s", code, s)
	}
	if code, _ := station("--auto", "--device", "someone-else"); code != factory.ExitError {
		t.Errorf("another unit's DEVICE_ID: exit %d", code)
	}
	if err := f.Remove("wifi"); err != nil {
		t.Fatal(err)
	}
	code, s := station("--auto")
	if code != factory.ExitFail {
		t.Fatalf("no Wi-Fi: exit %d", code)
	}
	if r := report(t, []byte(s)); r.Checks == nil {
		t.Fatal("no checks")
	} else if c, _ := r.Result("wifi"); c.Result != factory.Fail {
		t.Errorf("no Wi-Fi: %+v", c)
	}

	var stdout bytes.Buffer
	if code := run([]string{"--unit", ts.URL, "--provision", filepath.Join(dir, "ca")}, &stdout); code != factory.ExitPass {
		t.Fatalf("provision: exit %d", code)
	}
	if !f.Store.Provisioned() {
		t.Fatal("the keystore is not provisioned")
	}
	if code, s := station("--auto"); code != factory.ExitRefused || report(t, []byte(s)).Refused == "" {
		t.Errorf("provisioned: exit %d\n%s", code, s)
	}
	if code := run([]string{"--unit", ts.URL, "--provision", filepath.Join(dir, "ca")}, &stdout); code != factory.ExitRefused {
		t.Errorf("provisioned again: exit %d", code)
	}
	if code := run([]string{"--unit", "127.0.0.1:1", "--auto"}, &stdout); code != factory.ExitError {
		t.Errorf("no unit: exit %d", code)
	}
}

func TestGateCommand(t *testing.T) {
	dir := shortDir(t)
	mark := filepath.Join(dir, "factory", "on")
	var stdout bytes.Buffer
	if code := run([]string{"--fake", filepath.Join(dir, "unit"), "--sku", qemuSKU, "--gate", "--mark", mark}, &stdout); code != factory.ExitPass || !factory.Active(mark) {
		t.Fatalf("unprovisioned: exit %d, mark %v", code, factory.Active(mark))
	}
	if code := run([]string{"--sku", qemuSKU, "--run-dir", filepath.Join(dir, "none"), "--gate", "--mark", mark}, &stdout); code != factory.ExitRefused || factory.Active(mark) {
		t.Fatalf("no keystore: exit %d, mark %v", code, factory.Active(mark))
	}
}
