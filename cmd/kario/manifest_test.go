package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// kario manifest check -json names every problem by its field, with the
// tables the editor lays its form out from; build refuses with the first.
func TestManifestCheck(t *testing.T) {
	dir := newTestProject(t)
	var out bytes.Buffer
	if err := manifestCommand(dir, true, nil, &out); err != nil {
		t.Fatalf("the template: %v\n%s", err, out.String())
	}
	var rep manifestReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || !rep.OK || len(rep.Problems) != 0 {
		t.Fatalf("%v %+v", err, rep)
	}
	s := rep.Schema
	cam := slices.IndexFunc(s.Permissions, func(p permInfo) bool { return p.Name == "camera" })
	net := slices.IndexFunc(s.Permissions, func(p permInfo) bool { return p.Name == s.NetPerm })
	if cam < 0 || !s.Permissions[cam].Runtime || s.Permissions[cam].Data != "photos" || net < 0 || s.Permissions[net].Runtime ||
		s.MaxPurpose != 80 || !slices.Contains(s.PrivacyData, "location") || !slices.Contains(s.PrivacyUses, "ads") ||
		!slices.Contains(s.Languages, "zh-Hant") || s.Runtime != "js" {
		t.Fatalf("schema %+v", s)
	}
	if slices.ContainsFunc(s.Permissions, func(p permInfo) bool { return p.Name == "system" || p.Name == "install" }) {
		t.Fatal("the schema offers a system permission")
	}

	bad := `{"id": "Notes", "name": " ", "runtime": "js", "icon_image": "none.png",
		"permissions": ["camera", "net", "location", "bogus"],
		"purposes": {"location": "Nearby notes", "photos": "x"},
		"privacy": {"collects": [{"data": "location", "use": ["app"]}]}}`
	out.Reset()
	err := manifestCommand("-", true, strings.NewReader(bad), &out)
	if !errors.Is(err, errSilent) {
		t.Fatalf("problems: %v", err)
	}
	rep = manifestReport{}
	_ = json.Unmarshal(out.Bytes(), &rep)
	fields := map[string]string{}
	for _, p := range rep.Problems {
		fields[p.Field] += p.Message + "; "
	}
	for f, want := range map[string]string{
		"id": "reverse domain", "name": "needs a name", "permissions.bogus": "not one an app can declare",
		"purposes.camera": "needs a purpose", "purposes.photos": "does not declare", "privacy": `"camera" permission opens photos`,
	} {
		if !strings.Contains(fields[f], want) {
			t.Errorf("%s: %q, want %q (%+v)", f, fields[f], want, rep.Problems)
		}
	}
	if _, ok := fields["icon_image"]; ok {
		t.Error("a manifest on stdin has no directory to look for its icon in")
	}

	replace(t, dir, "manifest.json", `"permissions": []`, `"icon_image": "none.png", "permissions": ["camera"]`)
	out.Reset()
	if err := manifestCommand(filepath.Join(dir, "manifest.json"), false, nil, &out); !errors.Is(err, errSilent) ||
		!strings.Contains(out.String(), "needs a purpose") || !strings.Contains(out.String(), "icon_image none.png") {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if _, err := build(dir, buildOptions{}); err == nil || !strings.Contains(err.Error(), "needs a purpose") {
		t.Fatalf("build: %v", err)
	}
	out.Reset()
	if err := manifestCommand("-", true, strings.NewReader("{"), &out); !errors.Is(err, errSilent) || !strings.Contains(out.String(), "unexpected end") {
		t.Fatalf("not JSON: %v %s", err, out.String())
	}
	if err := manifestCommand(filepath.Join(t.TempDir(), "none"), true, nil, &out); err == nil || errors.Is(err, errSilent) {
		t.Fatalf("no manifest: %v", err)
	}
}
