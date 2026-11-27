package location

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ap(n int, ssid string, dbm int) AccessPoint {
	return AccessPoint{BSSID: fmt.Sprintf("00:1a:2b:3c:4d:%02x", n), SSID: ssid, SignalDBm: dbm}
}

var cellBJ = Cell{Radio: "lte", MCC: 460, MNC: 0, Area: 4321, ID: 123456789, SignalDBm: -85}

// Only what the user may be placed by leaves the phone: no hidden network,
// none whose owner opted out by its name, no randomised or multicast
// address; the strongest first, at most maxAPs, never one alone.
func TestUsable(t *testing.T) {
	o := Observation{WiFi: []AccessPoint{
		ap(1, "Home", -70),
		ap(2, "Cafe_nomap", -40),
		ap(3, "Office_OptOut", -45),
		ap(4, "", -50),
		{BSSID: "00:1a:2b:3c:4d:05", SSID: "Secret", SignalDBm: -42, Hidden: true},
		{BSSID: "02:1a:2b:3c:4d:06", SSID: "Hotspot", SignalDBm: -30},
		{BSSID: "01:00:5e:00:00:07", SSID: "Multicast", SignalDBm: -30},
		{BSSID: "00:00:00:00:00:00", SSID: "Zero", SignalDBm: -30},
		{BSSID: "nonsense", SSID: "Bad"},
		ap(8, "Library", -60),
		{BSSID: "00:1A:2B:3C:4D:08", SSID: "Library", SignalDBm: -61},
	}, Cells: []Cell{cellBJ, {Radio: "lte", MCC: 460}, {Radio: "cdma", MCC: 460, Area: 1, ID: 1}}}
	u := o.Usable()
	if len(u.WiFi) != 2 || u.WiFi[0].BSSID != "00:1a:2b:3c:4d:08" || u.WiFi[1].BSSID != "00:1a:2b:3c:4d:01" {
		t.Fatalf("%+v", u.WiFi)
	}
	if len(u.Cells) != 1 || u.Cells[0] != cellBJ {
		t.Fatalf("%+v", u.Cells)
	}
	if one := (Observation{WiFi: []AccessPoint{ap(1, "Home", -70), ap(2, "x_nomap", -40)}}).Usable(); one.WiFi != nil {
		t.Fatalf("one access point sent alone: %+v", one.WiFi)
	}
	var many Observation
	for i := 0; i < 30; i++ {
		many.WiFi = append(many.WiFi, ap(i, "n", -90+i))
	}
	if u := many.Usable(); len(u.WiFi) != maxAPs || u.WiFi[0].SignalDBm != -61 {
		t.Fatalf("%d sent, the first at %d dBm", len(u.WiFi), u.WiFi[0].SignalDBm)
	}
}

