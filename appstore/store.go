// Package appstore is the store backend's open core (07-ecosystem-and-store.md
// §4.1): what devices and developers talk to. Developers register with an
// identity number (checked here, then verified by the store staff before
// the store root certifies their key) and submit signed packages, which
// pass automatic checks and, for a new app or new permissions, a human
// review; published apps appear in the signed catalogue devices download
// from, updates as deltas. Staff take apps down: they leave the catalogue
// and go on a revocation list, signed by the store root, that devices
// enforce. Devices send crash reports, which only the app's developer reads
// back (signed requests), and anyone may report an app.
//
// cmd/phone-storesrv runs it as the simulated store devices are tested
// against. A store built on it (the official one) sets the hooks: who the
// staff are, the audit of what they do, extra rules for a submission, more
// in the staff's queue, and adds its own routes to Routes().
package appstore

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

var b64 = base64.StdEncoding

// Entry is a published app in the catalogue.
type Entry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Developer   string   `json:"developer"`
	Summary     string   `json:"summary,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	// Purposes: why it asks for each runtime permission (its manifest).
	Purposes map[string]string `json:"purposes,omitempty"`
	// Privacy is its manifest's privacy label; none: not provided.
	Privacy   *ui.Privacy `json:"privacy,omitempty"`
	Size      int         `json:"size"`
	SHA256    string      `json:"sha256"`
	Published string      `json:"published"`
	// DeveloperCredit is the developer's unified social credit code when it
	// is a company, shown with the app (compliance.go); Filing the app's
	// APP filing number from its manifest.
	DeveloperCredit string `json:"developer_credit,omitempty"`
	Filing          string `json:"filing,omitempty"`
	// A staged version goes to Rollout percent of devices, Previous to the
	// rest (pkg.OfferedApps); Halted says why the store stopped it.
	Rollout  int          `json:"rollout,omitempty"`
	Previous *pkg.Release `json:"previous,omitempty"`
	Halted   string       `json:"halted,omitempty"`
	// Lang is the language of Name, Summary and Purposes (the manifest's
	// "lang"); Locales the same in other languages (its "locales").
	Lang    string               `json:"lang,omitempty"`
	Locales map[string]EntryText `json:"locales,omitempty"`
}

// EntryText is an entry's text in one language.
type EntryText struct {
	Name     string            `json:"name,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Purposes map[string]string `json:"purposes,omitempty"`
}

// Text is the entry's name and summary in lang, by its fallback chain
// (i18n.PickText).
func (e Entry) Text(lang string) (name, summary string) {
	locales := map[string]i18n.AppText{}
	for l, t := range e.Locales {
		locales[l] = i18n.AppText{Name: t.Name, Description: t.Summary}
	}
	name = i18n.PickText(lang, e.Lang, locales, e.Name, func(t i18n.AppText) string { return t.Name })
	summary = i18n.PickText(lang, e.Lang, locales, e.Summary, func(t i18n.AppText) string { return t.Description })
	return name, summary
}

func (e Entry) release() pkg.Release {
	r := pkg.Release{Version: e.Version, Size: e.Size, SHA256: e.SHA256, Permissions: e.Permissions}
	if e.Privacy != nil {
		r.Privacy, _ = json.Marshal(e.Privacy)
	}
	return r
}

// A staged version that crashes on haltCrashShare of the devices that
// downloaded it (at least haltCrashMin reports) is halted: devices go back
// to being offered the previous release until the developer acts.
const (
	haltCrashMin   = 5
	haltCrashShare = 10 // percent
)

type Crash struct {
	App     string   `json:"app"`
	Version string   `json:"version"`
	Reason  string   `json:"reason"`
	Time    string   `json:"time"`
	Log     []string `json:"log,omitempty"`
}

// Developer is a registered developer: verified ones have a certificate.
type Developer struct {
	Name   string `json:"name"`
	Pub    string `json:"pub"`
	Kind   string `json:"kind"`   // company | person
	Number string `json:"number"` // the identity number, masked
	// Credit is a company's unified social credit code in full: public
	// record, shown with its apps. A person's ID number is kept masked only.
	Credit string       `json:"credit,omitempty"`
	Status string       `json:"status"` // pending | verified | rejected
	Reason string       `json:"reason,omitempty"`
	Cert   *pkg.DevCert `json:"cert,omitempty"`
}

