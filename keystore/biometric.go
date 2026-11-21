package keystore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kario-os/kario/hal/fingerprint"
	"github.com/kario-os/kario/ui"
)

// Fingerprints, matched here as the TEE would: the templates never leave
// this service (on disk they are sealed with a key derived from the device
// key), and a match yields an AuthToken, never the template. The rules are
// Android's and iOS's: at most MaxFingers fingers; FingerFailLimit failed
// matches in a row and the PIN is needed before the next try; after a
// restart, and PINEvery after the PIN was last entered, the first unlock
// needs the PIN; enrolling needs a fresh PIN token; removing the PIN
// removes the fingers. Each change to the set of fingers gives it a new
// ID, and biometric-bound keys made under the old one stop working.

const (
	MaxFingers      = 5
	FingerFailLimit = 5
	PINEvery        = 48 * time.Hour
	maxFingerName   = 32
	// A PIN token opens enrolling for enrolTokenFor (Android: 10 minutes);
	// a match allows one biometric signature within bioTokenFor.
	enrolTokenFor = 10 * time.Minute
	bioTokenFor   = 30 * time.Second
)

// Why a fingerprint needs the PIN first.
const (
	BlockedRestart = "restart"
	BlockedTimeout = "timeout"
	BlockedLockout = "lockout"
)

var (
	ErrNoSensor       = errors.New("no_sensor")
	ErrNoFingerprints = errors.New("no_fingerprints")
	ErrTooManyFingers = errors.New("too_many_fingerprints")
	ErrNoEnrolment    = errors.New("no enrolment in progress")
	ErrPINRequired    = errors.New("pin_required")
	ErrFingerName     = errors.New("a fingerprint's name is 1 to 32 characters")
	errCancelled      = errors.New(ui.ErrCancelled)
)

// Finger is an enrolled finger as others see it: no template.
type Finger struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// fingerFile is keystore/fingerprints.json.
type fingerFile struct {
	Set     string      `json:"set"`
	Next    int         `json:"next,omitempty"`
	Fingers []fingerRec `json:"fingers,omitempty"`
}

type fingerRec struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Template string `json:"template"`
}

func newSetID() string { return fmt.Sprintf("%016x", random64()) }

func (s *Store) fingerPath() string { return filepath.Join(s.dir, "keystore", "fingerprints.json") }

func (s *Store) loadFingers() error {
	raw, err := os.ReadFile(s.fingerPath())
	if errors.Is(err, os.ErrNotExist) {
		s.lk.fp = fingerFile{Set: newSetID()}
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &s.lk.fp)
}

func (s *Store) saveFingers() error { return writeJSON(s.fingerPath(), s.lk.fp) }

func (s *Store) seal(t fingerprint.Template) (string, error) {
	block, err := aes.NewCipher(s.lk.sealKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return b64.EncodeToString(gcm.Seal(nonce, nonce, t, nil)), nil
}

func (s *Store) unseal(sealed string) (fingerprint.Template, error) {
	raw, err := b64.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.lk.sealKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("corrupt template")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
}

// SetSensor gives the keystore its sensor (nil: none).
func (s *Store) SetSensor(sensor fingerprint.Sensor) {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	s.lk.sensor = sensor
}

// blocked: why a fingerprint needs the PIN first, "" if it does not; call
// with lk.mu held.
func (l *lockState) blocked() string {
	switch {
	case l.fpLocked:
		return BlockedLockout
	case l.pinAt.IsZero():
		return BlockedRestart
	case l.now().Sub(l.pinAt) > PINEvery:
		return BlockedTimeout
	}
	return ""
}

// startOp makes ctx the sensor's operation, cancelling the one before;
// call with lk.mu held.
func (s *Store) startOp(ctx context.Context) (context.Context, int) {
	s.cancelOp()
	ctx, cancel := context.WithCancel(ctx)
	s.lk.opSeq++
	s.lk.op = cancel
	return ctx, s.lk.opSeq
}

func (s *Store) cancelOp() {
	if s.lk.op != nil {
		s.lk.op()
		s.lk.op = nil
	}
}

func (s *Store) endOp(seq int) {
	if s.lk.opSeq == seq {
		s.cancelOp()
	}
}

// CancelSensor ends what the sensor is doing: a wait for a match, an
// enrolment (which is dropped).
func (s *Store) CancelSensor() {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	s.cancelOp()
	s.lk.enrolling = nil
}

func validFingerName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n > 0 && n <= maxFingerName && utf8.ValidString(name) && strings.TrimSpace(name) != "" &&
		!strings.ContainsFunc(name, unicode.IsControl)
}

