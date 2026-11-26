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

// The capability versions a manifest needs are ones the SDK knows, of a
// level its min_platform reaches; the problem is the field's.
func TestManifestCapabilities(t *testing.T) {
	check := func(minPlatform int, caps string) []problem {
		t.Helper()
		m := `{"id": "com.example.music", "name": "Music", "runtime": "js", "min_platform": ` + strconv.Itoa(minPlatform) + `, "capabilities": ` + caps + `}`
		var out bytes.Buffer
		err := manifestCommand("-", true, strings.NewReader(m), &out)
		var rep manifestReport
		if json.Unmarshal(out.Bytes(), &rep) != nil || rep.OK != (err == nil) {
			t.Fatalf("%s: %v %s", m, err, out.String())
		}
		return rep.Problems
	}
	if p := check(2, `["media.list@2", "media.list@1", "clipboard.set"]`); len(p) != 0 {
		t.Errorf("served versions: %+v", p)
	}
	for _, tc := range []struct {
		min        int
		caps, want string
	}{
		{1, `["media.list@2"]`, "media.list@2 came in at platform level 2, above min_platform 1"},
		{2, `["media.list@3"]`, `unknown capability "media.list@3" (served: media.list@1, media.list@2)`},
	} {
		if p := check(tc.min, tc.caps); len(p) != 1 || p[0].Field != "capabilities" || !strings.Contains(p[0].Message, tc.want) {
			t.Errorf("%s at %d: %+v, want %q", tc.caps, tc.min, p, tc.want)
		}
	}
}
