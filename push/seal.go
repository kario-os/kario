package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// End-to-end encryption of push data, in the spirit of Web Push (RFC 8291):
// the app keeps an X25519 key of its own (not in any system service), hands
// the public half to its server, and the server seals each push's data to
// it. The gateway, pushd and the shell only ever carry ciphertext. The
// notification title and body stay readable: the system has to show them,
// so a server that wants them private sends data only and lets the woken
// app post its own notification.
//
// Sealed data: "e1." + base64url(ephemeral public key | nonce | AES-256-GCM
// ciphertext), the key derived by HKDF-SHA256 from the X25519 secret, with
// both public keys bound into the derivation.

const sealPrefix = "e1."

var curve = ecdh.X25519()

// IsSealed reports whether data was sealed with Seal.
func IsSealed(data string) bool { return strings.HasPrefix(data, sealPrefix) }

func sealKey(secret, ephPub, recipientPub []byte) ([]byte, error) {
	info := append(append([]byte("phone push e1"), ephPub...), recipientPub...)
	return hkdf.Key(sha256.New, secret, nil, string(info), 32)
}

// Seal encrypts plaintext to the recipient's public key (raw 32 bytes,
// base64url as the app hands it out).
func Seal(recipient string, plaintext []byte) (string, error) {
	rawPub, err := b64.DecodeString(recipient)
	if err != nil {
		return "", errors.New("bad push key")
	}
	pub, err := curve.NewPublicKey(rawPub)
	if err != nil {
		return "", err
	}
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	secret, err := eph.ECDH(pub)
	if err != nil {
		return "", err
	}
	key, err := sealKey(secret, eph.PublicKey().Bytes(), rawPub)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(append(eph.PublicKey().Bytes(), nonce...), gcm.Seal(nil, nonce, plaintext, nil)...)
	return sealPrefix + b64.EncodeToString(out), nil
}

// Open decrypts data sealed to key.
func Open(key *ecdh.PrivateKey, sealed string) ([]byte, error) {
	bad := errors.New("cannot open sealed push data")
	raw, err := b64.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if !IsSealed(sealed) || err != nil || len(raw) < 32+12+16 {
		return nil, bad
	}
	ephPub, rest := raw[:32], raw[32:]
	pub, err := curve.NewPublicKey(ephPub)
	if err != nil {
		return nil, bad
	}
	secret, err := key.ECDH(pub)
	if err != nil {
		return nil, bad
	}
	k, err := sealKey(secret, ephPub, key.PublicKey().Bytes())
	if err != nil {
		return nil, bad
	}
	block, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], nil)
	if err != nil {
		return nil, bad
	}
	return plain, nil
}

// KeyFile loads (or creates) a push key kept in a file (0600). On the device
// keystored keeps one per app; apps only ever see the public half.
func KeyFile(p string) (*ecdh.PrivateKey, error) {
	if raw, err := os.ReadFile(p); err == nil {
		return curve.NewPrivateKey(raw)
	}
	k, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, k.Bytes(), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// PublicKey is how an app hands its push key to its server.
func PublicKey(k *ecdh.PrivateKey) string { return b64.EncodeToString(k.PublicKey().Bytes()) }