// Pending is a submission waiting for a human review, and the rollout its
// developer asked for (none: everyone).
type Pending struct {
	Entry
	Why    string `json:"why"`
	Stages *int   `json:"stages,omitempty"`
}

type Server struct {
	mu         sync.Mutex
	dir        string
	store      *pkg.Store
	admin      string
	catalog    map[string]Entry
	crashes    []Crash
	developers map[string]*Developer
	pending    map[string]Pending // id@version
	revoked    []pkg.Revoked
	// downloads of staged versions, id@version (the crash share's base).
	downloads map[string]int
	// reports.go: the reports on apps and their rate by address.
	reports     []Report
	reportTimes map[string][]time.Time

	// Staff names who a request comes from, "" for nobody (default: the
	// shared admin token, named "staff").
	Staff func(r *http.Request) string
	// Audit records what the staff did (default: nothing).
	Audit func(r *http.Request, action, detail string)
	// Accept is a store's own rule for a submission it would publish.
	Accept func(Manifest) error
	// Queue adds to what GET /v1/admin/queue lists.
	Queue func() map[string]any
}

// Manifest is what a submission's manifest says that the review looks at.
type Manifest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Permissions []string          `json:"permissions"`
	Purposes    map[string]string `json:"purposes"`
	Granted     []string          `json:"granted"`
	Filing      string            `json:"filing"`
	// A TypeScript app's runtime and module (jsrt.Check), and exec.
	Runtime string `json:"runtime"`
	Main    string `json:"main"`
	Exec    string `json:"exec"`
	// The manifest's text in other languages (i18n.AppText), and what it
	// may refer to.
	Lang     string                  `json:"lang"`
	Locales  map[string]i18n.AppText `json:"locales"`
	Settings []struct {
		Key     string   `json:"key"`
		Choices []string `json:"choices"`
	} `json:"settings"`
	Commands []struct {
		ID string `json:"id"`
	} `json:"commands"`
	Intents []struct {
		Action string `json:"action"`
	} `json:"intents"`
	NotifyActions []struct {
		ID string `json:"id"`
	} `json:"notify_actions"`
	NFC     []ui.NFCCard `json:"nfc"`
	Privacy *ui.Privacy  `json:"privacy"`
}

// textRules are what the installer checks a manifest's locales against.
func (m Manifest) textRules() i18n.AppTextRules {
	r := i18n.DefaultAppTextRules()
	r.MaxPurpose = ui.MaxPurpose
	r.Settings = map[string]int{}
	for _, st := range m.Settings {
		r.Settings[st.Key] = len(st.Choices)
	}
	r.Permissions = m.Permissions
	for _, c := range m.Commands {
		r.Commands = append(r.Commands, c.ID)
	}
	for _, d := range m.Intents {
		r.Intents = append(r.Intents, d.Action)
	}
	for _, a := range m.NotifyActions {
		r.NotifyActions = append(r.NotifyActions, a.ID)
	}
	return r
}

// New opens a store backend over dir with its root key; admin is the
// shared staff token ("" for none).
func New(dir string, store *pkg.Store, admin string) *Server {
	s := &Server{dir: dir, store: store, admin: admin, catalog: map[string]Entry{}, developers: map[string]*Developer{},
		pending: map[string]Pending{}, downloads: map[string]int{}}
	s.load()
	return s
}

var safeID = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// reserved permissions belong to the system image; review refuses them.
var reserved = map[string]bool{"system": true, "install": true}

func (s *Server) save() {
	raw, _ := json.MarshalIndent(map[string]any{"catalog": s.catalog, "crashes": s.crashes, "developers": s.developers,
		"pending": s.pending, "revoked": s.revoked, "reports": s.reports, "downloads": s.downloads}, "", "  ")
	if err := WriteAtomic(filepath.Join(s.dir, "state.json"), raw); err != nil {
		log.Printf("state not saved: %v", err)
	}
}

