// Package familytest is a Kario account server's kario-family/1 in memory
// (package family's tests, the Shell's tests and selftest): the phone's
// API as the real server serves it, and the parent's side as methods
// (Claim, SetRules, Locate, AskReport, Asks, Answer, ConfirmReset, Unbind)
// where the real server has its web page. It signs with a key of its own
// (Pub is what the phone trusts) and tells the phone through Push, when
// set. The real server's fresh sign-in before a reset is the page's: here
// ConfirmReset is the parent who did it.
package familytest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/family"
)

type device struct {
	id, tokenSum string
	code         string
	codeUntil    time.Time
	state        string
	parent       string
	consent      int
	pushToken    string
	version      int
	signed       *family.Signed
	report       *family.Report
	location     *family.Location
	locateID     string
	locateAt     time.Time
	askReport    bool
	asks         []*Request
	reset        *reset
	resetsAsked  []time.Time
}

// Request is a request for more time as the server holds it: when it came,
// its state, the minutes the parent gave and the grant.
type Request struct {
	family.Ask
	At      time.Time
	State   string
	Granted int
	Signed  *family.SignedGrant
}

// reset is the phone's PIN reset: asked, confirmed (signed, good until
// expires) or done.
type reset struct {
	id, state string
	asked     time.Time
	expires   time.Time
	signed    *family.SignedReset
}

// state is the request's at now: pending past its time is expired.
func (q *Request) state(now time.Time) string {
	if q.State == family.AskPending && (!now.Before(q.At.Add(family.AskTTL)) || now.Unix() >= q.Until) {
		return family.AskExpired
	}
	return q.State
}

// sweep drops what the real server keeps no longer: requests past the end
// of the phone's day, a reset not confirmed in time or not used in time.
func (d *device) sweep(now time.Time) {
	d.asks = slices.DeleteFunc(d.asks, func(q *Request) bool { return now.Unix() >= q.Until })
	if r := d.reset; r != nil && (r.state == "asked" && !now.Before(r.asked.Add(family.ResetAskTTL)) || r.state == "confirmed" && !now.Before(r.expires)) {
		d.reset = nil
	}
}

// Server is the server. Push, when set, carries a push to the phone's
// token (the selftest posts it to the push gateway); a failed push is
// counted (Pushes) and otherwise ignored, as the real server does.
type Server struct {
	mu           sync.Mutex
	Key          ed25519.PrivateKey
	Pub          ed25519.PublicKey
	Now          func() time.Time
	Push         func(token string, d family.PushData) error
	pushed       int
	pushFailures int
	// Requests counts the phone's requests by "METHOD path".
	Requests map[string]int
	devices  map[string]*device
}

func New() *Server {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	return &Server{Key: key, Pub: pub, Now: time.Now, Requests: map[string]int{}, devices: map[string]*device{}}
}

func sum(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) push(d *device, p family.PushData) {
	token, f := d.pushToken, s.Push
	if token == "" || f == nil {
		return
	}
	s.mu.Unlock()
	err := f(token, p)
	s.mu.Lock()
	s.pushed++
	if err != nil {
		s.pushFailures++
	}
}

