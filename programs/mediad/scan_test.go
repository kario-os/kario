package mediad

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

func scanServer(t *testing.T, code string) *server {
	t.Helper()
	s := &server{started: time.Now(), streams: map[string]media.Source{}, calls: map[string]*call{}, scans: map[string]*scan{}, players: map[string]*player{}}
	if code != "" {
		var err error
		if s.card, err = media.NewCard(code); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s *server) do(t *testing.T, method string, params any) (any, error) {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return s.handle(method, b)
}

func (s *server) open(t *testing.T) string {
	t.Helper()
	res, err := s.do(t, svc.MediaScanOpen, map[string]string{"app": "com.phone.wallet"})
	if err != nil {
		t.Fatal(err)
	}
	return res.(map[string]string)["scan_id"]
}

type scanResult struct {
	text string
	err  error
}

// wait answers on the channel when scan.wait does.
func (s *server) wait(t *testing.T, id string) <-chan scanResult {
	ch := make(chan scanResult, 1)
	go func() {
		b, _ := json.Marshal(map[string]string{"scan_id": id})
		res, err := s.handle(svc.MediaScanWait, b)
		r := scanResult{err: err}
		if m, ok := res.(map[string]string); ok {
			r.text = m["text"]
		}
		ch <- r
	}()
	return ch
}

func (s *server) tries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanInfo().Open
}

// TestScanReadsTheTable: the synthetic camera's table, the shop's code on it,
// is read from the frames drawn.
func TestScanReadsTheTable(t *testing.T) {
	s := scanServer(t, demoMerchantCode)
	id := s.open(t)
	f := media.NewFrame(192, 192)
	s.source(id)(0.5, f)
	if r, g, b := f.At(96, 96); r == g && g == b && r < 40 {
		t.Fatalf("the viewfinder shows no card: %d %d %d", r, g, b)
	}
	select {
	case r := <-s.wait(t, id):
		if r.err != nil || r.text != demoMerchantCode {
			t.Fatalf("%q, %v", r.text, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no code read in 10 s")
	}
	if s.source(id) != nil {
		t.Fatal("the scan's stream is still there")
	}
	s.mu.Lock()
	st := s.scanInfo()
	s.mu.Unlock()
	if st.Found != 1 || st.Tries < 1 || st.MaxMS <= 0 || st.W != scanW || st.Open != -1 {
		t.Fatalf("stats %+v", st)
	}
}

// TestScanWaitsForACode: with nothing in front of the camera the scan goes
// on decoding, and reads the code once one is held up.
func TestScanWaitsForACode(t *testing.T) {
	s := scanServer(t, "")
	id := s.open(t)
	got := s.wait(t, id)
	deadline := time.Now().Add(5 * time.Second)
	for s.tries() < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := s.tries(); n < 3 {
		t.Fatalf("%d tries in 5 s", n)
	}
	select {
	case r := <-got:
		t.Fatalf("read %q, %v from a frame without a code", r.text, r.err)
	default:
	}
	const wifi = "WIFI:S:Kario;T:WPA;P:kario-wifi-1;;"
	if _, err := s.do(t, svc.MediaSimCode, map[string]string{"text": wifi}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err != nil || r.text != wifi {
			t.Fatalf("%q, %v", r.text, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the code held up was not read in 10 s")
	}
}

func TestScanClose(t *testing.T) {
	s := scanServer(t, "")
	id := s.open(t)
	got := s.wait(t, id)
	time.Sleep(600 * time.Millisecond)
	if _, err := s.do(t, svc.MediaScanClose, map[string]string{"scan_id": id}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err == nil || r.err.Error() != ui.ErrCancelled {
			t.Fatalf("%q, %v", r.text, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan.wait did not end with scan.close")
	}
	s.mu.Lock()
	n := s.stats.Tries
	s.mu.Unlock()
	time.Sleep(3 * scanEvery)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats.Tries != n {
		t.Fatalf("still decoding after close: %d tries, then %d", n, s.stats.Tries)
	}
}

func TestScansBounded(t *testing.T) {
	s := scanServer(t, "")
	var ids []string
	for i := 0; i < maxScans; i++ {
		ids = append(ids, s.open(t))
	}
	if _, err := s.do(t, svc.MediaScanOpen, nil); err == nil {
		t.Fatal("a scan past the bound opened")
	}
	for _, id := range ids {
		if _, err := s.do(t, svc.MediaScanClose, map[string]string{"scan_id": id}); err != nil {
			t.Fatal(err)
		}
	}
	s.open(t)
}

// TestSimCodeOnlyForTestCameras: the hook is refused with a sensor.
func TestSimCodeOnlyForTestCameras(t *testing.T) {
	old := cam
	defer func() { cam = old }()
	s := scanServer(t, "")
	for driver, ok := range map[string]bool{"vivid": true, "uvcvideo": false, "qcom-camss": false} {
		cam = &media.V4L2{Driver: driver}
		if _, err := s.do(t, svc.MediaSimCode, map[string]string{"text": "hello"}); (err == nil) != ok {
			t.Errorf("%s: %v", driver, err)
		}
	}
	anyCameraCodes = true
	if _, err := s.do(t, svc.MediaSimCode, map[string]string{"text": "hello"}); err != nil {
		t.Errorf("uvcvideo with --test-codes: %v", err)
	}
	anyCameraCodes = false
	cam = nil
	if _, err := s.do(t, svc.MediaSimCode, map[string]string{"text": ""}); err != nil || s.card != nil {
		t.Fatalf("taking the code away: %v, %v", err, s.card)
	}
	if _, err := s.do(t, svc.MediaSimCode, map[string]string{"text": string(make([]byte, 4000))}); err == nil {
		t.Fatal("a code too long for a QR code was held up")
	}
}