// writeAtomic replaces the file whole: a crash or a full disk leaves the
// old one, never half of the new.
func WriteAtomic(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *Server) load() {
	raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		return
	}
	var st struct {
		Catalog    map[string]Entry      `json:"catalog"`
		Crashes    []Crash               `json:"crashes"`
		Developers map[string]*Developer `json:"developers"`
		Pending    map[string]Pending    `json:"pending"`
		Revoked    []pkg.Revoked         `json:"revoked"`
		Reports    []Report              `json:"reports"`
		Downloads  map[string]int        `json:"downloads"`
	}
	if json.Unmarshal(raw, &st) == nil {
		s.reports = st.Reports
		if st.Downloads != nil {
			s.downloads = st.Downloads
		}
		if st.Catalog != nil {
			s.catalog = st.Catalog
		}
		if st.Developers != nil {
			s.developers = st.Developers
		}
		if st.Pending != nil {
			s.pending = st.Pending
		}
		s.crashes, s.revoked = st.Crashes, st.Revoked
	}
}

func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func Fail(w http.ResponseWriter, code int, err error) {
	WriteJSON(w, code, map[string]string{"error": err.Error()})
}

// register records a developer: a name, a key and an identity number (a
// company's unified social credit code or a person's resident ID), whose
// check character must be right. The key is certified once the staff have
// verified the identity (in production against the business registry or
// the ID card service); asking again returns the status, and the
// certificate when there is one.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Pub    string `json:"pub"`
		Kind   string `json:"kind"`
		Number string `json:"number"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		Fail(w, 400, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	pub, err := b64.DecodeString(req.Pub)
	if err != nil || len(pub) != ed25519.PublicKeySize || name == "" || len(name) > 64 {
		Fail(w, 400, errors.New("need a name and an Ed25519 public key"))
		return
	}
	switch {
	case req.Kind == "company" && pkg.ValidCreditCode(req.Number):
	case req.Kind == "person" && pkg.ValidResidentID(req.Number):
	default:
		Fail(w, 400, errors.New("need kind company with a unified social credit code, or person with a resident ID number"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.developers[name]
	switch {
	case d == nil:
		d = &Developer{Name: name, Pub: req.Pub, Kind: req.Kind, Number: mask(req.Number), Status: "pending"}
		if req.Kind == "company" {
			d.Credit = strings.ToUpper(req.Number)
		}
		s.developers[name] = d
		s.save()
		log.Printf("developer %s registered (%s %s), waiting for verification", name, d.Kind, d.Number)
	case d.Pub != req.Pub:
		Fail(w, 409, errors.New("this developer name is registered with another key"))
		return
	}
	WriteJSON(w, 200, d)
}

func mask(n string) string { return strings.Repeat("*", max(0, len(n)-4)) + n[max(0, len(n)-4):] }

// staff checks the admin token.
func (s *Server) staff(w http.ResponseWriter, r *http.Request) bool {
	if s.Who(r) == "" {
		Fail(w, 401, errors.New("staff only"))
		return false
	}
	return true
}

// verifyDeveloper is the staff's decision on a developer's identity.
func (s *Server) verifyDeveloper(w http.ResponseWriter, r *http.Request) {
	if !s.staff(w, r) {
		return
	}
	var req struct {
		Reject string `json:"reject"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.developers[r.PathValue("name")]
	if d == nil {
		Fail(w, 404, errors.New("no such developer"))
		return
	}
	if req.Reject != "" {
		d.Status, d.Reason, d.Cert = "rejected", req.Reject, nil
	} else {
		pub, _ := b64.DecodeString(d.Pub)
		cert := s.store.Certify(d.Name, pub)
		d.Status, d.Reason, d.Cert = "verified", "", &cert
	}
	s.save()
	log.Printf("developer %s %s", d.Name, d.Status)
	s.audit(r, "developer", d.Name+" "+d.Status+" "+d.Reason)
	WriteJSON(w, 200, d)
}

// review checks a submitted package: signed by a verified developer this
// store certified, a sane manifest, no reserved permissions, and an update
// keeps the developer and raises the version. A new app, or an update
// asking for permissions the published version did not have, waits for a
// human review; other updates are published at once.
func (s *Server) review(raw []byte, stages int) (Entry, string, error) {
	e, why, err := s.check(raw)
	if err != nil {
		return Entry{}, "", err
	}
	dst := filepath.Join(s.dir, "packages", e.ID, e.Version+".phoneapp")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return Entry{}, "", err
	}
	if err := WriteAtomic(dst, raw); err != nil {
		return Entry{}, "", err
	}
	if why != "" {
		p := Pending{Entry: e, Why: why}
		if stages < 100 {
			p.Stages = &stages
		}
		s.pending[e.ID+"@"+e.Version] = p
		s.save()
		return e, "pending_review", nil
	}
	e = s.publish(e, stages)
	s.save()
	return e, "published", nil
}