// Pushes counts the pushes sent, and those that failed.
func (s *Server) Pushes() (sent, failed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushed, s.pushFailures
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// byToken is the device a request's bearer token names.
func (s *Server) byToken(r *http.Request) *device {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil
	}
	want := sum(strings.TrimSpace(token))
	for _, d := range s.devices {
		if d.tokenSum == want {
			if d.state != family.StateBound && s.Now().After(d.codeUntil) {
				delete(s.devices, d.id)
				return nil
			}
			return d
		}
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Date", s.Now().UTC().Format(http.TimeFormat))
	s.Requests[r.Method+" "+r.URL.Path]++
	read := func(v any) bool {
		if err := json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(v); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return false
		}
		return true
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/family/device/pair" {
		var p family.Pair
		if !read(&p) {
			return
		}
		token := family.TokenPrefix + randomHex(32)
		d := &device{id: "fd" + randomHex(8), tokenSum: sum(token), code: s.newCode(), codeUntil: s.Now().Add(family.CodeTTL),
			state: family.StatePairing, pushToken: p.PushToken}
		s.devices[d.id] = d
		writeJSON(w, http.StatusCreated, family.Paired{Device: d.id, Token: token, Code: d.code, ExpiresIn: int(family.CodeTTL / time.Second)})
		return
	}
	d := s.byToken(r)
	if d == nil || !strings.HasPrefix(r.URL.Path, "/v1/family/device") {
		fail(w, http.StatusUnauthorized, "no such binding")
		return
	}
	now := s.Now()
	d.sweep(now)
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/family/device":
		st := family.State{State: d.state, Parent: d.parent, Version: d.version, Report: d.askReport}
		if d.locateID != "" && s.Now().Sub(d.locateAt) < family.LocateTTL {
			st.Locate = d.locateID
		}
		for _, q := range d.asks {
			if q.State == family.AskGranted || q.State == family.AskRefused {
				st.Answered = append(st.Answered, q.ID)
			}
		}
		if d.reset != nil && d.reset.state == "confirmed" {
			st.Reset = d.reset.id
		}
		writeJSON(w, http.StatusOK, st)
	case "POST /v1/family/device/requests":
		var a family.Ask
		if !read(&a) {
			return
		}
		if err := a.Check(now); err != nil || d.state != family.StateBound {
			fail(w, http.StatusBadRequest, fmt.Sprint("ask: ", err))
			return
		}
		if slices.ContainsFunc(d.asks, func(q *Request) bool { return q.ID == a.ID }) {
			w.WriteHeader(http.StatusOK)
			return
		}
		var recent []time.Time
		for _, q := range d.asks {
			if now.Sub(q.At) < time.Hour {
				recent = append(recent, q.At)
			}
		}
		if len(recent) >= family.AsksPerHour {
			w.Header().Set("Retry-After", strconv.Itoa(int(recent[len(recent)-family.AsksPerHour].Add(time.Hour).Sub(now).Seconds())+1))
			fail(w, http.StatusTooManyRequests, "asked too often")
			return
		}
		d.asks = append(d.asks, &Request{Ask: a, At: now, State: family.AskPending})
		w.WriteHeader(http.StatusCreated)
	case "GET /v1/family/device/requests":
		out := []family.AskState{}
		for _, q := range d.asks {
			out = append(out, family.AskState{ID: q.ID, State: q.state(now), Minutes: q.Granted, Grant: q.Signed})
		}
		writeJSON(w, http.StatusOK, out)
	case "POST /v1/family/device/reset":
		var a family.ResetAsk
		if !read(&a) {
			return
		}
		if !family.ValidID(a.ID) || d.state != family.StateBound {
			fail(w, http.StatusBadRequest, "reset: an ID")
			return
		}
		d.resetsAsked = slices.DeleteFunc(d.resetsAsked, func(t time.Time) bool { return now.Sub(t) >= time.Hour })
		if len(d.resetsAsked) >= family.ResetsPerHour {
			w.Header().Set("Retry-After", strconv.Itoa(int(d.resetsAsked[0].Add(time.Hour).Sub(now).Seconds())+1))
			fail(w, http.StatusTooManyRequests, "asked too often")
			return
		}
		d.resetsAsked = append(d.resetsAsked, now)
		d.reset = &reset{id: a.ID, state: "asked", asked: now}
		w.WriteHeader(http.StatusCreated)
	case "GET /v1/family/device/reset":
		if d.reset == nil || d.reset.state != "confirmed" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, d.reset.signed)
	case "POST /v1/family/device/reset/done":
		var a family.ResetAsk
		if !read(&a) {
			return
		}
		if d.reset == nil || d.reset.state != "confirmed" || d.reset.id != a.ID {
			fail(w, http.StatusConflict, "no such reset confirmed")
			return
		}
		d.reset.state, d.reset.signed = "done", nil
		w.WriteHeader(http.StatusNoContent)
	case "DELETE /v1/family/device/reset":
		if d.reset != nil && d.reset.state != "done" {
			d.reset = nil
		}
		w.WriteHeader(http.StatusNoContent)
	case "POST /v1/family/device/confirm":
		var c family.Confirm
		if !read(&c) {
			return
		}
		if d.state != family.StateClaimed {
			fail(w, http.StatusConflict, "no parent to confirm")
			return
		}
		if !c.Allow {
			delete(s.devices, d.id)
		} else {
			d.state, d.consent, d.code = family.StateBound, c.Consent, ""
		}
		w.WriteHeader(http.StatusNoContent)
	case "GET /v1/family/device/policy":
		if d.state != family.StateBound || d.signed == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, d.signed)
	case "PUT /v1/family/device/report":
		var rep family.Report
		if !read(&rep) {
			return
		}
		if err := rep.Check(); err != nil || d.state != family.StateBound {
			fail(w, http.StatusBadRequest, fmt.Sprint("report: ", err))
			return
		}
		if rep.PushToken != "" {
			d.pushToken = rep.PushToken
		}
		rep.PushToken = ""
		d.report, d.askReport = &rep, false
		w.WriteHeader(http.StatusNoContent)
	case "POST /v1/family/device/location":
		var l family.Location
		if !read(&l) {
			return
		}
		if err := l.Check(); err != nil || l.Request != d.locateID {
			fail(w, http.StatusBadRequest, fmt.Sprint("location: ", err))
			return
		}
		d.location, d.locateID = &l, ""
		w.WriteHeader(http.StatusNoContent)
	case "DELETE /v1/family/device":
		delete(s.devices, d.id)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusNotFound, "no such request")
	}
}

func (s *Server) newCode() string {
	for {
		n, _ := rand.Int(rand.Reader, big.NewInt(100000000))
		code := fmt.Sprintf("%08d", n.Int64())
		taken := false
		for _, d := range s.devices {
			taken = taken || d.code == code
		}
		if !taken {
			return code
		}
	}
}

var errNoDevice = errors.New("familytest: no such phone")

