package ui

import (
	"slices"
	"strings"
	"testing"
)

// A capability is "name" or "name@N" from 1 without leading zeros; the
// bare name and "@1" are the same version and key.
func TestParseCap(t *testing.T) {
	for c, want := range map[string]struct {
		name string
		v    int
	}{
		"media.list": {"media.list", 1}, "media.list@1": {"media.list", 1}, "media.list@2": {"media.list", 2},
		"media.call.start@12": {"media.call.start", 12}, "settings.app.set": {"settings.app.set", 1},
	} {
		name, v, ok := ParseCap(c)
		if !ok || name != want.name || v != want.v {
			t.Errorf("ParseCap(%q) = %q %d %v", c, name, v, ok)
		}
		if key := CapKey(name, v); key != strings.TrimSuffix(c, "@1") {
			t.Errorf("CapKey(%q, %d) = %q", name, v, key)
		}
	}
	for _, c := range []string{"", "@2", "media.list@", "media.list@0", "media.list@-1", "media.list@02", "media.list@x",
		"media.list@2@3", "media.list@ 2", "Media.list", "media", "media.list ", "media..list"} {
		if _, _, ok := ParseCap(c); ok {
			t.Errorf("ParseCap(%q) took it", c)
		}
	}
}

// The broker's names: the bare name and @1 are version 1, @2 its own key;
// a version this system lacks, or a removed one, says which it serves,
// and every refusal starts as an older system's for an unknown name.
func TestResolveCap(t *testing.T) {
	for c, want := range map[string]string{
		CapMediaList: CapMediaList, CapMediaList + "@1": CapMediaList, CapMediaList2: CapMediaList2,
		CapLocationGet + "@1": CapLocationGet,
	} {
		if got, err := ResolveCap(c); err != nil || got != want {
			t.Errorf("ResolveCap(%q) = %q %v, want %q", c, got, err, want)
		}
	}
	for c, want := range map[string]string{
		"media.list@3":   `unknown capability "media.list@3" (served: media.list@1, media.list@2)`,
		"location.get@2": `unknown capability "location.get@2" (served: location.get@1)`,
		"no.such@2":      `unknown capability "no.such@2"`,
		"media.list@02":  `unknown capability "media.list@02"`,
		"no.such":        `unknown capability "no.such"`,
	} {
		if _, err := ResolveCap(c); err == nil || err.Error() != want {
			t.Errorf("ResolveCap(%q): %v, want %s", c, err, want)
		}
	}
	if got := CapVersions("media.list"); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("media.list's versions %v", got)
	}

	// Version 1 removed at level 5, its replacement of level 2 served: the
	// bare name goes with it rather than reaching @2 with @1's arguments.
	levels, removed := map[string]int{"x.y@2": 2, "x.z": 1}, map[string]int{"x.y": 5}
	for _, c := range []string{"x.y", "x.y@1"} {
		if _, err := ResolveCapIn(levels, removed, c); err == nil || err.Error() != `unknown capability "`+c+`": removed at platform level 5 (served: x.y@2)` {
			t.Errorf("%s once removed: %v", c, err)
		}
	}
	if got, err := ResolveCapIn(levels, removed, "x.y@2"); err != nil || got != "x.y@2" {
		t.Errorf("x.y@2: %q %v", got, err)
	}
}

// The platform's own tables keep the rules; tables that break them fail:
// a version removed before CapKeepLevels levels went by, or with nothing
// replacing it, or still served; a version without the one before, or of
// no later level; a key not in its one spelling; a level not reached.
func TestCapTables(t *testing.T) {
	if err := CheckCapTables(CapLevels, CapRemoved, PlatformLevel); err != nil {
		t.Fatal(err)
	}
	ok := map[string]int{"a.b": 1, "a.b@2": 2, "a.b@3": 4}
	if err := CheckCapTables(ok, map[string]int{}, 4); err != nil {
		t.Fatalf("good tables: %v", err)
	}
	if err := CheckCapTables(map[string]int{"a.b@2": 2}, map[string]int{"a.b": 5}, 5); err != nil {
		t.Fatalf("@1 removed CapKeepLevels levels after @2: %v", err)
	}
	for why, tc := range map[string]struct {
		levels, removed map[string]int
		platform        int
	}{
		"earliest":           {map[string]int{"a.b@2": 2}, map[string]int{"a.b": 4}, 5},
		"no later version":   {map[string]int{"a.c": 1}, map[string]int{"a.b": 5}, 5},
		"removed and served": {map[string]int{"a.b": 1, "a.b@2": 2}, map[string]int{"a.b": 5}, 5},
		"past this system":   {map[string]int{"a.b@2": 2}, map[string]int{"a.b": 6}, 5},
		"no a.b before it":   {map[string]int{"a.b@2": 2}, map[string]int{}, 5},
		"not after":          {map[string]int{"a.b": 2, "a.b@2": 2}, map[string]int{}, 5},
		"not a capability":   {map[string]int{"a.b@1": 1}, map[string]int{}, 5},
		"not 1 to 2":         {map[string]int{"a.b": 3}, map[string]int{}, 2},
	} {
		if err := CheckCapTables(tc.levels, tc.removed, tc.platform); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%v / %v: %v, want %q", tc.levels, tc.removed, err, why)
		}
	}
}

// A manifest's capabilities: served versions of a level its min_platform
// reaches (0 is level 1).
func TestCheckCapabilities(t *testing.T) {
	if err := CheckCapabilities([]string{CapMediaList, CapMediaList2, CapClipboardSet + "@1"}, 2); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		caps []string
		min  int
		want string
	}{
		{[]string{CapMediaList2}, 1, "media.list@2 came in at platform level 2, above min_platform 1"},
		{[]string{CapMediaList2}, 0, "above min_platform 0"},
		{[]string{"media.list@3"}, 2, `capabilities: unknown capability "media.list@3"`},
		{[]string{"media list"}, 2, "unknown capability"},
	} {
		if err := CheckCapabilities(tc.caps, tc.min); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v at %d: %v, want %q", tc.caps, tc.min, err, tc.want)
		}
	}
}
