package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A signed catalog verifies as the store encoded it (through another JSON
// encoder too); changed entries, an untrusted store and a stale or
// future-dated catalog do not.
func TestCatalog(t *testing.T) {
	state := t.TempDir()
	store, err := DevStore(filepath.Join(state, "store"), filepath.Join(state, "trust"))
	must(t, err)
	roots := LoadRoots(filepath.Join(state, "trust"))
	now := time.Unix(1_800_000_000, 0)
	apps := []map[string]any{{"id": "com.example.notes", "version": "1.1", "filing": "\u4eacICP\u590712345678\u53f7-1A", "summary": "<b>Notes</b>"}}
	signed, err := store.SignCatalog(apps, now)
	must(t, err)
	var out bytes.Buffer
	must(t, json.NewEncoder(&out).Encode(signed))
	c, err := VerifyCatalog(out.Bytes(), roots, now.Add(time.Hour))
	if err != nil || !bytes.Contains(c.Apps, []byte("com.example.notes")) {
		t.Fatalf("verify: %v %+v", err, c)
	}
	forged := signed
	forged.Apps = bytes.Replace(signed.Apps, []byte(`"1.1"`), []byte(`"1.0"`), 1)
	raw, _ := json.Marshal(forged)
	if _, err := VerifyCatalog(raw, roots, now); err == nil {
		t.Fatal("a changed catalog verified")
	}
	other, err := DevStore(filepath.Join(state, "other"), filepath.Join(state, "trust2"))
	must(t, err)
	theirs, _ := other.SignCatalog(apps, now)
	raw, _ = json.Marshal(theirs)
	if _, err := VerifyCatalog(raw, roots, now); err == nil {
		t.Fatal("a catalog from an untrusted store verified")
	}
	raw, _ = json.Marshal(signed)
	if _, err := VerifyCatalog(raw, roots, now.Add(CatalogMaxAge+time.Hour)); err == nil {
		t.Fatal("a stale catalog verified")
	}
	if _, err := VerifyCatalog(raw, roots, now.Add(-48*time.Hour)); err == nil {
		t.Fatal("a future-dated catalog verified")
	}
}

// A staged entry offers its new version within the rollout and the previous
// one outside it or once halted; about the given share of buckets is in.
func TestOfferedApps(t *testing.T) {
	apps := json.RawMessage(`[{"id":"com.example.notes","version":"1.2","size":9,"sha256":"new","permissions":["net","camera"],
		"privacy":{"local":["photos"]},"rollout":10,"previous":{"version":"1.1","size":8,"sha256":"old","permissions":["net"],"privacy":{}}},
		{"id":"com.example.halted","version":"2.0","sha256":"b","privacy":{},"previous":{"version":"1.0","sha256":"a"}},
		{"id":"com.example.maps","version":"3.0","sha256":"m"}]`)
	type entry struct {
		ID, Version, SHA256 string
		Permissions         []string
		Privacy             *struct{ Local []string }
	}
	offer := func(in bool) []entry {
		raw, err := OfferedApps(apps, 1, func(string, int) bool { return in })
		if err != nil {
			t.Fatal(err)
		}
		var out []entry
		_ = json.Unmarshal(raw, &out)
		return out
	}
	if got := offer(true); got[0].Version != "1.2" || got[0].Privacy == nil || len(got[0].Privacy.Local) != 1 || got[1].Version != "1.0" ||
		got[1].Privacy != nil || got[2].Version != "3.0" {
		t.Fatalf("in the rollout: %+v", got)
	}
	if got := offer(false); got[0].Version != "1.1" || got[0].SHA256 != "old" || len(got[0].Permissions) != 1 || got[0].Privacy == nil ||
		len(got[0].Privacy.Local) != 0 || got[1].Privacy != nil || got[2].Version != "3.0" {
		t.Fatalf("outside it: %+v", got)
	}
	in := 0
	for b := 0; b < 10000; b++ {
		if InRollout(b, "com.example.notes", 10) {
			in++
		}
	}
	if in < 900 || in > 1100 || InRollout(0, "x", 0) || !InRollout(9999, "x", 100) {
		t.Fatalf("%d of 10000 buckets in a 10%% rollout", in)
	}
}

// A device is offered only what its platform level runs: an entry needing
// a newer one is left out, and a staged version needing one gives way to
// the previous release when that one runs.
func TestOfferedAppsByPlatform(t *testing.T) {
	apps := json.RawMessage(`[{"id":"com.example.old","version":"1.0"},
		{"id":"com.example.new","version":"1.0","min_platform":3},
		{"id":"com.example.staged","version":"2.0","min_platform":3,"rollout":50,"previous":{"version":"1.0","min_platform":2}},
		{"id":"com.example.lowered","version":"2.0","rollout":50,"previous":{"version":"1.0","min_platform":3}}]`)
	offer := func(level int, in bool) string {
		raw, err := OfferedApps(apps, level, func(string, int) bool { return in })
		if err != nil {
			t.Fatal(err)
		}
		var out []struct {
			ID, Version string
			MinPlatform int `json:"min_platform"`
		}
		_ = json.Unmarshal(raw, &out)
		var s []string
		for _, e := range out {
			s = append(s, fmt.Sprintf("%s@%s/%d", strings.TrimPrefix(e.ID, "com.example."), e.Version, e.MinPlatform))
		}
		return strings.Join(s, " ")
	}
	for _, c := range []struct {
		level int
		in    bool
		want  string
	}{
		{3, true, "old@1.0/0 new@1.0/3 staged@2.0/3 lowered@2.0/0"},
		{3, false, "old@1.0/0 new@1.0/3 staged@1.0/2 lowered@1.0/3"},
		{2, true, "old@1.0/0 staged@1.0/2 lowered@2.0/0"},
		{2, false, "old@1.0/0 staged@1.0/2"},
		{1, true, "old@1.0/0 lowered@2.0/0"},
		{1, false, "old@1.0/0"},
	} {
		if got := offer(c.level, c.in); got != c.want {
			t.Errorf("level %d, in the rollout %v: %s, want %s", c.level, c.in, got, c.want)
		}
	}
	if raw, err := OfferedApps(json.RawMessage(`[{"id":"com.example.new","version":"1.0","min_platform":3}]`), 1, nil); err != nil || string(raw) != "[]" {
		t.Errorf("nothing to offer: %s %v", raw, err)
	}
}
