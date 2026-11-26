package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// Secrets the system keeps for itself, such as a contacts account's
// app-specific password (shell/accounts.go): the shell holds them sealed,
// with AES-256-GCM under a key derived from the device key (in production
// the TEE's), which never leaves this service. A secret is sealed to its
// purpose ("carddav:<account>"): opened for another, it does not open, so a
// sealed password copied into another account's record is no use there.
// The secrets are not apps' keys: unlocking the device keeps them.

const (
	sealedPrefix = "s1."
	maxSecret    = 4 << 10
	maxPurpose   = 128
)

var (
	ErrSecretTooBig = errors.New("a secret is at most 4 KB")
	ErrNotSealed    = errors.New("cannot open the sealed secret")
)

var b64url = base64.RawURLEncoding

func (s *Store) secretAEAD() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.derive("kario-system-secrets"))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealSecret is plain sealed for purpose: "s1." and base64url of the
// nonce and the ciphertext.
func (s *Store) SealSecret(purpose string, plain []byte) (string, error) {
	if len(plain) > maxSecret {
		return "", ErrSecretTooBig
	}
	if purpose == "" || len(purpose) > maxPurpose {
		return "", errors.New("a secret needs a purpose of at most 128 bytes")
	}
	gcm, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sealedPrefix + b64url.EncodeToString(gcm.Seal(nonce, nonce, plain, []byte(purpose))), nil
}

// OpenSecret is what SealSecret sealed for the same purpose.
func (s *Store) OpenSecret(purpose, sealed string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, sealedPrefix)
	raw, err := b64url.DecodeString(rest)
	if !ok || err != nil {
		return nil, ErrNotSealed
	}
	gcm, err := s.secretAEAD()
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return nil, ErrNotSealed
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(purpose))
	if err != nil {
		return nil, ErrNotSealed
	}
	return plain, nil
}
