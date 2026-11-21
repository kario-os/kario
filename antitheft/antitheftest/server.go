// Package antitheftest is a Kario account server's kario-antitheft/1 in
// memory (package antitheft's tests, the Shell's tests and selftest): the
// phone's API as kario-accountsrv serves it, and the account's owner as
// methods where the real server has its page and the owner's mailbox
// (Remove, LastCode). It signs releases with a key of its own (Pub is what
// the phone trusts). With a file (Open) it keeps its state there after each
// change, so a selftest's simulated reboot, a new Shell process, finds the
// phones bound before it.
package antitheftest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/antitheft"
	"github.com/kario-os/kario/keystore"
)

// Account is an account as the server knows it: its ID, the login (an
// email address) and one app password.
type Account struct {
	ID       string `json:"id"`
	Login    string `json:"login"`
	Password string `json:"password"`
}

// Binding is a phone bound to an account: its ID, the account's, and when.
type Binding struct {
	Device  string `json:"device"`
	Account string `json:"account"`
	At      int64  `json:"at"`
}

type pending struct {
	Code     string `json:"code"`
	Expires  int64  `json:"expires"`
	Attempts int    `json:"attempts"`
}

// Mail is a code the server sent.
type Mail struct {
	To   string `json:"to"`
	Code string `json:"code"`
}

type state struct {
	Seed     []byte              `json:"seed"`
	Accounts []*Account          `json:"accounts"`
	Bindings map[string]*Binding `json:"bindings"` // by the device's public key
	Removed  map[string]string   `json:"removed"`  // the device's key -> the account its owner removed it from
	Codes    map[string]*pending `json:"codes"`    // by account ID and device key
	Mails    []Mail              `json:"mails"`
}

type Server struct {
	mu   sync.Mutex
	Key  ed25519.PrivateKey
	Pub  ed25519.PublicKey
	Now  func() time.Time
	path string
	st   state
	// Roots: when set, attestations must chain to them.
	Roots      *keystore.Roots
	challenges map[string]time.Time
	sent       map[string][]time.Time
	// Requests counts the phone's requests by path.
	Requests map[string]int
}

func New() *Server {
	s := &Server{Now: time.Now, challenges: map[string]time.Time{}, sent: map[string][]time.Time{}, Requests: map[string]int{}}
	s.st.Seed = make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(s.st.Seed)
	s.init()
	return s
}

func (s *Server) init() {
	s.Key = ed25519.NewKeyFromSeed(s.st.Seed)
	s.Pub = s.Key.Public().(ed25519.PublicKey)
	if s.st.Bindings == nil {
		s.st.Bindings = map[string]*Binding{}
	}
	if s.st.Removed == nil {
		s.st.Removed = map[string]string{}
	}
	if s.st.Codes == nil {
		s.st.Codes = map[string]*pending{}
	}
}

// Open is the server whose state is in path, a new one there if there is
// none yet.
func Open(path string) (*Server, error) {
	s := New()
	s.path = path
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s, s.save()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.st); err != nil || len(s.st.Seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("antitheftest: %s does not parse: %v", path, err)
	}
	s.init()
	return s, nil
}