// EnrollChallenge starts the way to enrolling: the PIN is to be verified
// against this challenge (VerifyLockPIN), and its token handed to
// EnrollBegin or RemoveFinger within 10 minutes.
func (s *Store) EnrollChallenge() (uint64, error) {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	if s.lk.file.Hash == "" {
		return 0, ErrNoPIN
	}
	s.lk.challenge, s.lk.challengeAt = random64(), s.lk.now()
	return s.lk.challenge, nil
}

// checkPINToken: a PIN token for the enrolment challenge, fresh; call with
// lk.mu held.
func (s *Store) checkPINToken(token string) error {
	t, age, err := s.checkToken(token)
	switch {
	case err != nil:
		return err
	case t.Type != AuthPIN || s.lk.challenge == 0 || t.Challenge != s.lk.challenge || t.User != s.lk.file.User:
		return ErrBadToken
	case age > enrolTokenFor || s.lk.now().Sub(s.lk.challengeAt) > enrolTokenFor:
		return fmt.Errorf("%w: expired", ErrBadToken)
	}
	return nil
}

// EnrollBegin starts enrolling a finger under the name; it returns how
// many touches it takes.
func (s *Store) EnrollBegin(token, name string) (int, error) {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	l := &s.lk
	switch {
	case l.sensor == nil:
		return 0, ErrNoSensor
	case l.file.Hash == "":
		return 0, ErrNoPIN
	case len(l.fp.Fingers) >= MaxFingers:
		return 0, ErrTooManyFingers
	case !validFingerName(name):
		return 0, ErrFingerName
	}
	if err := s.checkPINToken(token); err != nil {
		return 0, err
	}
	s.cancelOp()
	l.enrolling, l.enrolName, l.enrolDone = l.sensor.Enroll(), name, 0
	return l.enrolling.Steps(), nil
}

// EnrollStep is one touch of an enrolment; Finger is set once it is done.
type EnrollStep struct {
	Done     int     `json:"done"`
	Steps    int     `json:"steps"`
	Rejected string  `json:"rejected,omitempty"`
	Finger   *Finger `json:"finger,omitempty"`
}

// match finds the enrolled finger the sample is; call with lk.mu held.
func (s *Store) match(sample fingerprint.Sample) *fingerRec {
	for i := range s.lk.fp.Fingers {
		f := &s.lk.fp.Fingers[i]
		if t, err := s.unseal(f.Template); err == nil && s.lk.sensor.Match(t, sample) {
			return f
		}
	}
	return nil
}

// EnrollNext waits for the next touch of the enrolment.
func (s *Store) EnrollNext(ctx context.Context) (EnrollStep, error) {
	s.lk.mu.Lock()
	l := &s.lk
	e, sensor := l.enrolling, l.sensor
	if e == nil || sensor == nil {
		s.lk.mu.Unlock()
		return EnrollStep{}, ErrNoEnrolment
	}
	ctx, seq := s.startOp(ctx)
	s.lk.mu.Unlock()
	sample, err := sensor.Capture(ctx)
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	s.endOp(seq)
	if err != nil || l.enrolling != e {
		return EnrollStep{}, errCancelled
	}
	step := EnrollStep{Done: l.enrolDone, Steps: e.Steps()}
	if l.enrolDone == 0 && s.match(sample) != nil {
		step.Rejected = fingerprint.RejectEnrolled
		return step, nil
	}
	p := e.Add(sample)
	l.enrolDone = p.Done
	step.Done, step.Rejected = p.Done, p.Rejected
	if p.Done < p.Steps {
		return step, nil
	}
	sealed, err := s.seal(e.Template())
	if err != nil {
		return EnrollStep{}, err
	}
	l.fp.Next++
	f := fingerRec{ID: l.fp.Next, Name: l.enrolName, Template: sealed}
	l.fp.Fingers = append(l.fp.Fingers, f)
	l.fp.Set = newSetID()
	l.enrolling = nil
	if err := s.saveFingers(); err != nil {
		return EnrollStep{}, err
	}
	step.Finger = &Finger{ID: f.ID, Name: f.Name}
	return step, nil
}