func (s *Server) bound(id string) (*device, error) {
	d := s.devices[id]
	if d == nil || d.state != family.StateBound {
		return nil, errNoDevice
	}
	return d, nil
}

// Claim is a parent entering the code the phone shows; the phone then asks
// its user to allow parent.
func (s *Server) Claim(code, parent string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.code == code && d.state == family.StatePairing && s.Now().Before(d.codeUntil) {
			d.state, d.parent = family.StateClaimed, parent
			s.push(d, family.PushData{T: family.PushClaimed})
			return d.id, nil
		}
	}
	return "", errors.New("familytest: wrong or expired code")
}

// SetRules signs the parent's rules as the phone's next policy and tells
// the phone.
func (s *Server) SetRules(id string, r family.Rules) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.bound(id)
	if err != nil {
		return 0, err
	}
	now := s.Now()
	signed, err := family.Sign(s.Key, family.Policy{Device: d.id, Version: d.version + 1, Issued: now.Unix(),
		Expires: now.Add(family.PolicyTTL).Unix(), Parent: d.parent, Rules: r})
	if err != nil {
		return 0, err
	}
	d.version++
	d.signed = &signed
	s.push(d, family.PushData{T: family.PushPolicy, V: d.version})
	return d.version, nil
}

// Locate asks the phone where it is.
func (s *Server) Locate(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.bound(id)
	if err != nil {
		return "", err
	}
	d.locateID, d.locateAt = "l"+randomHex(6), s.Now()
	s.push(d, family.PushData{T: family.PushLocate, ID: d.locateID})
	return d.locateID, nil
}

// AskReport asks the phone for today's screen time again.
func (s *Server) AskReport(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.bound(id)
	if err != nil {
		return err
	}
	d.askReport = true
	s.push(d, family.PushData{T: family.PushReport})
	return nil
}

// Asks are the phone's requests for more time, oldest first, as the
// parent's page lists them.
func (s *Server) Asks(id string) ([]Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.bound(id)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	d.sweep(now)
	var out []Request
	for _, q := range d.asks {
		x := *q
		x.State = q.state(now)
		out = append(out, x)
	}
	return out, nil
}

// Answer is the parent answering a pending request: allow gives minutes
// (signed, for that request, app and day), or refuses; the phone hears.
func (s *Server) Answer(id, request string, allow bool, minutes int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.bound(id)
	if err != nil {
		return err
	}
	now := s.Now()
	d.sweep(now)
	i := slices.IndexFunc(d.asks, func(q *Request) bool { return q.ID == request })
	if i < 0 || d.asks[i].state(now) != family.AskPending {
		return errors.New("familytest: no such request pending")
	}
	q := d.asks[i]
	q.State = family.AskRefused
	if allow {
		g, err := family.SignGrant(s.Key, family.Grant{Device: d.id, Request: q.ID, App: q.App, Date: q.Date, Minutes: minutes,
			Issued: now.Unix(), Expires: min(now.Add(family.GrantTTL).Unix(), q.Until)})
		if err != nil {
			return err
		}
		q.State, q.Granted, q.Signed = family.AskGranted, minutes, &g
	}
	s.push(d, family.PushData{T: family.PushAnswer, ID: q.ID})
	return nil
}

// ConfirmReset is the parent, freshly signed in, confirming the phone's PIN
// reset: signed for ResetTTL; the phone hears.
func (s *Server) ConfirmReset(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.bound(id)
	if err != nil {
		return err
	}
	now := s.Now()
	d.sweep(now)
	if d.reset == nil || d.reset.state != "asked" {
		return errors.New("familytest: no reset asked for")
	}
	r := d.reset
	signed, err := family.SignReset(s.Key, family.Reset{Device: d.id, ID: r.id, Issued: now.Unix(), Expires: now.Add(family.ResetTTL).Unix()})
	if err != nil {
		return err
	}
	r.state, r.signed, r.expires = "confirmed", &signed, now.Add(family.ResetTTL)
	s.push(d, family.PushData{T: family.PushReset, ID: r.id})
	return nil
}

// Unbind is the parent ending the binding.
func (s *Server) Unbind(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.bound(id)
	if err != nil {
		return err
	}
	delete(s.devices, id)
	s.push(d, family.PushData{T: family.PushUnbound})
	return nil
}

// Status is a phone as the server holds it; Reset is its PIN reset's
// state ("" none, asked, confirmed, done).
type Status struct {
	State    string
	Parent   string
	Consent  int
	Version  int
	Report   *family.Report
	Location *family.Location
	Reset    string
}

// Device is the phone's status; false when the server has none.
func (s *Server) Device(id string) (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[id]
	if d == nil {
		return Status{}, false
	}
	d.sweep(s.Now())
	st := Status{State: d.state, Parent: d.parent, Consent: d.consent, Version: d.version, Report: d.report, Location: d.location}
	if d.reset != nil {
		st.Reset = d.reset.state
	}
	return st, true
}

// Devices are the IDs of the phones the server holds, bound or pairing.
func (s *Server) Devices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id := range s.devices {
		out = append(out, id)
	}
	return out
}
