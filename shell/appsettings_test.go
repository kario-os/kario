package shell

import (
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestSettingTypes(t *testing.T) {
	c := &Core{}
	c.cfg.StateDir = t.TempDir()
	c.loadPrefs()
	m := &Manifest{ID: "org.example.x", Settings: []ui.AppSetting{
		{Key: "n", Title: "Number", Type: "number", Min: 1, Max: 5},
		{Key: "s", Title: "Text", Type: "text", MaxLen: 4},
		{Key: "c", Title: "Choice", Type: "choice", Choices: []string{"A", "B"}},
	}}
	if err := checkSettingDecls(m); err != nil {
		t.Fatal(err)
	}
	c.apps = []*App{{M: m}}
	// The text limit counts characters: four Chinese ones fit, five do not.
	for _, ok := range []struct {
		key string
		v   any
	}{{"n", 3.0}, {"s", "\u56db\u4e2a\u5b57\u7b26"}, {"c", "B"}} {
		if err := c.setAppSetting("org.example.x", ok.key, ok.v); err != nil {
			t.Errorf("%s=%v: %v", ok.key, ok.v, err)
		}
	}
	for _, bad := range []struct {
		key string
		v   any
	}{{"n", 9.0}, {"n", "3"}, {"s", "\u4e94\u4e2a\u5b57\u7b26\u4e86"}, {"s", "a\nb"}, {"c", "C"}, {"zz", true}} {
		if err := c.setAppSetting("org.example.x", bad.key, bad.v); err == nil {
			t.Errorf("%s=%v accepted", bad.key, bad.v)
		}
	}
	for _, d := range [][]ui.AppSetting{
		{{Key: "n", Title: "Number", Type: "number", Min: 5, Max: 5}},
		{{Key: "c", Title: "Choice", Type: "choice", Choices: []string{"A"}}},
		{{Key: "", Title: "No key", Type: "toggle"}},
	} {
		if err := checkSettingDecls(&Manifest{Settings: d}); err == nil {
			t.Errorf("%+v accepted", d)
		}
	}
}