// RenameFinger gives an enrolled finger another name.
func (s *Store) RenameFinger(id int, name string) error {
	if !validFingerName(name) {
		return ErrFingerName
	}
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	for i := range s.lk.fp.Fingers {
		if s.lk.fp.Fingers[i].ID == id {
			s.lk.fp.Fingers[i].Name = name
			return s.saveFingers()
		}
	}
	return fmt.Errorf("no fingerprint %d", id)
}

// RemoveFinger deletes an enrolled finger, given a PIN token as for
// enrolling.
func (s *Store) RemoveFinger(token string, id int) error {
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	if err := s.checkPINToken(token); err != nil {
		return err
	}
	for i, f := range s.lk.fp.Fingers {
		if f.ID == id {
			s.lk.fp.Fingers = append(s.lk.fp.Fingers[:i], s.lk.fp.Fingers[i+1:]...)
			s.lk.fp.Set = newSetID()
			return s.saveFingers()
		}
	}
	return fmt.Errorf("no fingerprint %d", id)
}

// AuthResult is how a touch went: a match (with a token), or not, how many
// failed in a row and whether that disabled fingerprints until the PIN.
type AuthResult struct {
	Match    bool   `json:"match"`
	Finger   int    `json:"finger,omitempty"`
	Token    string `json:"token,omitempty"`
	Failures int    `json:"failures,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// Authenticate waits for a touch and matches it. With app and alias the
// match is for one signature with that biometric-bound key (ConfirmBio),
// which is checked first: a key the fingers changed since is invalidated.
func (s *Store) Authenticate(ctx context.Context, app, alias string) (AuthResult, error) {
	s.lk.mu.Lock()
	l := &s.lk
	switch {
	case l.sensor == nil:
		s.lk.mu.Unlock()
		return AuthResult{}, ErrNoSensor
	case l.file.Hash == "" || len(l.fp.Fingers) == 0:
		s.lk.mu.Unlock()
		return AuthResult{}, ErrNoFingerprints
	}
	if why := l.blocked(); why != "" {
		s.lk.mu.Unlock()
		return AuthResult{}, fmt.Errorf("%w:%s", ErrPINRequired, why)
	}
	if app != "" {
		if err := s.bioKeyUsable(app, alias, l.fp.Set); err != nil {
			s.lk.mu.Unlock()
			return AuthResult{}, err
		}
	}
	sensor := l.sensor
	ctx, seq := s.startOp(ctx)
	s.lk.mu.Unlock()
	sample, err := sensor.Capture(ctx)
	s.lk.mu.Lock()
	defer s.lk.mu.Unlock()
	s.endOp(seq)
	if err != nil {
		return AuthResult{}, errCancelled
	}
	if why := l.blocked(); why != "" {
		return AuthResult{}, fmt.Errorf("%w:%s", ErrPINRequired, why)
	}
	f := s.match(sample)
	if f == nil {
		l.fpFails++
		l.fpLocked = l.fpFails >= FingerFailLimit
		return AuthResult{Failures: l.fpFails, Disabled: l.fpLocked}, nil
	}
	l.fpFails = 0
	t := AuthToken{Challenge: random64(), User: l.file.User, AuthID: uint64(f.ID), Type: AuthFingerprint}
	if app != "" {
		l.bioOps[app+"\x00"+alias] = bioOp{challenge: t.Challenge, at: l.now()}
	}
	return AuthResult{Match: true, Finger: f.ID, Token: s.mintToken(t)}, nil
}
