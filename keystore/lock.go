package keystore

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kario-os/kario/hal/fingerprint"
)

// The lock-screen PIN, kept as Android's Gatekeeper keeps the device
// credential: only a salted, slow hash (PBKDF2 over an HMAC with a key
// derived from the device key, so the file is no use off the device; in
// production the TEE's hardware-bound key) is stored, and wrong attempts
// cost growing waits. The PIN itself is never stored or logged; a correct
// one yields an AuthToken. It is apart from the payment PIN that Confirm
// checks.

const (
	MinPIN = 4
	MaxPIN = 16
	// PINIterations is PBKDF2's cost: about 50 ms on a laptop, a few
	// hundred on a phone, for each attempt.
	PINIterations = 100_000
)

var (
	ErrNoPIN     = errors.New("no_pin")
	ErrPINFormat = errors.New("the PIN is 4 to 16 digits")
)

// lockFile is keystore/lock.json.
type lockFile struct {
	Salt string `json:"salt,omitempty"`
	Hash string `json:"hash,omitempty"`
	Iter int    `json:"iter,omitempty"`
	// User is the secure user ID: made when a PIN is first set, gone with
	// it; tokens carry it.
	User     uint64 `json:"user,omitempty"`
	Failures int    `json:"failures,omitempty"`
}

// lockState is the lock-screen and fingerprint state; its own mutex, taken
// before Store.mu when both are needed.
type lockState struct {
	mu       sync.Mutex
	file     lockFile
	fp       fingerFile
	clock    func() time.Time
	started  time.Time
	iter     int
	tokenKey []byte
	pepper   []byte
	sealKey  []byte
	// next: no PIN attempt before it. pinAt: when the PIN was last
	// entered, zero since the start (the first unlock after a restart
	// needs the PIN).
	next  time.Time
	pinAt time.Time
	// fpFails: failed matches in a row; fpLocked: too many, the PIN is
	// needed before the next.
	fpFails  int
	fpLocked bool
	sensor   fingerprint.Sensor
	// The sensor's one operation at a time: a new one cancels the last.
	op    context.CancelFunc
	opSeq int
	// The enrolment in progress, and the challenge its PIN token answers.
	enrolling   fingerprint.Enrollment
	enrolName   string
	enrolDone   int
	challenge   uint64
	challengeAt time.Time
	// bioOps: the challenge of each biometric signature a match allowed,
	// by app and key.
	bioOps map[string]bioOp
}

type bioOp struct {
	challenge uint64
	at        time.Time
}

func (l *lockState) now() time.Time { return l.clock() }

func (l *lockState) since() time.Duration { return l.now().Sub(l.started) }

func (s *Store) lockPath() string { return filepath.Join(s.dir, "keystore", "lock.json") }

func (s *Store) derive(label string) []byte {
	m := hmac.New(sha256.New, s.device.Seed())
	m.Write([]byte(label))
	return m.Sum(nil)
}

// openLock loads the PIN and the fingerprints. A count of wrong PINs past
// the free ones starts its wait again: a restart does not shorten it.
func (s *Store) openLock() error {
	l := &s.lk
	l.clock, l.iter, l.bioOps = time.Now, PINIterations, map[string]bioOp{}
	l.started = l.now()
	l.tokenKey = make([]byte, 32)
	if _, err := rand.Read(l.tokenKey); err != nil {
		return err
	}
	l.pepper, l.sealKey = s.derive("kario-lock-pin"), s.derive("kario-fingerprint-templates")
	if raw, err := os.ReadFile(s.lockPath()); err == nil {
		if err := json.Unmarshal(raw, &l.file); err != nil {
			return err
		}
	}
	l.next = l.now().Add(pinDelay(l.file.Failures))
	return s.loadFingers()
}

