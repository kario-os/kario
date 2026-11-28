package keystored

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestBoardSensor(t *testing.T) {
	dir := t.TempDir()
	sku := filepath.Join(dir, "sku.json")
	if got := readSKU(sku).sensor(); got != "sim" {
		t.Errorf("no sku.json (a developer machine): %q, want sim", got)
	}
	for body, want := range map[string]string{`{"sku": "x"}`: "none", `{"fingerprint": "sim"}`: "sim", `not json`: "none"} {
		if err := os.WriteFile(sku, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := readSKU(sku).sensor(); got != want {
			t.Errorf("%s: %q, want %q", body, got, want)
		}
	}
}

func TestBoardSensorPlace(t *testing.T) {
	sku := filepath.Join(t.TempDir(), "sku.json")
	if err := os.WriteFile(sku, []byte(`{"fingerprint": "sim", "fingerprint_at": "ok-key"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := readSKU(sku); s.sensor() != "sim" || s.FingerprintAt != ui.FingerprintOKKey {
		t.Errorf("%+v: want the simulated sensor in the OK key", s)
	}
}
