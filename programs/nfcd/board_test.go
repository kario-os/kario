package nfcd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoardController(t *testing.T) {
	sku := filepath.Join(t.TempDir(), "sku.json")
	if got := boardController(sku); got != "sim" {
		t.Errorf("no sku.json (a developer machine): %q, want sim", got)
	}
	for body, want := range map[string]string{`{"sku": "x"}`: "", `{"nfc": "sim"}`: "sim", `{"nfc": "nci:/dev/nxpnfc"}`: "nci:/dev/nxpnfc", `not json`: ""} {
		if err := os.WriteFile(sku, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := boardController(sku); got != want {
			t.Errorf("%s: %q, want %q", body, got, want)
		}
	}
}