// publish puts a version in the catalogue: to everyone, or staged to
// stages percent of devices with the last release everyone has kept for
// the others.
func (s *Server) publish(e Entry, stages int) Entry {
	if old, ok := s.catalog[e.ID]; ok && stages < 100 {
		prev := old.Previous
		if prev == nil {
			r := old.release()
			prev = &r
		}
		e.Previous, e.Rollout = prev, max(stages, 0)
	}
	s.catalog[e.ID] = e
	return e
}

func (s *Server) check(raw []byte) (Entry, string, error) {
	p, err := pkg.Open(raw, map[string]ed25519.PublicKey{s.store.Name: s.store.Public()})
	if err != nil {
		return Entry{}, "", err
	}
	if d := s.developers[p.Developer.Name]; d == nil || d.Status != "verified" || d.Pub != p.Developer.Pub {
		return Entry{}, "", errors.New("the developer is not verified")
	}
	var m Manifest
	if err := json.Unmarshal(p.Manifest, &m); err != nil {
		return Entry{}, "", fmt.Errorf("manifest: %w", err)
	}
	switch {
	case !safeID.MatchString(m.ID) || strings.HasPrefix(m.ID, "com.phone."):
		return Entry{}, "", fmt.Errorf("app id %q is not allowed", m.ID)
	case strings.TrimSpace(m.Name) == "":
		return Entry{}, "", errors.New("the app needs a name")
	case len(m.Granted) > 0:
		return Entry{}, "", errors.New("store apps cannot pre-grant permissions")
	case len(m.Filing) > 64:
		return Entry{}, "", errors.New("the filing number is too long")
	}
	if s.Accept != nil {
		if err := s.Accept(m); err != nil {
			return Entry{}, "", err
		}
	}
	for _, perm := range m.Permissions {
		if reserved[perm] {
			return Entry{}, "", fmt.Errorf("permission %q is for the system image", perm)
		}
		if _, runtime := ui.RuntimePerms[perm]; runtime && strings.TrimSpace(m.Purposes[perm]) == "" {
			return Entry{}, "", fmt.Errorf("permission %q needs a purpose: why the app asks for it", perm)
		}
	}
	for perm, why := range m.Purposes {
		if !slices.Contains(m.Permissions, perm) {
			return Entry{}, "", fmt.Errorf("a purpose for %q, which the app does not declare", perm)
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(why)); n == 0 || n > ui.MaxPurpose {
			return Entry{}, "", fmt.Errorf("the purpose for %q is 1–%d characters", perm, ui.MaxPurpose)
		}
	}
	if err := i18n.CheckAppText(m.Lang, m.Locales, m.textRules()); err != nil {
		return Entry{}, "", err
	}
	if err := ui.CheckNFCCards(m.NFC, m.Permissions, offeredLang); err != nil {
		return Entry{}, "", err
	}
	if err := ui.CheckPrivacy(m.Privacy, m.Permissions); err != nil {
		return Entry{}, "", err
	}
	if err := jsrt.Check(m.Runtime, m.Main, m.Exec, p.Files); err != nil {
		return Entry{}, "", err
	}
	for _, rv := range s.revoked {
		if rv.App == m.ID {
			return Entry{}, "", fmt.Errorf("%s was taken down: %s", m.ID, rv.Reason)
		}
	}
	why := "a new app"
	if old, ok := s.catalog[m.ID]; ok {
		if old.Developer != p.Developer.Name {
			return Entry{}, "", fmt.Errorf("%s belongs to another developer", m.ID)
		}
		if !newer(p.Version, old.Version) {
			return Entry{}, "", fmt.Errorf("version %s is not newer than %s", p.Version, old.Version)
		}
		why = ""
		for _, perm := range m.Permissions {
			if !slices.Contains(old.Permissions, perm) {
				why = "asks for the new permission " + perm
			}
		}
		if m.Privacy != nil {
			for _, c := range m.Privacy.Collects {
				if why == "" && !old.Privacy.Sends(c.Data) {
					why = "collects new data: " + c.Data
				}
			}
		}
	}
	sum := sha256.Sum256(raw)
	e := Entry{ID: m.ID, Name: m.Name, Version: p.Version, Developer: p.Developer.Name, Summary: m.Description,
		Permissions: m.Permissions, Purposes: m.Purposes, Privacy: m.Privacy, Size: len(raw), SHA256: hex.EncodeToString(sum[:]), Published: time.Now().UTC().Format(time.RFC3339),
		DeveloperCredit: s.developers[p.Developer.Name].Credit, Filing: m.Filing, Lang: m.Lang}
	for l, t := range m.Locales {
		if e.Locales == nil {
			e.Locales = map[string]EntryText{}
		}
		e.Locales[l] = EntryText{Name: t.Name, Summary: t.Description, Purposes: t.Purposes}
	}
	return e, why, nil
}

