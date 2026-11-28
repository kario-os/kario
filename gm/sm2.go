package gm

import (
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"io"

	"github.com/emmansun/gmsm/sm2"
)

// DefaultUID is the signer identity GB/T 35276 uses when none is agreed.
const DefaultUID = "1234567812345678"

type PublicKey struct{ k *ecdsa.PublicKey }

type PrivateKey struct {
	PublicKey
	k *sm2.PrivateKey
}

func GenerateKey(r io.Reader) (*PrivateKey, error) {
	k, err := sm2.GenerateKey(r)
	if err != nil {
		return nil, err
	}
	return &PrivateKey{PublicKey{&k.PublicKey}, k}, nil
}

// NewPrivateKey takes the 32-byte scalar.
func NewPrivateKey(b []byte) (*PrivateKey, error) {
	if len(b) != 32 {
		return nil, errors.New("gm: invalid SM2 private key")
	}
	k, err := sm2.NewPrivateKey(b)
	if err != nil {
		return nil, errors.New("gm: invalid SM2 private key")
	}
	return &PrivateKey{PublicKey{&k.PublicKey}, k}, nil
}

// Bytes is the 32-byte scalar.
func (k *PrivateKey) Bytes() []byte { return k.k.D.FillBytes(make([]byte, 32)) }

// Signer is the key as a crypto.Signer (for X.509 and TLCP).
func (k *PrivateKey) Signer() *sm2.PrivateKey { return k.k }

// Bytes is the uncompressed point 04 || X || Y.
func (k *PublicKey) Bytes() []byte {
	out := []byte{4}
	out = append(out, k.k.X.FillBytes(make([]byte, 32))...)
	return append(out, k.k.Y.FillBytes(make([]byte, 32))...)
}

func ParsePublicKey(b []byte) (*PublicKey, error) {
	if len(b) != 65 || b[0] != 4 {
		return nil, errors.New("gm: SM2 public key must be an uncompressed point")
	}
	k, err := sm2.NewPublicKey(b)
	if err != nil {
		return nil, errors.New("gm: SM2 public key is not on the curve")
	}
	return &PublicKey{k}, nil
}

// Sign signs msg for identity uid (Z_A binds the identity and key into the
// digest); the signature is DER (r, s) as in GM/T 0009.
func Sign(r io.Reader, k *PrivateKey, uid string, msg []byte) ([]byte, error) {
	if r == nil {
		r = rand.Reader
	}
	return sm2.SignASN1(r, k.k, msg, sm2.NewSM2SignerOption(true, []byte(uid)))
}

func Verify(pub *PublicKey, uid string, msg, signature []byte) bool {
	return pub != nil && sm2.VerifyASN1WithSM2(pub.k, []byte(uid), msg, signature)
}