// save keeps the state in the file; the caller holds s.mu.
func (s *Server) save() error {
	if s.path == "" {
		return nil
	}
	raw, _ := json.Marshal(s.st)
	return os.WriteFile(s.path, raw, 0o600)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddAccount makes an account with the login and app password, or finds
// the one there is; its ID.
func (s *Server) AddAccount(login, password string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	login = strings.ToLower(login)
	for _, a := range s.st.Accounts {
		if a.Login == login {
			a.Password = password
			_ = s.save()
			return a.ID
		}
	}
	a := &Account{ID: randomHex(8), Login: login, Password: password}
	s.st.Accounts = append(s.st.Accounts, a)
	_ = s.save()
	return a.ID
}

func (s *Server) byID(id string) *Account {
	for _, a := range s.st.Accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// Bound is the account the phone is bound to, if any.
func (s *Server) Bound(device string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.st.Bindings {
		if b.Device == device {
			return b.Account, true
		}
	}
	return "", false
}

// Devices are the phones bound, by ID.
func (s *Server) Devices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, b := range s.st.Bindings {
		out = append(out, b.Device)
	}
	return out
}

// Remove is the owner removing the phone on the account's page: the
// binding goes, and the phone asking next gets a release without a code.
func (s *Server) Remove(device string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, b := range s.st.Bindings {
		if b.Device == device {
			delete(s.st.Bindings, key)
			s.st.Removed[key] = b.Account
			return s.save()
		}
	}
	return fmt.Errorf("antitheftest: %s is bound to no account", device)
}

// LastCode is the last code sent to the login.
func (s *Server) LastCode(login string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.st.Mails) - 1; i >= 0; i-- {
		if s.st.Mails[i].To == strings.ToLower(login) {
			return s.st.Mails[i].Code
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/antitheft/") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests[r.URL.Path]++
	now := s.Now()
	if r.URL.Path == "/v1/antitheft/challenge" {
		c := randomHex(16)
		s.challenges[c] = now.Add(antitheft.ChallengeTTL)
		writeJSON(w, 200, antitheft.Challenge{Challenge: c, ExpiresIn: int(antitheft.ChallengeTTL / time.Second)})
		return
	}
	var req antitheft.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil || req.Attestation == nil {
		fail(w, 400, "a request with an attestation")
		return
	}
	until, ok := s.challenges[req.Challenge]
	delete(s.challenges, req.Challenge)
	if !ok || !now.Before(until) {
		fail(w, 403, "unknown or expired challenge")
		return
	}
	key := req.Attestation.DevicePub
	var acct *Account
	switch r.URL.Path {
	case "/v1/antitheft/bind", "/v1/antitheft/unbind":
		user, pass, _ := r.BasicAuth()
		for _, a := range s.st.Accounts {
			if a.Login == strings.ToLower(user) && subtle.ConstantTimeCompare([]byte(a.Password), []byte(pass)) == 1 {
				acct = a
			}
		}
		if acct == nil {
			fail(w, 401, "sign in with the login and an app password")
			return
		}
		purpose := strings.TrimPrefix(r.URL.Path, "/v1/antitheft/")
		if err := antitheft.CheckDevice(s.Roots, req.Attestation, antitheft.Nonce(purpose, acct.Login, req.Challenge, "")); err != nil {
			fail(w, 403, err.Error())
			return
		}
	default:
		if acct = s.byID(req.Account); acct == nil {
			fail(w, 404, "no such account")
			return
		}
		purpose := strings.TrimPrefix(r.URL.Path, "/v1/antitheft/")
		if req.Nonce == "" || antitheft.CheckDevice(s.Roots, req.Attestation, antitheft.Nonce(purpose, acct.ID, req.Challenge, req.Nonce)) != nil {
			fail(w, 403, "the attestation does not check out")
			return
		}
	}
	device := req.Attestation.DeviceID
	release := func() {
		signed, err := antitheft.Sign(s.Key, antitheft.Release{Device: device, Account: acct.ID, Nonce: req.Nonce,
			Issued: now.Unix(), Expires: now.Add(antitheft.ReleaseTTL).Unix()})
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		delete(s.st.Removed, key)
		delete(s.st.Codes, acct.ID+"\x00"+key)
		_ = s.save()
		writeJSON(w, 200, signed)
	}
	switch r.URL.Path {
	case "/v1/antitheft/bind":
		if b := s.st.Bindings[key]; b != nil && b.Account != acct.ID {
			fail(w, 409, "the phone is bound to another account")
			return
		}
		s.st.Bindings[key] = &Binding{Device: device, Account: acct.ID, At: now.Unix()}
		delete(s.st.Removed, key)
		_ = s.save()
		writeJSON(w, 200, antitheft.Bound{Account: acct.ID, Email: antitheft.MaskEmail(acct.Login)})
	case "/v1/antitheft/unbind":
		if b := s.st.Bindings[key]; b != nil && b.Account == acct.ID {
			delete(s.st.Bindings, key)
			_ = s.save()
		}
		w.WriteHeader(http.StatusNoContent)
	case "/v1/antitheft/status":
		if b := s.st.Bindings[key]; b != nil && b.Account == acct.ID {
			writeJSON(w, 200, antitheft.Status{Bound: true, Email: antitheft.MaskEmail(acct.Login)})
			return
		}
		if s.st.Removed[key] == acct.ID {
			signed, err := antitheft.Sign(s.Key, antitheft.Release{Device: device, Account: acct.ID, Nonce: req.Nonce,
				Issued: now.Unix(), Expires: now.Add(antitheft.ReleaseTTL).Unix()})
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
			writeJSON(w, 200, antitheft.Status{Email: antitheft.MaskEmail(acct.Login), Release: &signed})
			return
		}
		writeJSON(w, 200, antitheft.Status{Email: antitheft.MaskEmail(acct.Login)})
	case "/v1/antitheft/code":
		recent := s.sent[key][:0]
		for _, t := range s.sent[key] {
			if now.Sub(t) < time.Hour {
				recent = append(recent, t)
			}
		}
		if len(recent) >= antitheft.CodesPerHour {
			w.Header().Set("Retry-After", "3600")
			fail(w, 429, "too many codes")
			return
		}
		s.sent[key] = append(recent, now)
		n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
		c := fmt.Sprintf("%06d", n.Int64())
		s.st.Codes[acct.ID+"\x00"+key] = &pending{Code: c, Expires: now.Add(antitheft.CodeTTL).Unix()}
		s.st.Mails = append(s.st.Mails, Mail{To: acct.Login, Code: c})
		_ = s.save()
		writeJSON(w, 202, antitheft.CodeSent{Email: antitheft.MaskEmail(acct.Login)})
	case "/v1/antitheft/release":
		p := s.st.Codes[acct.ID+"\x00"+key]
		if p == nil || now.Unix() >= p.Expires || subtle.ConstantTimeCompare([]byte(p.Code), []byte(strings.TrimSpace(req.Code))) != 1 {
			if p != nil {
				if p.Attempts++; p.Attempts >= antitheft.CodeAttempts {
					delete(s.st.Codes, acct.ID+"\x00"+key)
				}
				_ = s.save()
			}
			fail(w, 401, "wrong or expired code")
			return
		}
		if b := s.st.Bindings[key]; b != nil && b.Account == acct.ID {
			delete(s.st.Bindings, key)
		}
		release()
	default:
		http.NotFound(w, r)
	}
}
