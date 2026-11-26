// Package keyset is a trust point's keys: the Ed25519 public keys a phone
// accepts for one purpose (a store's root, the update server's offers, the
// account server's minor mode policies), several at once so that a key can
// be replaced without a time when a phone trusts neither the old key nor
// the new one (11-platform-architecture.md §15.1). A signature names the key that
// made it by its ID and is checked against that key only; one without an
// ID, from a signer older than key IDs, against each key of the set. A key
// file holds a set one base64 key a line, the one that signs first.
//
// The signing side takes a crypto.Signer, so the key may sit in an HSM.
package keyset

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ID names pub in signatures: the first 8 bytes of its SHA-256, in hex.
func ID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// Set is the keys a trust point accepts: the one that signs and, while a
// key is rotated, the next one or the one it replaced.
type Set []ed25519.PublicKey

var (
	ErrUnknownKey = errors.New("signed with a key that is not trusted here")
	ErrSignature  = errors.New("the signature does not match the key")
)

// Parse reads a key file: one base64 Ed25519 public key a line; blank
// lines and lines starting with # are skipped, and a key given twice
// counts once.
func Parse(raw []byte) (Set, error) {
	var s Set
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, err := base64.StdEncoding.DecodeString(line)
		if err != nil || len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("line %d is not a base64 Ed25519 public key", i+1)
		}
		s = s.Add(k)
	}
	if len(s) == 0 {
		return nil, errors.New("no key")
	}
	return s, nil
}

// Load reads the key file at path.
func Load(path string) (Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// String is s as a key file.
func (s Set) String() string {
	var b strings.Builder
	for _, k := range s {
		b.WriteString(base64.StdEncoding.EncodeToString(k) + "\n")
	}
	return b.String()
}

// Add is s with the keys of more it does not have yet, in their order.
func (s Set) Add(more ...ed25519.PublicKey) Set {
	for _, k := range more {
		if len(k) == ed25519.PublicKeySize && s.Find(ID(k)) == nil {
			s = append(s, k)
		}
	}
	return s
}

// Find is the key of s that id names, nil if none.
func (s Set) Find(id string) ed25519.PublicKey {
	for _, k := range s {
		if len(k) == ed25519.PublicKeySize && ID(k) == id {
			return k
		}
	}
	return nil
}

// IDs are the keys' IDs, in order.
func (s Set) IDs() []string {
	out := make([]string, len(s))
	for i, k := range s {
		out[i] = ID(k)
	}
	return out
}

// Verify checks sig over msg: by the key id names (ErrUnknownKey when s
// has no such key), or with id "" by any key of s.
func (s Set) Verify(id string, msg, sig []byte) error {
	if id == "" {
		for _, k := range s {
			if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, msg, sig) {
				return nil
			}
		}
		return ErrSignature
	}
	k := s.Find(id)
	if k == nil {
		return fmt.Errorf("%w (key %s)", ErrUnknownKey, id)
	}
	if !ed25519.Verify(k, msg, sig) {
		return ErrSignature
	}
	return nil
}

// Public is signer's Ed25519 public key: signer is an ed25519.PrivateKey or
// a crypto.Signer holding one (an HSM's).
func Public(signer crypto.Signer) (ed25519.PublicKey, error) {
	if signer == nil {
		return nil, errors.New("no signing key")
	}
	pub, ok := signer.Public().(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("a %T key, not Ed25519", signer.Public())
	}
	return pub, nil
}

// Sign signs msg with signer as Ed25519 does (the message itself:
// crypto.Hash(0)) and gives the key's ID. A signature that does not
// verify under the signer's public key (a faulty HSM) is an error here
// rather than on the phones.
func Sign(signer crypto.Signer, msg []byte) (sig []byte, id string, err error) {
	pub, err := Public(signer)
	if err != nil {
		return nil, "", err
	}
	if sig, err = signer.Sign(rand.Reader, msg, crypto.Hash(0)); err != nil {
		return nil, "", err
	}
	if !ed25519.Verify(pub, msg, sig) {
		return nil, "", errors.New("the signing key's signature does not verify under its public key")
	}
	return sig, ID(pub), nil
}
