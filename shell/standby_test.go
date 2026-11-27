package shell

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
)

// What phone-charger draws without the Shell follows the user's settings
// as the Shell's standby screen does, and the board's region without them.
func TestStandbyFollowsTheSettings(t *testing.T) {
	prev := i18n.Lang()
	t.Cleanup(func() { i18n.SetLang(prev) })
	t.Setenv("PHONE_LANG", "")
	at := time.Date(2026, 11, 26, 5, 5, 0, 0, time.UTC)

	dir := t.TempDir()
	settings := `{"language":"es","time_zone":"America/Mexico_City","clock_12h":true,"theme":"retro","brightness":35}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	s := LoadStandby(dir, SKU{Region: "cn"})
	if s.Lang() != i18n.Spanish || i18n.Lang() != i18n.Spanish {
		t.Errorf("language %q (process %q), want es", s.Lang(), i18n.Lang())
	}
	if got := s.Clock(at); got != "11:05 p. m." {
		t.Errorf("clock %q, want the 12-hour clock in Mexico City", got)
	}
	if got := s.Date(at); got != "miércoles, 25 de noviembre" {
		t.Errorf("date %q, want Mexico City's day in Spanish", got)
	}
	if s.Theme() != "retro" || s.Brightness() != 35 {
		t.Errorf("theme %q brightness %d", s.Theme(), s.Brightness())
	}

	none := LoadStandby(t.TempDir(), SKU{Region: "cn"})
	if none.Lang() != i18n.SimplifiedChinese || none.Theme() != "" || none.Brightness() != defaultBrightness {
		t.Errorf("no settings: language %q theme %q brightness %d", none.Lang(), none.Theme(), none.Brightness())
	}
	if got := none.Clock(at); got != "13:05" {
		t.Errorf("no settings: clock %q, want 24-hour China time", got)
	}
}
