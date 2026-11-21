package gm

import (
	"bytes"
	"testing"
)

// FuzzSM2 is an SM2 public key and signature from a peer (an attestation,
// a certificate chain): a point parses to the same bytes, and checking a
// signature only ever says yes or no.
func FuzzSM2(f *testing.F) {
	k, _ := NewPrivateKey(bytes.Repeat([]byte{3}, 32))
	sig, _ := Sign(nil, k, DefaultUID, []byte("msg"))
	f.Add(k.PublicKey.Bytes(), []byte("msg"), sig)
	f.Fuzz(func(t *testing.T, point, msg, sig []byte) {
		pub, err := ParsePublicKey(point)
		if err != nil {
			pub = &k.PublicKey
		} else if !bytes.Equal(pub.Bytes(), point) {
			t.Fatalf("%x parses to %x", point, pub.Bytes())
		}
		Verify(pub, DefaultUID, msg, sig)
	})
}
