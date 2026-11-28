package pushd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
)

// The keep-alive rides other traffic at most once per half interval, on a
// clock that goes on while the phone is suspended. Go's monotonic clock
// stops then: a phone that slept 20 minutes between two wakes for an app's
// heartbeat (28-battery.md §8) looked to pushd as if seconds had passed,
// and the second wake carried nothing, so the gateway's ping woke the radio
// once more on its own.
func TestRideCountsTheTimeSuspended(t *testing.T) {
	var mu sync.Mutex
	var beats []int64
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			HeartbeatMS int64 `json:"heartbeat_ms"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/v1/device/heartbeat" {
			mu.Lock()
			beats = append(beats, body.HeartbeatMS)
			mu.Unlock()
		}
	}))
	defer gw.Close()
	boot := 3 * time.Hour
	d := &daemon{http: gw.Client(), hb: push.NewHeartbeat(), gw: gw.URL, connected: true,
		clock: func() time.Duration { return boot }}
	d.hb.Cur = 28 * time.Minute
	ride := func() bool {
		t.Helper()
		out, err := d.handle(svc.PushRadioActive, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]bool)["rode"]
	}
	if !ride() {
		t.Fatal("the first radio_active did not ride")
	}
	boot += 13 * time.Minute
	if ride() {
		t.Fatal("rode again within half the interval")
	}
	// Asleep for 20 minutes, awake for seconds of it.
	boot += 20 * time.Minute
	if !ride() {
		t.Fatal("33 minutes on, 20 of them suspended, the radio_active did not ride")
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		n := len(beats)
		mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the gateway was asked %d times to move its ping, want 2", n)
		}
	}
	if beats[0] != d.hb.Cur.Milliseconds() || beats[1] != beats[0] {
		t.Errorf("heartbeats asked for %v, want the interval %s twice", beats, d.hb.Cur)
	}

	a := sinceBoot()
	time.Sleep(20 * time.Millisecond)
	if b := sinceBoot(); a <= 0 || b-a < 20*time.Millisecond {
		t.Errorf("CLOCK_BOOTTIME read %s, then %s", a, b)
	}
}
