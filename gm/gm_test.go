package gm

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSM3Vectors(t *testing.T) {
	for msg, want := range map[string]string{
		"abc":                      "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0",
		strings.Repeat("abcd", 16): "debe9ff92275b8a138604889c18e5a4d6fdb70e5387e5765293dcba39c0c5732",
	} {
		got := SM3([]byte(msg))
		if hex.EncodeToString(got[:]) != want {
			t.Errorf("SM3(%q) = %x", msg, got)
		}
	}
	h := NewSM3()
	for _, part := range []string{"ab", "c"} {
		h.Write([]byte(part))
	}
	if hex.EncodeToString(h.Sum(nil)) != "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0" {
		t.Error("streaming SM3 differs")
	}
}

// GB/T 32918's example key on the recommended curve, and the signature
// the standard gives for its example nonce (cross-checked against gmssl).
// Signing draws a hedged nonce, so the vector is checked by verifying it.
func TestSM2KnownSignature(t *testing.T) {
	k, err := NewPrivateKey(unhex(t, "3945208F7B2144B13F36E38AC6D39F95889393692860B51A42FB81EF4DF7C5B8"))
	if err != nil {
		t.Fatal(err)
	}
	wantPub := "0409f9df311e5421a150dd7d161e4bc5c672179fad1833fc076bb08ff356f35020ccea490ce26775a52dc6ea718cc1aa600aed05fbf35e084a6632f6072da9ad13"
	if hex.EncodeToString(k.PublicKey.Bytes()) != wantPub {
		t.Fatalf("public key %x", k.PublicKey.Bytes())
	}
	rs := unhex(t, "f5a03b0648d2c4630eeac513e1bb81a15944da3827d5b74143ac7eaceee720b3b1b6aa29df212fd8763182bc0d421ca1bb9038fd1f7f42d4840b69c485bbc1aa")
	der, err := asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(rs[:32]), new(big.Int).SetBytes(rs[32:])})
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(&k.PublicKey, DefaultUID, []byte("message digest"), der) {
		t.Fatal("the standard's signature does not verify")
	}
	if Verify(&k.PublicKey, DefaultUID, []byte("message digesT"), der) {
		t.Fatal("the standard's signature verifies another message")
	}
	mine, err := Sign(nil, k, DefaultUID, []byte("message digest"))
	if err != nil || !Verify(&k.PublicKey, DefaultUID, []byte("message digest"), mine) {
		t.Fatal("own signature does not verify")
	}
}

func TestSM2SignVerify(t *testing.T) {
	k, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := GenerateKey(rand.Reader)
	msg := []byte("Pay 12.50 yuan to Morning Light Cafe")
	der, err := Sign(nil, k, DefaultUID, msg)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublicKey(k.PublicKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(pub, DefaultUID, msg, der) {
		t.Fatal("signature does not verify")
	}
	for name, ok := range map[string]bool{
		"other message": Verify(pub, DefaultUID, []byte("Pay 125.0 yuan to Morning Light Cafe"), der),
		"other signer":  Verify(&other.PublicKey, DefaultUID, msg, der),
		"other uid":     Verify(pub, "ALICE123@YAHOO.COM", msg, der),
		"truncated":     Verify(pub, DefaultUID, msg, der[:len(der)-1]),
	} {
		if ok {
			t.Errorf("%s verified", name)
		}
	}
	bad := k.PublicKey.Bytes()
	bad[64] ^= 1
	if _, err := ParsePublicKey(bad); err == nil {
		t.Error("point off the curve accepted")
	}
}

// GB/T 32907 appendix A: one block, and the same block encrypted a million
// times.
func TestSM4Vectors(t *testing.T) {
	key := unhex(t, "0123456789abcdeffedcba9876543210")
	c, err := NewSM4(key)
	if err != nil {
		t.Fatal(err)
	}
	b := append([]byte{}, key...)
	c.Encrypt(b, b)
	if hex.EncodeToString(b) != "681edf34d206965e86b3e94f536e4246" {
		t.Fatalf("SM4 = %x", b)
	}
	c.Decrypt(b, b)
	if !bytes.Equal(b, key) {
		t.Fatal("decrypt does not invert")
	}
	if testing.Short() {
		return
	}
	for i := 0; i < 1000000; i++ {
		c.Encrypt(b, b)
	}
	if hex.EncodeToString(b) != "595298c7c6fd271f0402f804c33d3f66" {
		t.Fatalf("SM4 x10^6 = %x", b)
	}
}

func TestSM4GCM(t *testing.T) {
	c, _ := NewSM4(unhex(t, "0123456789abcdeffedcba9876543210"))
	aead, err := cipher.NewGCM(c)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	sealed := aead.Seal(nil, nonce, []byte("Pay 12.50 yuan"), []byte("order-1"))
	if plain, err := aead.Open(nil, nonce, sealed, []byte("order-1")); err != nil || string(plain) != "Pay 12.50 yuan" {
		t.Fatalf("open: %v %q", err, plain)
	}
	sealed[0] ^= 1
	if _, err := aead.Open(nil, nonce, sealed, []byte("order-1")); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
}
