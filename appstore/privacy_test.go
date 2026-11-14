package appstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// The review applies the privacy label check, the catalogue carries the
// label (a staged update's previous release its own), and an update that
// collects data the published version did not waits for the staff.
func TestPrivacyLabel(t *testing.T) {
	srv, ts := newServer(t)
	alice := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	app := func(version string, privacy any) []byte {
		m := map[string]any{"id": "org.example.maps", "name": "Maps", "version": version, "exec": "phone-app-x",
			"permissions": []string{ui.PermNet, ui.PermLocation}, "purposes": map[string]string{ui.PermLocation: "Show where you are"}}
		if privacy != nil {
			m["privacy"] = privacy
		}
		return build(t, alice, m)
	}
	refuse := func(privacy any, want string) {
		t.Helper()
		var out struct{ Error string }
		if code := call(t, "POST", ts.URL+"/v1/submissions", "", app("1.0", privacy), &out); code != 422 || !strings.Contains(out.Error, want) {
			t.Fatalf("label %v: %d %q, want %q", privacy, code, out.Error, want)
		}
	}
	refuse(map[string]any{}, `"location" permission opens location`)
	refuse(map[string]any{"local": []string{"location"}, "collects": []any{map[string]any{"data": "usage"}}}, "what usage is collected for")

	local := map[string]any{"local": []string{ui.DataLocation}}
	if code, st := submit(t, ts, app("1.0", local)); code != 200 || st != "pending_review" {
		t.Fatalf("1.0: %d %s", code, st)
	}
	call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.maps/1.0", token, map[string]string{}, nil)
	if e := srv.catalog["org.example.maps"]; e.Privacy == nil || len(e.Privacy.Local) != 1 || len(e.Privacy.Collects) != 0 {
		t.Fatalf("published label %+v", e.Privacy)
	}

	collects := map[string]any{"collects": []any{map[string]any{"data": ui.DataLocation, "use": []string{ui.UseApp}, "linked": true}}}
	var out struct {
		Status string
		Entry  Entry
	}
	if code := call(t, "POST", ts.URL+"/v1/submissions", "", app("1.1", collects), &out); code != 200 || out.Status != "pending_review" {
		t.Fatalf("an update collecting location: %d %s", code, out.Status)
	}
	if p := srv.pending["org.example.maps@1.1"]; p.Why != "collects new data: location" {
		t.Fatalf("why %q", p.Why)
	}
	call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.maps/1.1", token, map[string]string{}, nil)

	same := map[string]any{"collects": []any{map[string]any{"data": ui.DataLocation, "use": []string{ui.UseApp, ui.UseAnalytics}}}}
	if code := call(t, "POST", ts.URL+"/v1/submissions?rollout=10", "", app("1.2", same), &out); code != 200 || out.Status != "published" {
		t.Fatalf("an update collecting nothing new: %d %s", code, out.Status)
	}
	e := srv.catalog["org.example.maps"]
	if !e.Privacy.Sends(ui.DataLocation) || e.Previous == nil {
		t.Fatalf("staged entry %+v", e)
	}
	apps, _ := json.Marshal([]Entry{e})
	offered, err := pkg.OfferedApps(apps, func(string, int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	var got []Entry
	if err := json.Unmarshal(offered, &got); err != nil || got[0].Version != "1.1" || !got[0].Privacy.Sends(ui.DataLocation) ||
		len(got[0].Privacy.Collects[0].Use) != 1 || !got[0].Privacy.Collects[0].Linked {
		t.Fatalf("outside the rollout: %+v %v", got, err)
	}

	srv2, ts2 := newServer(t)
	bob := developer(t, ts2, "Bob", "91310000MA1FL0000N")
	none := build(t, bob, map[string]any{"id": "org.example.calc", "name": "Calc", "version": "1.0", "exec": "phone-app-x"})
	if code, _ := submit(t, ts2, none); code != 200 || srv2.pending["org.example.calc@1.0"].Privacy != nil {
		t.Fatalf("an app without a label: %d", code)
	}
}
