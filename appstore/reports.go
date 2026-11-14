package appstore

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kario-os/kario/pkg"
)

// Report is someone's complaint about a published app, kept until the staff
// handle it.
type Report struct {
	ID      string `json:"id"`
	App     string `json:"app"`
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason"`
	Contact string `json:"contact,omitempty"`
	At      string `json:"at"`
	// Handled: what the staff did, and who.
	Handled string `json:"handled,omitempty"`
	By      string `json:"by,omitempty"`
}

// reportsPerHour bounds the reports from one address.
const reportsPerHour = 20

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	var req struct {
		App     string `json:"app"`
		Reason  string `json:"reason"`
		Contact string `json:"contact"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		Fail(w, 400, err)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len([]rune(reason)) > 500 || len(req.Contact) > 100 {
		Fail(w, 400, errors.New("a report needs a reason (at most 500 characters)"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.catalog[req.App]
	if !ok {
		Fail(w, 404, errors.New("not in the catalogue"))
		return
	}
	if !Allowed(&s.reportTimes, ClientAddr(r), reportsPerHour) {
		Fail(w, 429, errors.New("too many reports from this address; try again later"))
		return
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	rep := Report{ID: hex.EncodeToString(b), App: e.ID, Version: e.Version, Reason: reason,
		Contact: strings.TrimSpace(req.Contact), At: time.Now().UTC().Format(time.RFC3339)}
	s.reports = append(s.reports, rep)
	s.save()
	log.Printf("report %s on %s: %s", rep.ID, rep.App, rep.Reason)
	WriteJSON(w, 200, map[string]string{"id": rep.ID})
}

// HandleReport records how the staff member by handled the report.
func (s *Server) HandleReport(id, handled, by string) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.reports {
		if s.reports[i].ID == id {
			s.reports[i].Handled, s.reports[i].By = handled, by
			s.save()
			return s.reports[i], nil
		}
	}
	return Report{}, errors.New("no such report")
}

// openReports are the reports not handled yet, newest first.
func (s *Server) openReports() []Report {
	out := []Report{}
	for _, rep := range s.reports {
		if rep.Handled == "" {
			out = append(out, rep)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// Catalog is what is published, by name.
func (s *Server) Catalog() []Entry {
	s.mu.Lock()
	out := make([]Entry, 0, len(s.catalog))
	for _, e := range s.catalog {
		out = append(out, e)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Allowed counts a request from addr in times and says whether it is
// within perHour.
func Allowed(times *map[string][]time.Time, addr string, perHour int) bool {
	now := time.Now()
	if *times == nil {
		*times = map[string][]time.Time{}
	}
	m := *times
	kept := m[addr][:0]
	for _, t := range m[addr] {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	if len(kept) >= perHour {
		m[addr] = kept
		return false
	}
	m[addr] = append(kept, now)
	return true
}

// Who names the staff member behind the request: Staff when set, else
// "staff" for the shared admin token, "" for none.
func (s *Server) Who(r *http.Request) string {
	if s.Staff != nil {
		return s.Staff(r)
	}
	got := []byte(r.Header.Get("Authorization"))
	if s.admin != "" && subtle.ConstantTimeCompare(got, []byte("Bearer "+s.admin)) == 1 {
		return "staff"
	}
	return ""
}

func (s *Server) audit(r *http.Request, action, detail string) {
	if s.Audit != nil {
		s.Audit(r, action, detail)
	}
}

// ClientAddr is the address a request came from: the first hop a proxy in
// front reports (X-Forwarded-For), else the connection's.
func ClientAddr(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		first, _, _ := strings.Cut(fwd, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Open is the store's root key in dir (made on first start), named
// "store", its public key written to dir/store.pub for the devices.
func Open(dir string) (*pkg.Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	store, err := pkg.DevStore(dir, dir)
	if err != nil {
		return nil, err
	}
	store.Name = "store"
	return store, os.WriteFile(filepath.Join(dir, "store.pub"), []byte(PublicText(store)), 0o644)
}

// PublicText is the store root's public key as devices read it.
func PublicText(store *pkg.Store) string { return b64.EncodeToString(store.Public()) + "\n" }