// haltOnCrashes stops a staged version that crashes on too many of the
// devices that took it.
func (s *Server) haltOnCrashes(id string) {
	e, ok := s.catalog[id]
	if !ok || e.Previous == nil || e.Rollout == 0 {
		return
	}
	n := 0
	for _, c := range s.crashes {
		if c.App == id && c.Version == e.Version {
			n++
		}
	}
	got := s.downloads[id+"@"+e.Version]
	if n < haltCrashMin || n*100 < got*haltCrashShare {
		return
	}
	e.Rollout, e.Halted = 0, fmt.Sprintf("%d crash reports from %d downloads", n, got)
	s.catalog[id] = e
	log.Printf("rollout of %s %s halted: %s", id, e.Version, e.Halted)
}

// rollout is a developer moving their staged version: a new percentage, 0
// to hold it back, 100 to give it to everyone (it is staged no more).
func (s *Server) rollout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		App     string `json:"app"`
		Version string `json:"version"`
		Percent int    `json:"percent"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Percent < 0 || req.Percent > 100 {
		Fail(w, 400, errors.New("need app, version and a percentage"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.devAuth(r, func(name string, t int64) string { return pkg.RolloutMsg(name, t, req.App, req.Version, req.Percent) })
	if err != nil {
		Fail(w, 403, err)
		return
	}
	e, ok := s.catalog[req.App]
	switch {
	case !ok || e.Developer != d.Name:
		Fail(w, 404, errors.New("not an app of yours"))
		return
	case e.Version != req.Version || e.Previous == nil:
		Fail(w, 409, fmt.Errorf("%s %s is not being rolled out", req.App, req.Version))
		return
	}
	e.Rollout, e.Halted = req.Percent, ""
	if req.Percent == 100 {
		e.Rollout, e.Previous = 0, nil
	}
	s.catalog[req.App] = e
	s.save()
	log.Printf("%s: rollout of %s %s at %d%%", d.Name, req.App, req.Version, req.Percent)
	WriteJSON(w, 200, e)
}

// decide is the staff's review of a pending submission.
func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	if !s.staff(w, r) {
		return
	}
	var req struct {
		Reject string `json:"reject"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	key := r.PathValue("id") + "@" + r.PathValue("version")
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[key]
	if !ok {
		Fail(w, 404, errors.New("no such submission"))
		return
	}
	delete(s.pending, key)
	if req.Reject != "" {
		_ = os.Remove(filepath.Join(s.dir, "packages", p.ID, p.Version+".phoneapp"))
		s.save()
		log.Printf("review: %s rejected: %s", key, req.Reject)
		s.audit(r, "review", key+" rejected: "+req.Reject)
		WriteJSON(w, 200, map[string]string{"status": "rejected"})
		return
	}
	if old, ok := s.catalog[p.ID]; ok && !newer(p.Version, old.Version) {
		s.save()
		Fail(w, 409, fmt.Errorf("a newer version %s is out already", old.Version))
		return
	}
	p.Published = time.Now().UTC().Format(time.RFC3339)
	stages := 100
	if p.Stages != nil {
		stages = *p.Stages
	}
	s.publish(p.Entry, stages)
	s.save()
	log.Printf("review: published %s", key)
	s.audit(r, "review", key+" published")
	WriteJSON(w, 200, map[string]string{"status": "published"})
}