func (s *Store) saveLock() error {
	if s.lk.file == (lockFile{}) {
		if err := os.Remove(s.lockPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeJSON(s.lockPath(), s.lk.file)
}

// pinDelay is the wait after so many wrong PINs in a row.
func pinDelay(failures int) time.Duration {
	switch {
	case failures < 5:
		return 0
	case failures < 10:
		return 30 * time.Second
	case failures < 15:
		return time.Minute
	case failures < 20:
		return 5 * time.Minute
	case failures < 30:
		return 30 * time.Minute
	case failures < 40:
		return time.Hour
	}
	return 24 * time.Hour
}

func validPIN(pin string) bool {
	if len(pin) < MinPIN || len(pin) > MaxPIN {
		return false
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *Store) pinHash(pin string, salt []byte, iter int) []byte {
	m := hmac.New(sha256.New, s.lk.pepper)
	m.Write([]byte(pin))
	k, _ := pbkdf2.Key(sha256.New, string(m.Sum(nil)), salt, iter, 32)
	return k
}

// PINResult is how a PIN attempt went: OK (with a token), or how many were
// wrong in a row, how many more are free before a wait (Left), and how
// long to wait before the next (WaitMS).
type PINResult struct {
	OK       bool   `json:"ok"`
	Token    string `json:"token,omitempty"`
	Failures int    `json:"failures,omitempty"`
	Left     int    `json:"left,omitempty"`
	WaitMS   int64  `json:"wait_ms,omitempty"`
}

func waitMS(d time.Duration) int64 { return int64((d + time.Millisecond - 1) / time.Millisecond) }

// checkPIN checks the PIN, counting a wrong one; call with lk.mu held.
func (s *Store) checkPIN(pin string) PINResult {
	l := &s.lk
	now := l.now()
	if wait := l.next.Sub(now); wait > 0 {
		return PINResult{Failures: l.file.Failures, WaitMS: waitMS(wait)}
	}
	salt, _ := hex.DecodeString(l.file.Salt)
	want, _ := hex.DecodeString(l.file.Hash)
	if !validPIN(pin) || !hmac.Equal(s.pinHash(pin, salt, l.file.Iter), want) {
		l.file.Failures++
		d := pinDelay(l.file.Failures)
		l.next = now.Add(d)
		_ = s.saveLock()
		r := PINResult{Failures: l.file.Failures, WaitMS: waitMS(d)}
		if d == 0 {
			for n := l.file.Failures; pinDelay(n) == 0; n++ {
				r.Left++
			}
		}
		return r
	}
	if l.file.Failures != 0 {
		l.file.Failures = 0
		_ = s.saveLock()
	}
	l.next, l.pinAt, l.fpFails, l.fpLocked = time.Time{}, now, 0, false
	return PINResult{OK: true}
}

// LockStatus is what the lock screen and Settings › Security show.
type LockStatus struct {
	PIN      bool  `json:"pin"`
	Failures int   `json:"failures,omitempty"`
	WaitMS   int64 `json:"wait_ms,omitempty"`
	// Sensor names the fingerprint sensor's backend ("" without one).
	Sensor     string   `json:"sensor,omitempty"`
	Fingers    []Finger `json:"fingers"`
	MaxFingers int      `json:"max_fingers"`
	// Blocked says why a fingerprint cannot unlock until the PIN is
	// entered: BlockedRestart, BlockedTimeout, BlockedLockout.
	Blocked string `json:"blocked,omitempty"`
}

func (s *Store) LockStatus() LockStatus {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	l := &s.lk
	st := LockStatus{PIN: l.file.Hash != "", Failures: l.file.Failures, MaxFingers: MaxFingers, Fingers: []Finger{}, Blocked: l.blocked()}
	if wait := l.next.Sub(l.now()); wait > 0 {
		st.WaitMS = waitMS(wait)
	}
	if l.sensor != nil {
		st.Sensor = l.sensor.Name()
	}
	for _, f := range l.fp.Fingers {
		st.Fingers = append(st.Fingers, Finger{ID: f.ID, Name: f.Name})
	}
	return st
}

// VerifyLockPIN checks the PIN; a right one gives a token for the
// challenge (0 when the caller only unlocks).
func (s *Store) VerifyLockPIN(pin string, challenge uint64) (PINResult, error) {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	if s.lk.file.Hash == "" {
		return PINResult{}, ErrNoPIN
	}
	r := s.checkPIN(pin)
	if r.OK {
		r.Token = s.mintToken(AuthToken{Challenge: challenge, User: s.lk.file.User, Type: AuthPIN})
	}
	return r, nil
}

// SetLockPIN sets the PIN, or changes it given the one in force (old).
func (s *Store) SetLockPIN(old, pin string) (PINResult, error) {
	if !validPIN(pin) {
		return PINResult{}, ErrPINFormat
	}
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	l := &s.lk
	if l.file.Hash != "" {
		if r := s.checkPIN(old); !r.OK {
			return r, nil
		}
	} else {
		l.file.User = random64()
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return PINResult{}, err
	}
	l.file.Salt, l.file.Iter, l.file.Failures = hex.EncodeToString(salt), l.iter, 0
	l.file.Hash = hex.EncodeToString(s.pinHash(pin, salt, l.iter))
	if err := s.saveLock(); err != nil {
		return PINResult{}, err
	}
	l.next, l.pinAt, l.fpFails, l.fpLocked = time.Time{}, l.now(), 0, false
	return PINResult{OK: true}, nil
}

// ClearLockPIN removes the PIN given it, and every fingerprint with it (as
// Android and iOS do: a fingerprint only stands in for the PIN).
func (s *Store) ClearLockPIN(old string) (PINResult, error) {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	l := &s.lk
	if l.file.Hash == "" {
		return PINResult{}, ErrNoPIN
	}
	if r := s.checkPIN(old); !r.OK {
		return r, nil
	}
	l.file = lockFile{}
	if err := s.saveLock(); err != nil {
		return PINResult{}, err
	}
	s.cancelOp()
	l.enrolling, l.challenge = nil, 0
	l.fp = fingerFile{Set: newSetID(), Next: l.fp.Next}
	return PINResult{OK: true}, s.saveFingers()
}
