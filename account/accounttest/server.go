// Package accounttest is the Kario account server's sign-in for phones in
// memory (package account's tests, the Shell's tests and selftest), as
// kario-accountsrv answers it: codes sent to an address (LastCode where the
// real server has the mailbox), the app passwords they get phones, and the
// account's page revoking one (Revoke). A code holds account.CodeTTL and
// account.CodeAttempts tries, once; a login gets one code every
// CodeInterval; an account has at most MaxPasswords.
package accounttest

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/account"
)

// MaxPasswords is kario-accountsrv's: an account's app passwords, those of
// phones and those the user made.
const MaxPasswords = 10

// Device is an app password the server made for a phone.
type Device struct {
	ID       string
	Login    string
	Name     string
	Password string
	Revoked  bool
}

type pending struct {
	code     string
	expires  time.Time
	attempts int
}

type Server struct {
	mu  sync.Mutex
	Now func() time.Time
	// CodeInterval: a login gets one code this often (the real server's
	// minute); 0 for no limit.
	CodeInterval time.Duration
	// Down answers every request 503.
	Down bool
	// Requests counts the requests by method and path.
	Requests map[string]int

	accounts map[string]bool
	codes    map[string]*pending
	mailbox  map[string]string
	sentAt   map[string]time.Time
	devices  []*Device
	others   map[string]int // app passwords the user made, by login
}

func New() *Server {
	return &Server{Now: time.Now, CodeInterval: time.Minute, Requests: map[string]int{}, accounts: map[string]bool{},
		codes: map[string]*pending{}, mailbox: map[string]string{}, sentAt: map[string]time.Time{}, others: map[string]int{}}
}

// AddAccount makes an account for the login, with n app passwords the user
// made on the page.
func (s *Server) AddAccount(login string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	login = strings.ToLower(login)
	s.accounts[login] = true
	s.others[login] = n
}

// HasAccount: the login has an account.
func (s *Server) HasAccount(login string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accounts[strings.ToLower(login)]
}

// LastCode is the last code sent to the login, "" for none.
func (s *Server) LastCode(login string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mailbox[strings.ToLower(login)]
}

// Devices are the passwords made for phones, revoked ones too, oldest
// first.
func (s *Server) Devices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, *d)
	}
	return out
}

// Check: the password is a phone's of the login, not revoked.
func (s *Server) Check(login, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.device(login, password) != nil
}

func (s *Server) device(login, password string) *Device {
	login = strings.ToLower(strings.TrimSpace(login))
	for _, d := range s.devices {
		if !d.Revoked && d.Login == login && subtle.ConstantTimeCompare([]byte(d.Password), []byte(password)) == 1 {
			return d
		}
	}
	return nil
}

// Revoke revokes a phone's password as the account's page does.
func (s *Server) Revoke(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.ID == id && !d.Revoked {
			d.Revoked = true
			return true
		}
	}
	return false
}

func (s *Server) passwords(login string) int {
	n := s.others[login]
	for _, d := range s.devices {
		if d.Login == login && !d.Revoked {
			n++
		}
	}
	return n
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newPassword() string {
	var b strings.Builder
	for i := 0; i < 16; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(26))
		b.WriteByte(byte('a' + n.Int64()))
	}
	return b.String()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, account.Failure{Error: msg})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Requests[r.Method+" "+r.URL.Path]++
	down := s.Down
	s.mu.Unlock()
	if down {
		fail(w, http.StatusServiceUnavailable, "down for maintenance")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/code":
		s.code(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/device":
		s.signIn(w, r)
	case r.Method == http.MethodDelete && r.URL.Path == "/v1/auth/device":
		s.signOut(w, r)
	default:
		http.NotFound(w, r)
	}
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(v)
}

func login(email string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(email))
	at := strings.IndexByte(l, '@')
	return l, at > 0 && at < len(l)-1 && !strings.ContainsAny(l, ": \"")
}

func (s *Server) code(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	l, ok := login(req.Email)
	if !ok {
		fail(w, 400, fmt.Sprintf("%q is not an email address", req.Email))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if last, ok := s.sentAt[l]; ok && s.CodeInterval > 0 && now.Sub(last) < s.CodeInterval {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int((s.CodeInterval-now.Sub(last)).Seconds()))))
		fail(w, 429, "too many codes asked for; try again later")
		return
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	c := fmt.Sprintf("%06d", n.Int64())
	s.codes[l] = &pending{code: c, expires: now.Add(account.CodeTTL)}
	s.mailbox[l], s.sentAt[l] = c, now
	writeJSON(w, 202, account.CodeSent{Sent: "email", ExpiresIn: int(account.CodeTTL.Seconds())})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var req account.SignIn
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	l, ok := login(req.Email)
	if !ok {
		fail(w, 400, fmt.Sprintf("%q is not an email address", req.Email))
		return
	}
	name := strings.TrimSpace(req.Device)
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, func(c rune) bool { return c < 0x20 }) {
		fail(w, 400, "name the device, at most 64 characters")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.codes[l]
	if p == nil || !s.Now().Before(p.expires) {
		delete(s.codes, l)
		left := 0
		writeJSON(w, 401, account.Failure{Error: "wrong or expired code", AttemptsLeft: &left})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Code)), []byte(p.code)) != 1 {
		p.attempts++
		left := account.CodeAttempts - p.attempts
		if left <= 0 {
			delete(s.codes, l)
			left = 0
		}
		writeJSON(w, 401, account.Failure{Error: "wrong or expired code", AttemptsLeft: &left})
		return
	}
	created := !s.accounts[l]
	if created && !req.Agree {
		writeJSON(w, 403, account.Failure{Error: "a new account needs the user's consent to the privacy policy; the code still holds", Consent: true})
		return
	}
	if s.passwords(l) >= MaxPasswords {
		fail(w, 409, fmt.Sprintf("an account has at most %d app passwords: revoke one first", MaxPasswords))
		return
	}
	delete(s.codes, l)
	s.accounts[l] = true
	d := &Device{ID: "p" + randomHex(6), Login: l, Name: name, Password: newPassword()}
	s.devices = append(s.devices, d)
	writeJSON(w, 201, account.Device{ID: d.ID, Name: d.Name, Created: s.Now().UTC().Format(time.RFC3339), Password: d.Password,
		Username: l, Server: "http://" + r.Host, NewAccount: created})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(user, pass)
	if !ok || d == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Kario"`)
		fail(w, 401, "sign in with the account's login and the phone's password")
		return
	}
	d.Revoked = true
	w.WriteHeader(204)
}
