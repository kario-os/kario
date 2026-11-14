package shell

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

func TestUpdateStatus(t *testing.T) {
	dir := t.TempDir()
	c := &Core{}
	c.update.slots = dir
	c.update.rauc = "true" // any command on PATH stands in for rauc
	if s := c.updateStatus(); s.Supported || s.Version != shownVersion() {
		t.Fatalf("without the state partition: %+v", s)
	}
	if err := os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=b\ngood=a\ntries=3\nbooted=a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := c.updateStatus()
	if !s.Supported || s.Slot != "a" || s.Good != "a" || s.Pending != "b" || s.State != "idle" {
		t.Fatalf("after an install: %+v", s)
	}
	c.update.rauc = "no-such-rauc"
	if c.updateStatus().Supported {
		t.Fatal("supported without rauc")
	}
}

func TestFetchOffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/updates/latest.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"version":"0.7","size":123,"url":"phone.raucb",
			"deltas":[{"from":"0.6","from_root_hash":"ab","url":"delta/0.6.raucb","size":5},{"from":"0.5","url":""}],"delta":{"from":"0.1","url":"x"}}`))
	}))
	defer srv.Close()
	o, err := fetchOffer(srv.URL+"/updates/latest.json", nil, "", time.Now())
	if err != nil || o.Version != "0.7" || o.URL != srv.URL+"/updates/phone.raucb" {
		t.Fatalf("%+v %v", o, err)
	}
	// Incremental bundles relative to latest.json too; the choice is the phone's.
	if len(o.Deltas) != 1 || o.Deltas[0].URL != srv.URL+"/updates/delta/0.6.raucb" || o.Deltas[0].FromRootHash != "ab" || o.Delta != nil {
		t.Fatalf("deltas %+v, delta %+v", o.Deltas, o.Delta)
	}
	if _, err := fetchOffer(srv.URL+"/none/latest.json", nil, "", time.Now()); err == nil {
		t.Fatal("a missing manifest was accepted")
	}
}

// With the update server's key configured, latest.json must be signed,
// recent, and not older than what the phone already saw.
func TestSignedOffer(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	body := []byte(`{"version":"0.8.0","url":"phone.raucb"}`)
	issued := time.Now().Add(-time.Hour).Unix()
	sign := func(w http.ResponseWriter, at int64, b []byte) {
		w.Header().Set("X-Offer-Issued", strconv.FormatInt(at, 10))
		w.Header().Set("X-Offer-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(priv, ota.OfferMessage(at, b))))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good/latest.json":
			sign(w, issued, body)
		case "/older/latest.json":
			sign(w, issued-60, body)
		case "/stale/latest.json":
			sign(w, time.Now().Add(-8*24*time.Hour).Unix(), body)
		case "/tampered/latest.json":
			sign(w, issued, body)
			_, _ = w.Write([]byte(`{"version":"9.9.9","url":"evil.raucb"}`))
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	seen := filepath.Join(t.TempDir(), "update-issued")
	if _, err := fetchOffer(srv.URL+"/unsigned/latest.json", pub, seen, time.Now()); err == nil {
		t.Fatal("an unsigned latest.json was accepted")
	}
	if o, err := fetchOffer(srv.URL+"/good/latest.json", pub, seen, time.Now()); err != nil || o.Version != "0.8.0" {
		t.Fatalf("signed: %+v %v", o, err)
	}
	for _, path := range []string{"/older/", "/stale/", "/tampered/"} {
		if _, err := fetchOffer(srv.URL+path+"latest.json", pub, seen, time.Now()); err == nil {
			t.Errorf("%s latest.json was accepted", path)
		}
	}
}

// A phone takes the incremental bundle made from its own slot image (root
// hash) or, where the server names no root hash, from its release version.
func TestPickDelta(t *testing.T) {
	ab, cd := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	deltas := []updateDelta{
		{From: "0.7.0", URL: "by-version"},
		{From: "0.7.1", FromRootHash: ab, URL: "by-image"},
		{From: "0.6.0", FromRootHash: cd, URL: "dev-image"},
	}
	for _, c := range []struct {
		version, root, want string
	}{
		{"0.7.0", "", "by-version"},
		{"0.7.1", ab, "by-image"},
		{"0.7.1", strings.ToUpper(ab), "by-image"},
		{"0.7.1", "", ""},                       // the server says which image; the phone cannot tell
		{"0.6.0", strings.Repeat("ef", 32), ""}, // the version of a delta, another image
		{"0.6 (prototype)", cd, "dev-image"},
		{"0.6 (prototype)", "", ""}, // a development build is no release version
		{"0.5.0", cd, "dev-image"},
	} {
		got := ""
		if d := pickDelta(deltas, c.version, c.root); d != nil {
			got = d.URL
		}
		if got != c.want {
			t.Errorf("%s %s: %q, want %q", c.version, c.root, got, c.want)
		}
	}
	if pickDelta(nil, "0.7.0", ab) != nil {
		t.Error("a delta out of none")
	}
}

func TestRunningRootHash(t *testing.T) {
	dir := t.TempDir()
	if runningRootHash(dir) != "" {
		t.Fatal("a root hash without a state partition")
	}
	desc := "data_blocks=2\nhash_offset=8192\nsalt=00ff\nroot_hash=" + strings.Repeat("5a", 32) + "\n"
	_ = os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=b\ngood=b\ntries=0\nbooted=b\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "rootfs_b.verity"), []byte(desc), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "rootfs_a.verity"), []byte(strings.Replace(desc, "5a", "11", -1)), 0o644)
	if got := runningRootHash(dir); got != strings.Repeat("5a", 32) {
		t.Fatalf("root hash %q", got)
	}
	_ = os.WriteFile(filepath.Join(dir, "rootfs_b.verity"), []byte("root_hash=nothing"), 0o644)
	if runningRootHash(dir) != "" {
		t.Fatal("a root hash from a malformed descriptor")
	}
}

// The incremental bundle installs first; when it does not (another
// version, a damaged payload), the full bundle does.
func TestInstallDelta(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	rauc := filepath.Join(dir, "rauc")
	_ = os.WriteFile(rauc, []byte("#!/bin/sh\necho \"$2\" >> "+calls+"\ncase \"$2\" in *bad*) echo 'failed to run slot hook' >&2; exit 1;; esac\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644)
	install := func(delta string) (string, UpdateStatus) {
		_ = os.Remove(calls)
		c := &Core{cfg: Config{StateDir: dir}, log: log.New(io.Discard, "", 0), ev: make(chan func(), 16)}
		c.update.slots, c.update.rauc = dir, rauc
		c.update.offer = &updateOffer{Version: "0.8.0", URL: "http://x/full.raucb"}
		if delta != "" {
			c.update.offer.Delta = &updateDelta{From: "0.7.0", URL: "http://x/" + delta}
		}
		done := make(chan UpdateStatus, 1)
		c.updateInstall(func(v any, err error) {
			st, _ := v.(UpdateStatus)
			done <- st
		})
		for {
			select {
			case f := <-c.ev:
				f()
			case st := <-done:
				raw, _ := os.ReadFile(calls)
				return strings.TrimSpace(string(raw)), st
			case <-time.After(10 * time.Second):
				t.Fatal("no answer")
			}
		}
	}
	if got, st := install("delta.raucb"); got != "http://x/delta.raucb" || st.State != "installed" {
		t.Fatalf("incremental: rauc installed %q, %s", got, st.State)
	}
	if got, st := install("bad-delta.raucb"); got != "http://x/bad-delta.raucb\nhttp://x/full.raucb" || st.State != "installed" {
		t.Fatalf("incremental failing: rauc installed %q, %s", got, st.State)
	}
	if got, st := install(""); got != "http://x/full.raucb" || st.State != "installed" {
		t.Fatalf("full: rauc installed %q, %s", got, st.State)
	}
}

func TestHeldRollout(t *testing.T) {
	pct := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		o      updateOffer
		bucket int
		held   bool
	}{
		{updateOffer{}, 9999, false},
		{updateOffer{Rollout: pct(0)}, 0, true},
		{updateOffer{Rollout: pct(10)}, 999, false},
		{updateOffer{Rollout: pct(10)}, 1000, true},
		{updateOffer{Rollout: pct(100)}, 9999, false},
		{updateOffer{Rollout: pct(100), Paused: true}, 0, true},
	} {
		if got := held(&c.o, c.bucket) != ""; got != c.held {
			t.Errorf("%+v at bucket %d: held %v", c.o, c.bucket, got)
		}
	}
	core := &Core{cfg: Config{StateDir: t.TempDir()}}
	_ = os.MkdirAll(filepath.Join(core.cfg.StateDir, "shell"), 0o700)
	if a, b := core.updateBucket(), core.updateBucket(); a != b || a < 0 || a >= 10000 {
		t.Fatalf("bucket %d then %d", a, b)
	}
}

// A release is offered only what comes after it.
func TestReleaseVersion(t *testing.T) {
	for v, release := range map[string]bool{"0.8.0": true, "1.2": true, "0.8.0-rc1": true, "0.6 (prototype)": false, shownVersion(): false, "": false, "delta": false} {
		if releaseVersion.MatchString(v) != release {
			t.Errorf("releaseVersion(%q) = %v", v, !release)
		}
	}
}

func TestVersionNewer(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"0.8.0", "0.7.0", true},
		{"0.7.0", "0.7.0", false},
		{"0.7.0", "0.8.0", false},
		{"0.10.0", "0.9.2", true},
		{"0.8", "0.8.0", false},
		{"0.8.1", "0.8", true},
		{"0.8.0", "0.8.0-rc1", true},
		{"0.8.0-rc2", "0.8.0-rc1", true},
		{"0.8.0-rc1", "0.8.0", false},
		{"1.0.0", "0.99.99", true},
	} {
		if got := versionNewer(c.a, c.b); got != c.newer {
			t.Errorf("versionNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestRaucError(t *testing.T) {
	for out, want := range map[string]string{
		"signature verification failed: unable to get local issuer certificate":                 tr.T("The update's signature is not trusted; it was not installed"),
		"Compatible mismatch: Expected 'kario-qemuarm64' but bundle manifest has 'kario-other'": tr.T("This update is not for this device"),
		"Failed to download bundle https://example/phone.raucb":                                 tr.T("Downloading the update failed"),
		"Version mismatch: Expected at least '0.8.0' but bundle manifest has '0.7.0'":           tr.T("This update is older than the system on the phone and cannot be installed"),
		"no space left on device": tr.T("Installation failed"),
	} {
		if got := raucError(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

func TestUpdateOutcome(t *testing.T) {
	for boot, want := range map[string]string{
		"slot=b\ngood=a\ntries=2\nbooted=b\n": "",         // on trial
		"slot=b\ngood=b\ntries=0\nbooted=b\n": "good",     // marked good
		"slot=a\ngood=a\ntries=0\nbooted=a\n": "rollback", // tries spent
	} {
		if got := updateOutcome(boot, "b"); got != want {
			t.Errorf("%q: %q, want %q", boot, got, want)
		}
	}
}

// The verdict goes to the server the update came from, signed by the
// device's keystore for ota.ReportApp over the report's fields.
func TestReportUpdate(t *testing.T) {
	got := make(chan ota.Report, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep ota.Report
		if r.URL.Path == "/updates/report" && json.NewDecoder(r.Body).Decode(&rep) == nil {
			got <- rep
		}
	}))
	defer srv.Close()
	dir, run := t.TempDir(), t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	go wire.Serve(filepath.Join(run, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ App, Nonce string }
		_ = json.Unmarshal(params, &p)
		return ks.Attest(p.App, p.Nonce, "")
	})
	c := &Core{cfg: Config{StateDir: dir, RunDir: run}, log: log.New(io.Discard, "", 0)}
	c.broker = &broker{c: c, clients: map[string]*wire.Client{}}
	c.update.slots, c.update.rauc = dir, "true"
	_ = os.MkdirAll(filepath.Join(dir, "shell"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=b\ngood=b\ntries=0\nbooted=b\n"), 0o644)
	c.rememberInstall(updateReport{Version: "0.7", Slot: "b", Server: srv.URL + "/updates"})
	c.reportUpdate()
	select {
	case rep := <-got:
		roots, _ := keystore.LoadRootPub(keystore.RootPubPath(dir))
		if rep.Version != "0.7" || rep.Outcome != "good" || rep.Kind != "" || ota.VerifyReport(rep, roots) != nil {
			t.Fatalf("reported %+v", rep)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reported")
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(c.reportPath()); os.IsNotExist(err) {
			var day updateReport
			raw, _ := os.ReadFile(c.dayReportPath())
			if json.Unmarshal(raw, &day) != nil || day.Kind != ota.KindDay || day.GoodAt.IsZero() {
				t.Fatalf("no day report waiting: %s", raw)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the report was kept after the server took it")
}

// A day after a good install the phone reports again, signed, with the app
// crashes recorded since (a phone off meanwhile sends it at its next start).
func TestDayReport(t *testing.T) {
	got := make(chan ota.Report, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep ota.Report
		if json.NewDecoder(r.Body).Decode(&rep) == nil {
			got <- rep
		}
	}))
	defer srv.Close()
	dir, run := t.TempDir(), t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	go wire.Serve(filepath.Join(run, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ App, Nonce string }
		_ = json.Unmarshal(params, &p)
		return ks.Attest(p.App, p.Nonce, "")
	})
	c := &Core{cfg: Config{StateDir: dir, RunDir: run}, log: log.New(io.Discard, "", 0)}
	c.broker = &broker{c: c, clients: map[string]*wire.Client{}}
	c.update.slots, c.update.rauc = dir, "true"
	_ = os.MkdirAll(filepath.Join(dir, "shell"), 0o700)
	goodAt := time.Now().Add(-25 * time.Hour)
	crashes, _ := json.Marshal([]CrashRecord{{Time: goodAt.Add(-time.Hour), App: "a"}, {Time: goodAt.Add(time.Hour), App: "a"}, {Time: goodAt.Add(2 * time.Hour), App: "b"}})
	_ = os.WriteFile(c.crashFile(), crashes, 0o600)
	day, _ := json.Marshal(updateReport{Version: "0.7", Server: srv.URL, Outcome: ota.OutcomeGood, Kind: ota.KindDay, GoodAt: goodAt})
	_ = os.WriteFile(c.dayReportPath(), day, 0o600)
	c.reportDay()
	select {
	case rep := <-got:
		roots, _ := keystore.LoadRootPub(keystore.RootPubPath(dir))
		if rep.Kind != ota.KindDay || rep.Crashes != 2 || ota.VerifyReport(rep, roots) != nil {
			t.Fatalf("day report %+v", rep)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no day report")
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(c.dayReportPath()); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the day report was kept after the server took it")
}
