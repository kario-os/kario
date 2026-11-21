// Package switchrecv is the phone's side of kario-switch/1
// (docs/36-device-switch.md): the "Kario Switch" app on an old phone scans
// the QR code the receiver shows, then sends contacts, the call log,
// messages, calendar events, photos and videos over HTTP on the local
// network, every body sealed with keys only the code carries. The Shell
// runs it (shell/switch.go) and takes what arrives through a Sink.
package switchrecv

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Info is HKDF's info, the protocol's name and version.
const Info = "kario-switch/1"

// An envelope's nonce starts with its direction; the counter follows.
const (
	dirUp   uint32 = 1
	dirDown uint32 = 2
)

const (
	nonceSize = 12
	tagSize   = 16
	keySize   = 32
	tokenSize = 32
)

var errEnvelope = errors.New("the envelope does not open")

// keys are a session's two AES-256-GCM keys: up seals what the app sends,
// down what the receiver answers.
type keys struct{ up, down []byte }

// deriveKeys is X25519 of one side's private key and the other's public
// key, then HKDF-SHA256 over it with the token's raw bytes as the salt.
func deriveKeys(priv *ecdh.PrivateKey, peer, token []byte) (keys, error) {
	pub, err := ecdh.X25519().NewPublicKey(peer)
	if err != nil {
		return keys{}, err
	}
	// ECDH refuses a low-order point (an all-zero result).
	shared, err := priv.ECDH(pub)
	if err != nil {
		return keys{}, err
	}
	k, err := hkdf.Key(sha256.New, shared, token, Info, 2*keySize)
	if err != nil {
		return keys{}, err
	}
	return keys{up: k[:keySize], down: k[keySize:]}, nil
}

func newAEAD(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return g
}

// seal is an envelope: the nonce (direction, counter) and the ciphertext
// with its tag, the request's method and target as the additional data.
func seal(aead cipher.AEAD, dir uint32, counter uint64, plain []byte, aad string) []byte {
	var nonce [nonceSize]byte
	binary.BigEndian.PutUint32(nonce[:4], dir)
	binary.BigEndian.PutUint64(nonce[4:], counter)
	return aead.Seal(nonce[:], nonce[:], plain, []byte(aad))
}

// open opens an envelope sealed for dir and gives its counter.
func open(aead cipher.AEAD, dir uint32, env []byte, aad string) (uint64, []byte, error) {
	if len(env) < nonceSize+tagSize || binary.BigEndian.Uint32(env[:4]) != dir {
		return 0, nil, errEnvelope
	}
	plain, err := aead.Open(nil, env[:nonceSize], env[nonceSize:], []byte(aad))
	if err != nil {
		return 0, nil, errEnvelope
	}
	return binary.BigEndian.Uint64(env[4:nonceSize]), plain, nil
}
