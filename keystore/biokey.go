package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kario-os/kario/ui"
)

// Biometric-bound keys (iOS's biometryCurrentSet, Android's keys that need
// a strong biometric and setInvalidatedByBiometricEnrollment): such a key
// signs a confirmation only with a fresh token from a fingerprint match,
// and stops working for good once a finger is added or removed, so a
// finger enrolled by someone who learnt the lock-screen PIN cannot pay
// with it. The keystore makes one only after the payment PIN confirmed
// (EnsureBioKey), and makes it again the same way after it was voided.

var (
	ErrKeyInvalidated = errors.New("key_invalidated")
	ErrNoBioKey       = errors.New("no_bio_key")
)

func (s *Store) bioKeyPath(app, alias string) (string, error) {
	dir, err := s.keyDir(app)
	if err != nil {
		return "", err
	}
	if !safeName.MatchString(alias) {
		return "", errors.New("invalid key alias")
	}
	return filepath.Join(dir, alias+".json"), nil
}

// readBioKey reads the biometric-bound key under the set of fingers in
// force; one made under another set is deleted (ErrKeyInvalidated). Call
// with s.mu held.
func (s *Store) readBioKey(app, alias, set string) (*appKey, error) {
	path, err := s.bioKeyPath(app, alias)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoBioKey
	}
	if err != nil {
		return nil, err
	}
	var k appKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, err
	}
	if !k.Bio {
		return nil, fmt.Errorf("key %q is not bound to the fingerprints", alias)
	}
	if k.BioSet != set {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		return nil, ErrKeyInvalidated
	}
	return &k, nil
}

// bioKeyUsable: the key exists under the fingers in force; call with lk.mu
// held.
func (s *Store) bioKeyUsable(app, alias, set string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.readBioKey(app, alias, set)
	return err
}

// EnsureBioKey makes the biometric-bound key if it is missing or was voided,
// bound to the fingers enrolled now; with none enrolled it does nothing.
// The caller has just had the payment PIN confirmed.
func (s *Store) EnsureBioKey(app, alias, alg string) error {
	if alg != "" && alg != AlgSM2 {
		return fmt.Errorf("unknown key algorithm %q", alg)
	}
	s.lk.mu.Lock()
	set, fingers := s.lk.fp.Set, len(s.lk.fp.Fingers)
	s.lk.mu.Unlock()
	if fingers == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, err := s.readBioKey(app, alias, set)
	switch {
	case err == nil && k.Alg == alg:
		return nil
	case err == nil:
		return fmt.Errorf("key %q exists with algorithm %q", alias, algName(k.Alg))
	case !errors.Is(err, ErrNoBioKey) && !errors.Is(err, ErrKeyInvalidated):
		return err
	}
	path, _ := s.bioKeyPath(app, alias)
	nk, err := s.newKey(alg, func(pub string) []byte { return AppKeyCertMsgBio(s.deviceID, app, alias, alg, pub) })
	if err != nil {
		return err
	}
	nk.UserAuth, nk.Bio, nk.BioSet = true, true, set
	return writeJSON(path, nk)
}

// ConfirmBio signs the prompt and payload with the biometric-bound key,
// given the token of the match Authenticate made for this very key.
func (s *Store) ConfirmBio(app, alias, alg, prompt, payload, token string) (*ui.KeyProof, error) {
	s.lk.mu.Lock()
	t, age, err := s.checkToken(token)
	key := app + "\x00" + alias
	op, ok := s.lk.bioOps[key]
	delete(s.lk.bioOps, key)
	set, user := s.lk.fp.Set, s.lk.file.User
	s.lk.mu.Unlock()
	switch {
	case err != nil:
		return nil, err
	case t.Type != AuthFingerprint || !ok || t.Challenge != op.challenge || t.User != user:
		return nil, ErrBadToken
	case age > bioTokenFor:
		return nil, fmt.Errorf("%w: expired", ErrBadToken)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, err := s.readBioKey(app, alias, set)
	if err != nil {
		return nil, err
	}
	if k.Alg != alg {
		return nil, fmt.Errorf("key %q exists with algorithm %q", alias, algName(k.Alg))
	}
	sign, err := k.signer()
	if err != nil {
		return nil, err
	}
	return s.proof(app, alias, sign, k, ConfirmDigestAlg(alg, app, alias, prompt, payload))
}
