package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// kario new writes the SDK's platform level as the project's min_platform
// (a JSON number); manifest check takes no level the SDK does not know.
func TestTemplateMinPlatform(t *testing.T) {
	dir := newTestProject(t)
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if got := string(m["min_platform"]); got != strconv.Itoa(ui.PlatformLevel) {
		t.Fatalf("min_platform %s, want %d:\n%s", got, ui.PlatformLevel, raw)
	}
	rep, err := checkManifest(dir, nil)
	if err != nil || !rep.OK || rep.Schema.PlatformLevel != ui.PlatformLevel {
		t.Fatalf("the template: %v %+v", err, rep)
	}
	for _, level := range []int{ui.PlatformLevel + 1, -1} {
		bad := `{"id": "com.example.notes", "name": "Notes", "runtime": "js", "min_platform": ` + strconv.Itoa(level) + `}`
		var out bytes.Buffer
		if err := manifestCommand("-", false, strings.NewReader(bad), &out); !errors.Is(err, errSilent) || !strings.Contains(out.String(), "min_platform") {
			t.Errorf("min_platform %d: %v %s", level, err, out.String())
		}
	}
}
