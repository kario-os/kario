package shell

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kario-os/kario/ota"
)

// The calendar is taken only as the update server signed it, for this
// file, lately, and not older than the one kept; then it is kept.
func TestFetchHolidays(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	// A test fixture, not an official calendar.
	body := `{"format": "kario-holidays/1", "regions": {"CN": {"source": "test fixture", "days_off": ["2031-11-19"], "workdays": ["2031-11-15"]}}}`
	now := time.Now()
	var issued int64
	var serve string
	var msg func(int64, []byte) []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if issued != 0 {
			w.Header().Set("X-Offer-Issued", strconv.FormatInt(issued, 10))
			w.Header().Set("X-Offer-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(key, msg(issued, []byte(body)))))
		}
		_, _ = w.Write([]byte(serve))
	}))
	defer srv.Close()
	url := srv.URL + "/" + ota.HolidaysFile
	path := filepath.Join(t.TempDir(), ota.HolidaysFile)
	data := func(i int64, b []byte) []byte { return ota.DataMessage(ota.HolidaysFile, i, b) }

	issued, serve, msg = now.Unix(), body, data
	h, err := fetchHolidayFile(url, pub, path, now)
	if err != nil {
		t.Fatal(err)
	}
	off, work := h.regionDays("CN")
	if len(off) != 1 || off[0] != "2031-11-19" || len(work) != 1 || work[0] != "2031-11-15" {
		t.Fatalf("CN: %v %v", off, work)
	}
	if off, work := h.regionDays("MX"); off != nil || work != nil {
		t.Fatalf("a region the calendar does not have: %v %v", off, work)
	}
	if kept := loadHolidays(path); kept == nil || kept.Issued != issued {
		t.Fatalf("kept %+v", kept)
	}
	for why, c := range map[string]struct {
		issued int64
		serve  string
		msg    func(int64, []byte) []byte
	}{
		"unsigned":             {0, body, data},
		"changed":              {now.Unix() + 1, body + " ", data},
		"signed as an offer":   {now.Unix() + 1, body, ota.OfferMessage},
		"older than the kept":  {now.Unix() - 60, body, data},
		"signed long ago":      {now.Add(-8 * 24 * time.Hour).Unix(), body, data},
		"signed in the future": {now.Add(48 * time.Hour).Unix(), body, data},
	} {
		issued, serve, msg = c.issued, c.serve, c.msg
		if _, err := fetchHolidayFile(url, pub, path, now); err == nil {
			t.Errorf("%s: taken", why)
		}
	}
	if kept := loadHolidays(path); kept == nil || kept.Issued != now.Unix() {
		t.Fatalf("a refused calendar replaced the one kept: %+v", kept)
	}
	// Without a key (no update server key configured) as latest.json:
	// unsigned is taken, but it must still be a calendar.
	issued, serve = 0, `{"format": "kario-holidays/1", "regions": {"CN": {"days_off": ["2031-02-30"]}}}`
	if _, err := fetchHolidayFile(url, nil, filepath.Join(t.TempDir(), "h.json"), now); err == nil {
		t.Fatal("a broken calendar was taken")
	}
	serve = body
	if _, err := fetchHolidayFile(url, nil, filepath.Join(t.TempDir(), "h.json"), now); err != nil {
		t.Fatalf("unsigned without a key: %v", err)
	}
}
