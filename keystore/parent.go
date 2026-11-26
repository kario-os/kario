package keystore

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The parental PIN of minor mode (31-lock-and-biometrics.md §10): kept as
// the lock-screen PIN is, a salted slow hash under a pepper of its own
// derived from the device key, in a file of its own with its own count of
// wrong tries and the same growing waits, which a restart does not
// shorten. It unlocks nothing and yields no token: the shell, which
// enforces minor mode, only hears whether it was right. A parental PIN set
// is minor mode on.

type parentFile struct {
	Salt     string `json:"salt,omitempty"`
	Hash     string `json:"hash,omitempty"`
	Iter     int    `json:"iter,omitempty"`
	Failures int    `json:"failures,omitempty"`
}

type parentState struct {
	mu     sync.Mutex
	file   parentFile
	pepper []byte
	// next: no try before it.
	next time.Time
}

// ParentStatus is whether a parental PIN is set, and any wait before the
// next try.
type ParentStatus struct {
	PIN      bool  `json:"pin"`
	Failures int   `json:"failures,omitempty"`
	WaitMS   int64 `json:"wait_ms,omitempty"`
}

func (s *Store) parentPath() string { return filepath.Join(s.dir, "keystore", "parent.json") }

func (s *Store) openParent() error {
	p := &s.pa
	p.pepper = s.derive("kario-parental-pin")
	if raw, err := os.ReadFile(s.parentPath()); err == nil {
		if err := json.Unmarshal(raw, &p.file); err != nil {
			return err
		}
	}
	p.next = s.lk.now().Add(pinDelay(p.file.Failures))
	return nil
}

func (s *Store) saveParent() error {
	if s.pa.file == (parentFile{}) {
		if err := os.Remove(s.parentPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeJSON(s.parentPath(), s.pa.file)
}

// checkParent checks the parental PIN, counting a wrong one; call with
// pa.mu held.
func (s *Store) checkParent(pin string) PINResult {
	p := &s.pa
	now := s.lk.now()
	if wait := p.next.Sub(now); wait > 0 {
		return PINResult{Failures: p.file.Failures, WaitMS: waitMS(wait)}
	}
	salt, _ := hex.DecodeString(p.file.Salt)
	want, _ := hex.DecodeString(p.file.Hash)
	if !validPIN(pin) || !hmac.Equal(hashPIN(p.pepper, pin, salt, p.file.Iter), want) {
		p.file.Failures++
		p.next = now.Add(pinDelay(p.file.Failures))
		_ = s.saveParent()
		return wrongTry(p.file.Failures)
	}
	if p.file.Failures != 0 {
		p.file.Failures = 0
		_ = s.saveParent()
	}
	p.next = time.Time{}
	return PINResult{OK: true}
}

func (s *Store) ParentStatus() ParentStatus {
	s.pa.mu.Lock()
	defer s.pa.mu.Unlock()
	st := ParentStatus{PIN: s.pa.file.Hash != "", Failures: s.pa.file.Failures}
	if wait := s.pa.next.Sub(s.lk.now()); wait > 0 {
		st.WaitMS = waitMS(wait)
	}
	return st
}

// VerifyParentPIN checks the parental PIN.
func (s *Store) VerifyParentPIN(pin string) (PINResult, error) {
	s.pa.mu.Lock()
	defer s.pa.mu.Unlock()
	if s.pa.file.Hash == "" {
		return PINResult{}, ErrNoPIN
	}
	return s.checkParent(pin), nil
}

// SetParentPIN sets the parental PIN, or changes it given the one in force
// (old).
func (s *Store) SetParentPIN(old, pin string) (PINResult, error) {
	if !validPIN(pin) {
		return PINResult{}, ErrPINFormat
	}
	s.pa.mu.Lock()
	defer s.pa.mu.Unlock()
	p := &s.pa
	if p.file.Hash != "" {
		if r := s.checkParent(old); !r.OK {
			return r, nil
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return PINResult{}, err
	}
	p.file = parentFile{Salt: hex.EncodeToString(salt), Iter: s.lk.iter}
	p.file.Hash = hex.EncodeToString(hashPIN(p.pepper, pin, salt, p.file.Iter))
	if err := s.saveParent(); err != nil {
		return PINResult{}, err
	}
	p.next = time.Time{}
	return PINResult{OK: true}, nil
}

// ResetParentPIN replaces the parental PIN without the one in force, its
// wrong tries forgotten. The shell asks only after the account server's
// signed reset for this phone checked out (a parent who forgot the PIN,
// family.VerifyReset); being the one that enforces minor mode, the shell
// gains nothing by it it did not have.
func (s *Store) ResetParentPIN(pin string) (PINResult, error) {
	if !validPIN(pin) {
		return PINResult{}, ErrPINFormat
	}
	s.pa.mu.Lock()
	defer s.pa.mu.Unlock()
	p := &s.pa
	if p.file.Hash == "" {
		return PINResult{}, ErrNoPIN
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return PINResult{}, err
	}
	p.file = parentFile{Salt: hex.EncodeToString(salt), Iter: s.lk.iter}
	p.file.Hash = hex.EncodeToString(hashPIN(p.pepper, pin, salt, p.file.Iter))
	if err := s.saveParent(); err != nil {
		return PINResult{}, err
	}
	p.next = time.Time{}
	return PINResult{OK: true}, nil
}

// ClearParentPIN removes the parental PIN given it: minor mode ends.
func (s *Store) ClearParentPIN(old string) (PINResult, error) {
	s.pa.mu.Lock()
	defer s.pa.mu.Unlock()
	if s.pa.file.Hash == "" {
		return PINResult{}, ErrNoPIN
	}
	if r := s.checkParent(old); !r.OK {
		return r, nil
	}
	s.pa.file = parentFile{}
	return PINResult{OK: true}, s.saveParent()
}
