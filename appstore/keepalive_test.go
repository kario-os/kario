package appstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// An app's own connection in the background needs a purpose, and every
// version of an app with one waits for the staff.
func TestReviewKeepalive(t *testing.T) {
	srv, ts := newServer(t)
	dev := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	app := func(version string, purposes map[string]string) []byte {
		return build(t, dev, map[string]any{"id": "org.example.chat", "name": "Chat", "version": version, "exec": "phone-app-x",
			"permissions": []string{ui.PermNet, ui.PermKeepalive}, "purposes": purposes})
	}
	var out struct{ Error string }
	if code := call(t, "POST", ts.URL+"/v1/submissions", "", app("1.0", nil), &out); code == 200 || !strings.Contains(out.Error, "needs a purpose") {
		t.Fatalf("no purpose: %d %q", code, out.Error)
	}
	why := map[string]string{ui.PermKeepalive: "Messages from our own servers"}
	if code, st := submit(t, ts, app("1.0", why)); code != 200 || st != "pending_review" {
		t.Fatalf("1.0: %d %s", code, st)
	}
	if code := call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.chat/1.0", token, map[string]string{}, nil); code != 200 {
		t.Fatalf("review: %d", code)
	}
	code, st := submit(t, ts, app("1.1", why))
	if p := srv.pending["org.example.chat@1.1"]; code != 200 || st != "pending_review" || !strings.Contains(p.Why, "Messages from our own servers") {
		t.Fatalf("an update: %d %s %q", code, st, p.Why)
	}

	// A TypeScript app's keepalive WebSocket is the same connection.
	script := func(version string, purposes map[string]string) []byte {
		dir := t.TempDir()
		raw, _ := json.Marshal(map[string]any{"id": "org.example.tschat", "name": "Chat", "version": version, "runtime": "js", "main": "main.js",
			"permissions": []string{ui.PermNet, ui.PermKeepalive}, "purposes": purposes})
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "main.js"), []byte(`import * as kario from "kario"; // `+version), 0o644)
		pk, err := pkg.Build(dir, dev)
		if err != nil {
			t.Fatal(err)
		}
		return pk
	}
	if code := call(t, "POST", ts.URL+"/v1/submissions", "", script("1.0", nil), &out); code == 200 || !strings.Contains(out.Error, "needs a purpose") {
		t.Fatalf("a TypeScript app without a purpose: %d %q", code, out.Error)
	}
	if code, st := submit(t, ts, script("1.0", why)); code != 200 || st != "pending_review" {
		t.Fatalf("a TypeScript app: %d %s", code, st)
	}
	call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.tschat/1.0", token, map[string]string{}, nil)
	code, st = submit(t, ts, script("1.1", why))
	if p := srv.pending["org.example.tschat@1.1"]; code != 200 || st != "pending_review" || !strings.Contains(p.Why, "Messages from our own servers") {
		t.Fatalf("a TypeScript app's update: %d %s %q", code, st, p.Why)
	}
}
