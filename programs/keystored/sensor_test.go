package keystored

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoardSensor(t *testing.T) {
	dir := t.TempDir()
	sku := filepath.Join(dir, "sku.json")
	if got := boardSensor(sku); got != "sim" {
		t.Errorf("no sku.json (a developer machine): %q, want sim", got)
	}
	for body, want := range map[string]string{`{"sku": "x"}`: "none", `{"fingerprint": "sim"}`: "sim", `not json`: "none"} {
		if err := os.WriteFile(sku, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := boardSensor(sku); got != want {
			t.Errorf("%s: %q, want %q", body, got, want)
		}
	}
}
