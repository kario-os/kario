package appstore

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// An app names the platform level it needs, one the store knows; a phone
// that says its level is not sent the apps it can run no release of.
func TestCatalogByPlatform(t *testing.T) {
	srv, ts := newServer(t)
	alice := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	app := func(id string, level int) []byte {
		return build(t, alice, map[string]any{"id": id, "name": "N", "version": "1.0", "exec": "phone-app-x", "min_platform": level})
	}
	for _, bad := range []int{-1, ui.PlatformLevel + 1} {
		if code, _ := submit(t, ts, app("org.example.bad", bad)); code != 422 {
			t.Errorf("min_platform %d: %d, want 422", bad, code)
		}
	}
	if code, _ := submit(t, ts, app("org.example.todo", ui.PlatformLevel)); code != 200 {
		t.Fatalf("min_platform %d refused: %d", ui.PlatformLevel, code)
	}
	call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.todo/1.0", token, map[string]string{}, nil)
	if e := srv.catalog["org.example.todo"]; e.MinPlatform != ui.PlatformLevel {
		t.Fatalf("entry %+v", e)
	}

	next := ui.PlatformLevel + 1
	srv.mu.Lock()
	srv.catalog["org.example.next"] = Entry{ID: "org.example.next", Name: "Next", Version: "1.0", MinPlatform: next}
	srv.catalog["org.example.staged"] = Entry{ID: "org.example.staged", Name: "Staged", Version: "2.0", MinPlatform: next,
		Rollout: 50, Previous: &pkg.Release{Version: "1.0", MinPlatform: ui.PlatformLevel}}
	srv.mu.Unlock()
	listed := func(query string) string {
		t.Helper()
		res, err := http.Get(ts.URL + "/v1/catalog" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		signed, err := pkg.VerifyCatalog(raw, srv.store.Roots(), time.Now())
		if err != nil {
			t.Fatalf("catalog%s: %v", query, err)
		}
		var cat []Entry
		_ = json.Unmarshal(signed.Apps, &cat)
		var ids []string
		for _, e := range cat {
			ids = append(ids, strings.TrimPrefix(e.ID, "org.example."))
		}
		sort.Strings(ids)
		return strings.Join(ids, " ")
	}
	for query, want := range map[string]string{
		"":                                "next staged todo",
		"?platform=" + strconv.Itoa(next): "next staged todo",
		"?platform=" + strconv.Itoa(ui.PlatformLevel): "staged todo",
		"?platform=0": "next staged todo",
		"?platform=x": "next staged todo",
	} {
		if got := listed(query); got != want {
			t.Errorf("catalog%s: %s, want %s", query, got, want)
		}
	}
}