// takedown removes an app from the catalogue and puts it on the signed
// revocation list devices fetch: installed copies stop starting.
func (s *Server) takedown(w http.ResponseWriter, r *http.Request) {
	if !s.staff(w, r) {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		Fail(w, 400, errors.New("a takedown needs a reason"))
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.catalog[id]; !ok {
		Fail(w, 404, errors.New("not in the catalogue"))
		return
	}
	delete(s.catalog, id)
	s.revoked = append(s.revoked, pkg.Revoked{App: id, Reason: strings.TrimSpace(req.Reason), At: time.Now().UnixMilli()})
	s.save()
	log.Printf("took down %s: %s", id, req.Reason)
	s.audit(r, "takedown", id+": "+req.Reason)
	WriteJSON(w, 200, map[string]string{"status": "revoked"})
}

// crashAuth: only the app's developer reads its crash reports, signing
// "phone-crashes/1|<app>|<unix time>" with the key the store certified.
func (s *Server) crashAuth(r *http.Request, app string) error {
	d, err := s.devAuth(r, func(_ string, t int64) string { return CrashReadMsg(app, t) })
	if err != nil {
		return err
	}
	if e, ok := s.catalog[app]; !ok || d.Status != "verified" || e.Developer != d.Name {
		return errors.New("only the app's developer reads its crash reports")
	}
	return nil
}

// devAuth checks a request a developer signed (X-Developer, X-Time,
// X-Signature over msg(name, time)) with their registered key. The name may
// be percent-encoded: browsers send header values in Latin-1 only.
func (s *Server) devAuth(r *http.Request, msg func(string, int64) string) (*Developer, error) {
	name, ts, sig := r.Header.Get("X-Developer"), r.Header.Get("X-Time"), r.Header.Get("X-Signature")
	if n, err := url.PathUnescape(name); err == nil {
		name = n
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(t, 0)).Abs() > 5*time.Minute {
		return nil, errors.New("sign the request with the current time")
	}
	d := s.developers[name]
	if d == nil {
		return nil, errors.New("no such developer")
	}
	pub, _ := b64.DecodeString(d.Pub)
	raw, err := b64.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, []byte(msg(name, t)), raw) {
		return nil, errors.New("bad signature")
	}
	return d, nil
}

// DevReadMsg is what a developer signs to read their own state.
func DevReadMsg(name string, t int64) string { return fmt.Sprintf("phone-dev/1|%s|%d", name, t) }

// CrashReadMsg is what a developer signs to read an app's crash reports.
func CrashReadMsg(app string, t int64) string { return fmt.Sprintf("phone-crashes/1|%s|%d", app, t) }