// The provider speaks the Geolocation API: the request names the access
// points and the cell, never asks to be placed by the address it comes
// from, and carries the key; the key never shows in an error.
func TestHTTPProvider(t *testing.T) {
	const key = "k3y-0f-the-owner"
	var got geoRequest
	status, answer := http.StatusOK, `{"location": {"lat": 39.9042, "lng": 116.4074}, "accuracy": 35.5}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("key") != key || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		got = geoRequest{}
		if err := json.Unmarshal(raw, &got); err != nil || strings.Contains(string(raw), "_nomap") {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	defer srv.Close()
	p := &HTTPProvider{URL: srv.URL + "/v1/geolocate", Key: key}
	o := Observation{WiFi: []AccessPoint{ap(1, "Home", -70), ap(2, "Shop", -60)}, Cells: []Cell{cellBJ}}.Usable()
	f, err := p.Locate(context.Background(), o)
	if err != nil || f.Lat != 39.9042 || f.Lon != 116.4074 || f.AccuracyM != 35.5 || !f.Network {
		t.Fatalf("%+v %v", f, err)
	}
	if got.ConsiderIP || got.Fallbacks.IP || got.Fallbacks.LAC || len(got.WiFi) != 2 || got.WiFi[0].MAC != "00:1a:2b:3c:4d:02" ||
		len(got.Cells) != 1 || got.Cells[0].ID != cellBJ.ID || got.Cells[0].Radio != "lte" || got.Cells[0].MCC != 460 {
		t.Fatalf("request %+v", got)
	}
	status, answer = http.StatusNotFound, `{"error": {"code": 404, "message": "Not found"}}`
	if _, err := p.Locate(context.Background(), o); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	status, answer = http.StatusForbidden, `{"error": {"code": 403, "message": "API key `+key+` is not valid"}}`
	if _, err := p.Locate(context.Background(), o); err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "403") {
		t.Fatalf("403: %v", err)
	}
	status, answer = http.StatusOK, `{"location": {"lat": 91, "lng": 0}, "accuracy": 10}`
	if _, err := p.Locate(context.Background(), o); err == nil {
		t.Fatal("a latitude of 91 taken")
	}
	srv.Close()
	if _, err := p.Locate(context.Background(), o); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("no server: %v", err)
	}
	if _, err := (&HTTPProvider{URL: "file:///etc/passwd"}).Locate(context.Background(), o); err == nil {
		t.Fatal("a file URL taken")
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) step(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock                { return &clock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)} }
func obs(cell bool, aps ...AccessPoint) Observation {
	o := Observation{WiFi: aps}
	if cell {
		o.Cells = []Cell{cellBJ}
	}
	return o
}

// An answer is kept: the same networks, or most of them, are placed again
// from the phone, without the service; a cell alone only by that cell.
func TestLocatorCache(t *testing.T) {
	fake, ck := &FakeProvider{}, newClock()
	l := &Locator{Provider: fake, Now: ck.now}
	if _, err := l.Locate(context.Background(), obs(false, ap(1, "a", -60))); !errors.Is(err, ErrNoNetworks) {
		t.Fatalf("one access point: %v", err)
	}
	f, err := l.Locate(context.Background(), obs(true, ap(1, "a", -60), ap(2, "b", -70), ap(3, "c", -80)))
	if err != nil || f.AccuracyM != FakeWiFiAccuracyM || fake.Calls.Load() != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	for _, o := range []Observation{
		obs(true, ap(1, "a", -61), ap(2, "b", -69), ap(3, "c", -80)),
		obs(false, ap(2, "b", -70), ap(3, "c", -80), ap(9, "new", -85)),
		obs(true, ap(1, "a", -60), ap(3, "c", -75)),
	} {
		ck.step(time.Second)
		if f, err := l.Locate(context.Background(), o); err != nil || !f.Network || fake.Calls.Load() != 1 {
			t.Fatalf("%+v: %+v %v, %d calls", o, f, err, fake.Calls.Load())
		}
	}
	ck.step(time.Minute)
	if _, err := l.Locate(context.Background(), obs(true)); err != nil || fake.Calls.Load() != 2 {
		t.Fatalf("a cell alone, not asked before: %v, %d calls", err, fake.Calls.Load())
	}
	ck.step(time.Minute)
	if f, err := l.Locate(context.Background(), obs(true)); err != nil || f.AccuracyM != FakeCellAccuracyM || fake.Calls.Load() != 2 {
		t.Fatalf("the cell again: %+v %v", f, err)
	}
	ck.step(netKeep + time.Hour)
	if _, err := l.Locate(context.Background(), obs(true)); err != nil || fake.Calls.Load() != 3 {
		t.Fatalf("a week later: %v, %d calls", err, fake.Calls.Load())
	}
	if st := l.Stats(); st.Requests != 3 || st.Cached != 4 {
		t.Fatalf("%+v", st)
	}
}

type failing struct {
	calls int
	err   error
}

func (p *failing) Locate(context.Context, Observation) (Fix, error) {
	p.calls++
	return Fix{}, p.err
}

// The service is asked at most so often; one that fails is left alone,
// longer each time; "not known" is believed for an hour.
func TestLocatorLimits(t *testing.T) {
	fake, ck := &FakeProvider{}, newClock()
	l := &Locator{Provider: fake, Now: ck.now, MaxPerHour: 3}
	ask := func(n int) error {
		_, err := l.Locate(context.Background(), obs(false, ap(10*n, "a", -60), ap(10*n+1, "b", -60)))
		return err
	}
	if err := ask(1); err != nil {
		t.Fatal(err)
	}
	ck.step(5 * time.Second)
	if err := ask(2); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("5 s after the last: %v", err)
	}
	for n := 2; n <= 3; n++ {
		ck.step(netMinGap)
		if err := ask(n); err != nil {
			t.Fatal(n, err)
		}
	}
	ck.step(time.Minute)
	if err := ask(4); !errors.Is(err, ErrRateLimited) || fake.Calls.Load() != 3 {
		t.Fatalf("a fourth in the hour: %v, %d calls", err, fake.Calls.Load())
	}
	ck.step(time.Hour)
	if err := ask(4); err != nil {
		t.Fatalf("an hour later: %v", err)
	}

	bad := &failing{err: errors.New("network location: example.org: 503 Service Unavailable")}
	l = &Locator{Provider: bad, Now: ck.now}
	for i, wait := range []time.Duration{netBackoff, 2 * netBackoff, 4 * netBackoff} {
		if err := ask(1); err == nil || errors.Is(err, ErrRateLimited) || bad.calls != i+1 {
			t.Fatalf("try %d: %v", i, err)
		}
		ck.step(wait - time.Second)
		if err := ask(1); !errors.Is(err, ErrRateLimited) || bad.calls != i+1 {
			t.Fatalf("a second before %s: %v", wait, err)
		}
		ck.step(time.Second)
	}
	if st := l.Stats(); !strings.Contains(st.Error, "503") {
		t.Fatalf("%+v", st)
	}
	bad.err = ErrNotFound
	if err := ask(1); !errors.Is(err, ErrNotFound) || bad.calls != 4 {
		t.Fatal(err)
	}
	ck.step(netMinGap)
	if err := ask(1); !errors.Is(err, ErrNotFound) || bad.calls != 4 {
		t.Fatalf("asked again about networks it does not know: %v, %d calls", err, bad.calls)
	}
	ck.step(netKeepMiss)
	if ask(1); bad.calls != 5 {
		t.Fatalf("an hour later: %d calls", bad.calls)
	}
}

// The answers outlast a restart, in a file only the system reads; Forget
// takes them off the phone.
func TestLocatorKeeps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netloc.json")
	fake, ck := &FakeProvider{Where: func() (float64, float64) { return 22.5431, 114.0579 }}, newClock()
	o := obs(true, ap(1, "a", -60), ap(2, "b", -70))
	l := &Locator{Provider: fake, Path: path, Now: ck.now}
	if _, err := l.Locate(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	again := &Locator{Provider: fake, Path: path, Now: ck.now}
	if f, err := again.Locate(context.Background(), o); err != nil || f.Lat != 22.5431 || fake.Calls.Load() != 1 {
		t.Fatalf("after a restart: %+v %v, %d calls", f, err, fake.Calls.Load())
	}
	again.Forget()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the answers stay on the phone")
	}
	ck.step(netMinGap)
	if _, err := again.Locate(context.Background(), o); err != nil || fake.Calls.Load() != 2 {
		t.Fatalf("forgotten: %v, %d calls", err, fake.Calls.Load())
	}
}
