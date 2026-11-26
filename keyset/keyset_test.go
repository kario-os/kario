package keyset

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pub(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

// A rotation as a phone sees it: images trusting the old key, then the
// old and the next one, then the next one alone. During the overlap what
// either key signed verifies, by its key ID or (a signer from before key
// IDs) by trying the set; after the old key is retired from the image,
// what it signed fails, as does a key never trusted.
func TestRotation(t *testing.T) {
	old, next, stranger := key(t), key(t), key(t)
	msg := []byte("kario-offer/1\n1700000000\n{}")
	sign := func(k ed25519.PrivateKey) ([]byte, string) {
		sig, id, err := Sign(k, msg)
		if err != nil || id != ID(pub(k)) || len(id) != 16 {
			t.Fatalf("sign: %q %v", id, err)
		}
		return sig, id
	}
	oldSig, oldID := sign(old)
	nextSig, nextID := sign(next)
	strangerSig, strangerID := sign(stranger)

	before := Set{pub(old)}
	overlap := Set{pub(old), pub(next)}
	after := Set{pub(next)}
	cases := []struct {
		name string
		set  Set
		id   string
		sig  []byte
		want error
	}{
		{"old image, old key", before, oldID, oldSig, nil},
		{"old image, old key, no ID", before, "", oldSig, nil},
		{"old image, next key", before, nextID, nextSig, ErrUnknownKey},
		{"old image, next key, no ID", before, "", nextSig, ErrSignature},
		{"overlap, old key", overlap, oldID, oldSig, nil},
		{"overlap, next key", overlap, nextID, nextSig, nil},
		{"overlap, old key, no ID", overlap, "", oldSig, nil},
		{"overlap, next key, no ID", overlap, "", nextSig, nil},
		{"overlap, a stranger", overlap, strangerID, strangerSig, ErrUnknownKey},
		{"overlap, a stranger, no ID", overlap, "", strangerSig, ErrSignature},
		{"overlap, old signature named as the next key's", overlap, nextID, oldSig, ErrSignature},
		{"retired, next key", after, nextID, nextSig, nil},
		{"retired, old key", after, oldID, oldSig, ErrUnknownKey},
		{"retired, old key, no ID", after, "", oldSig, ErrSignature},
	}
	for _, c := range cases {
		err := c.set.Verify(c.id, msg, c.sig)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if err := overlap.Verify(oldID, []byte("another message"), oldSig); !errors.Is(err, ErrSignature) {
		t.Errorf("another message: %v", err)
	}
	if err := (Set{}).Verify("", msg, oldSig); !errors.Is(err, ErrSignature) {
		t.Errorf("empty set: %v", err)
	}
}

func TestFile(t *testing.T) {
	a, b := pub(key(t)), pub(key(t))
	s := Set{a}.Add(b, a)
	if len(s) != 2 || s.Find(ID(b)) == nil || s.Find("0011223344556677") != nil {
		t.Fatalf("set %v", s.IDs())
	}
	text := s.String()
	if strings.Count(text, "\n") != 2 || !strings.HasPrefix(text, Set{a}.String()) {
		t.Fatalf("file %q", text)
	}
	got, err := Parse([]byte("# store-root v1 and v2\n\n  " + strings.ReplaceAll(text, "\n", " \n") + Set{b}.String()))
	if err != nil || got.String() != text {
		t.Fatalf("parsed %v: %v", got.IDs(), err)
	}
	for name, raw := range map[string]string{
		"empty":        "",
		"comment only": "# nothing\n",
		"not base64":   Set{a}.String() + "not a key\n",
		"short key":    "AAAA\n",
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	path := filepath.Join(t.TempDir(), "offer.pub")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got.String() != text {
		t.Fatalf("load: %v", err)
	}
	if _, err := Load(path + ".none"); err == nil {
		t.Fatal("loaded a missing file")
	}
}

// opaque is a key held elsewhere (an HSM): only Public and Sign, and it
// checks it is asked as Ed25519 is, for the message itself.
type opaque struct {
	k      ed25519.PrivateKey
	called bool
}

func (o *opaque) Public() crypto.PublicKey { return o.k.Public() }

func (o *opaque) Sign(r io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	o.called = true
	if opts.HashFunc() != crypto.Hash(0) {
		return nil, errors.New("asked for a digest")
	}
	return o.k.Sign(r, msg, opts)
}

type broken struct{ opaque }

func (b *broken) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return make([]byte, ed25519.SignatureSize), nil
}

func TestSigner(t *testing.T) {
	k := key(t)
	o := &opaque{k: k}
	sig, id, err := Sign(o, []byte("m"))
	if err != nil || !o.called || id != ID(pub(k)) || (Set{pub(k)}).Verify(id, []byte("m"), sig) != nil {
		t.Fatalf("a crypto.Signer: %v", err)
	}
	if _, _, err := Sign(&broken{opaque{k: k}}, []byte("m")); err == nil {
		t.Fatal("took a signature that does not verify")
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, _, err := Sign(ec, []byte("m")); err == nil || !strings.Contains(err.Error(), "not Ed25519") {
		t.Fatalf("an ECDSA key: %v", err)
	}
	if _, _, err := Sign(nil, []byte("m")); err == nil {
		t.Fatal("signed without a key")
	}
}