// newer compares dotted numeric versions.
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// Routes is the store's API; a store built on it adds its own routes.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/developers", s.register)
	mux.HandleFunc("POST /v1/admin/developers/{name}", s.verifyDeveloper)
	mux.HandleFunc("POST /v1/admin/reviews/{id}/{version}", s.decide)
	mux.HandleFunc("POST /v1/admin/takedowns/{id}", s.takedown)
	mux.HandleFunc("POST /v1/reports", s.report)
	mux.HandleFunc("POST /v1/developer/rollout", s.rollout)
	mux.HandleFunc("GET /v1/admin/queue", func(w http.ResponseWriter, r *http.Request) {
		if !s.staff(w, r) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		var devs []*Developer
		for _, d := range s.developers {
			if d.Status == "pending" {
				devs = append(devs, d)
			}
		}
		out := map[string]any{"developers": devs, "reviews": s.pending, "reports": s.openReports()}
		if s.Queue != nil {
			for k, v := range s.Queue() {
				out[k] = v
			}
		}
		WriteJSON(w, 200, out)
	})
	mux.HandleFunc("GET /v1/revocations", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		list := append([]pkg.Revoked{}, s.revoked...)
		s.mu.Unlock()
		issued := int64(0)
		for _, rv := range list {
			issued = max(issued, rv.At)
		}
		WriteJSON(w, 200, s.store.SignRevocations(pkg.Revocations{Issued: issued, Revoked: list}))
	})
	mux.HandleFunc("POST /v1/submissions", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, pkg.MaxSize+1))
		if err != nil {
			Fail(w, 400, err)
			return
		}
		stages := 100
		if v := r.URL.Query().Get("rollout"); v != "" {
			if stages, err = strconv.Atoi(v); err != nil || stages < 0 || stages > 100 {
				Fail(w, 400, errors.New("rollout is a percentage, 0 to 100"))
				return
			}
		}
		s.mu.Lock()
		e, status, err := s.review(raw, stages)
		s.mu.Unlock()
		if err != nil {
			log.Printf("submission refused: %v", err)
			Fail(w, 422, err)
			return
		}
		log.Printf("%s %s %s by %s", status, e.ID, e.Version, e.Developer)
		WriteJSON(w, 200, map[string]any{"status": status, "entry": e})
	})
	mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		out := make([]Entry, 0, len(s.catalog))
		for _, e := range s.catalog {
			out = append(out, e)
		}
		s.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		cat, err := s.store.SignCatalog(out, time.Now())
		if err != nil {
			Fail(w, 500, err)
			return
		}
		WriteJSON(w, 200, cat)
	})
	mux.HandleFunc("GET /v1/packages/{id}/{version}", func(w http.ResponseWriter, r *http.Request) {
		id, version := r.PathValue("id"), r.PathValue("version")
		s.mu.Lock()
		e, ok := s.catalog[id]
		offered := ok && (e.Version == version || e.Previous != nil && e.Previous.Version == version)
		if offered && e.Previous != nil && e.Version == version {
			s.downloads[id+"@"+version]++
		}
		s.mu.Unlock()
		if !offered || !safeID.MatchString(id) {
			Fail(w, 404, errors.New("no such package"))
			return
		}
		full := filepath.Join(s.dir, "packages", id, version+".phoneapp")
		// An update from a version the store still has goes as a delta.
		if from := r.URL.Query().Get("from"); from != "" && from != version && !strings.ContainsAny(from, "/\\") {
			oldRaw, err1 := os.ReadFile(filepath.Join(s.dir, "packages", id, from+".phoneapp"))
			newRaw, err2 := os.ReadFile(full)
			if err1 == nil && err2 == nil {
				if delta, err := pkg.MakeDelta(oldRaw, newRaw); err == nil && len(delta) < len(newRaw) {
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Header().Set("X-Phoneapp-Delta", from)
					_, _ = w.Write(delta)
					return
				}
			}
		}
		http.ServeFile(w, r, full)
	})
	mux.HandleFunc("GET /v1/developer", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d, err := s.devAuth(r, DevReadMsg)
		if err != nil {
			Fail(w, 403, err)
			return
		}
		published, pending := []Entry{}, []Pending{}
		for _, e := range s.catalog {
			if e.Developer == d.Name {
				published = append(published, e)
			}
		}
		for _, p := range s.pending {
			if p.Developer == d.Name {
				pending = append(pending, p)
			}
		}
		sort.Slice(published, func(i, j int) bool { return published[i].Name < published[j].Name })
		sort.Slice(pending, func(i, j int) bool { return pending[i].ID+pending[i].Version < pending[j].ID+pending[j].Version })
		WriteJSON(w, 200, map[string]any{"developer": d, "published": published, "pending": pending})
	})
	mux.HandleFunc("POST /v1/crashes", func(w http.ResponseWriter, r *http.Request) {
		var c Crash
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&c); err != nil || c.App == "" {
			Fail(w, 400, errors.New("need app and reason"))
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.catalog[c.App]; !ok {
			Fail(w, 404, errors.New("not a store app"))
			return
		}
		if len(c.Reason) > 1024 {
			c.Reason = c.Reason[:1024]
		}
		if len(c.Log) > 40 {
			c.Log = c.Log[len(c.Log)-40:]
		}
		s.crashes = append(s.crashes, c)
		if len(s.crashes) > 1000 {
			s.crashes = s.crashes[len(s.crashes)-1000:]
		}
		s.haltOnCrashes(c.App)
		s.save()
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /v1/crashes", func(w http.ResponseWriter, r *http.Request) {
		app := r.URL.Query().Get("app")
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.crashAuth(r, app); err != nil {
			Fail(w, 403, err)
			return
		}
		out := []Crash{}
		for _, c := range s.crashes {
			if c.App == app {
				out = append(out, c)
			}
		}
		WriteJSON(w, 200, out)
	})
	return mux
}
