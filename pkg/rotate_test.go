package pkg

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/keyset"
)

// hsmKey is a store root held elsewhere (an HSM): a crypto.Signer that is
// not an ed25519.PrivateKey.
type hsmKey struct{ k ed25519.PrivateKey }

func (h hsmKey) Public() crypto.PublicKey { return h.k.Public() }
func (h hsmKey) Sign(r io.Reader, msg []byte, o crypto.SignerOpts) ([]byte, error) {
	return h.k.Sign(r, msg, o)
}

// signed is what a store root signs, as devices get it.
type signed struct {
	pkg, catalog, revocations []byte
	cert                      DevCert
}

func signAll(t *testing.T, store *Store, dir string, devKey ed25519.PrivateKey, now time.Time) signed {
	t.Helper()
	cert, err := store.Certify("example", devKey.Public().(ed25519.PublicKey))
	must(t, err)
	raw, err := Build(dir, Developer{Key: devKey, Cert: cert})
	must(t, err)
	cat, err := store.SignCatalog([]map[string]string{{"id": "com.example.notes", "version": "1.0"}}, now)
	must(t, err)
	rv, err := store.SignRevocations(Revocations{Issued: now.Unix(), Revoked: []Revoked{}})
	must(t, err)
	c, _ := json.Marshal(cat)
	r, _ := json.Marshal(rv)
	return signed{pkg: raw, catalog: c, revocations: r, cert: cert}
}

// verifyAll is a device with roots: whether it takes the package, the
// catalog and the revocation list.
func verifyAll(s signed, roots Roots, now time.Time) []error {
	_, e1 := Open(s.pkg, roots)
	_, e2 := VerifyCatalog(s.catalog, roots, now)
	_, e3 := VerifyRevocations(s.revocations, roots)
	return []error{e1, e2, e3}
}

// A store root rotated (11-platform-architecture.md §15.1): the next key
// made and shipped in an image while the old one signs, then signing
// switched (the store's developers certified again), then the old one
// retired from the image. An image with both keys takes what either signed;
// the image before takes only the old key's; the image after only the new
// key's. The store's root may be in an HSM.
func TestStoreRootRotation(t *testing.T) {
	dir, dev, _ := setup(t)
	now := time.Now()
	_, oldKey, _ := ed25519.GenerateKey(nil)
	_, nextKey, _ := ed25519.GenerateKey(nil)
	oldPub, nextPub := oldKey.Public().(ed25519.PublicKey), nextKey.Public().(ed25519.PublicKey)

	before := &Store{Name: "store", Key: hsmKey{oldKey}}
	staged := &Store{Name: "store", Key: hsmKey{oldKey}, Trusted: keyset.Set{nextPub}}
	switched := &Store{Name: "store", Key: hsmKey{nextKey}, Trusted: keyset.Set{oldPub}}
	if staged.Keys().String() != (keyset.Set{oldPub, nextPub}).String() || switched.Keys()[0].Equal(oldPub) {
		t.Fatal("the store's keys: the one that signs first")
	}
	byOld, byNext := signAll(t, staged, dir, dev.Key, now), signAll(t, switched, dir, dev.Key, now)
	if byOld.cert.Key != keyset.ID(oldPub) || byNext.cert.Key != keyset.ID(nextPub) {
		t.Fatal("certificates without their key ID")
	}
	if !before.Issued(byOld.cert) || switched.Issued(byOld.cert) || !switched.Issued(byNext.cert) {
		t.Fatal("Issued: only the key that signs now issued a certificate")
	}

	image := func(lines string) Roots {
		trust := t.TempDir()
		must(t, os.WriteFile(filepath.Join(trust, "store.pub"), []byte(lines), 0o644))
		return LoadRoots(trust)
	}
	oldImage := image(before.Keys().String())
	overlap := image(staged.Keys().String())
	newImage := image((&Store{Name: "store", Key: nextKey}).Keys().String())
	for name, c := range map[string]struct {
		s     signed
		roots Roots
		ok    bool
	}{
		"old image, old key":       {byOld, oldImage, true},
		"old image, new key":       {byNext, oldImage, false},
		"both keys, old key":       {byOld, overlap, true},
		"both keys, new key":       {byNext, overlap, true},
		"retired image, new key":   {byNext, newImage, true},
		"retired image, old key":   {byOld, newImage, false},
		"the store itself, old":    {byOld, switched.Roots(), true},
		"the store itself, staged": {byNext, staged.Roots(), true},
	} {
		for i, err := range verifyAll(c.s, c.roots, now) {
			if c.ok != (err == nil) {
				t.Errorf("%s: [package catalog revocations][%d]: %v", name, i, err)
			}
		}
	}

	// The image's file adds to what the factory provisioned on the device.
	factory := t.TempDir()
	must(t, os.WriteFile(filepath.Join(factory, "store.pub"), []byte(before.Keys().String()), 0o644))
	trust := t.TempDir()
	must(t, os.WriteFile(filepath.Join(trust, "store.pub"), []byte("# store-root v2, next\n"+keyset.Set{nextPub}.String()), 0o644))
	if got := LoadRoots(trust, factory)["store"]; len(got) != 2 {
		t.Fatalf("the store's keys from two places: %v", got.IDs())
	}

	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	bad := &Store{Name: "store", Key: ec}
	if _, err := bad.Certify("example", oldPub); err == nil || !strings.Contains(err.Error(), "not Ed25519") {
		t.Fatalf("an ECDSA root certified: %v", err)
	}
	if _, err := bad.SignCatalog([]string{}, now); err == nil {
		t.Fatal("an ECDSA root signed a catalog")
	}
	if _, err := bad.SignRevocations(Revocations{}); err == nil {
		t.Fatal("an ECDSA root signed revocations")
	}
}

// What a store signs with key IDs, as a system from before them reads it:
// its own structures without the new field, one key a store.
func TestOldSystems(t *testing.T) {
	dir, dev, _ := setup(t)
	_, root, _ := ed25519.GenerateKey(nil)
	store := &Store{Name: "store", Key: root}
	s := signAll(t, store, dir, dev.Key, time.Now())
	pub := root.Public().(ed25519.PublicKey)

	type oldCert struct {
		Name  string `json:"name"`
		Pub   string `json:"pub"`
		Store string `json:"store"`
		Sig   string `json:"sig"`
	}
	type oldSignature struct {
		Format    string  `json:"format"`
		App       string  `json:"app"`
		Version   string  `json:"version"`
		Files     []File  `json:"files"`
		Developer oldCert `json:"developer"`
		Sig       string  `json:"sig"`
	}
	p, err := Open(s.pkg, store.Roots())
	must(t, err)
	var sig oldSignature
	must(t, json.Unmarshal(p.sig, &sig))
	if !strings.Contains(string(p.sig), `"key": "`+keyset.ID(pub)) {
		t.Fatalf("the package's certificate names no key: %s", p.sig)
	}
	certSig, _ := b64.DecodeString(sig.Developer.Sig)
	if !ed25519.Verify(pub, certMsg(sig.Developer.Store, sig.Developer.Name, sig.Developer.Pub), certSig) {
		t.Fatal("an old system: the certificate")
	}
	devSig, _ := b64.DecodeString(sig.Sig)
	sig.Sig = ""
	body, _ := json.Marshal(sig)
	if !ed25519.Verify(dev.Key.Public().(ed25519.PublicKey), append([]byte(Format+"\n"), body...), devSig) {
		t.Fatal("an old system: the developer's signature")
	}

	type oldRevocations struct {
		Format  string    `json:"format"`
		Store   string    `json:"store"`
		Issued  int64     `json:"issued"`
		Revoked []Revoked `json:"revoked"`
		Sig     string    `json:"sig,omitempty"`
	}
	var rv oldRevocations
	must(t, json.Unmarshal(s.revocations, &rv))
	rvSig, _ := b64.DecodeString(rv.Sig)
	rv.Sig = ""
	body, _ = json.Marshal(rv)
	if !ed25519.Verify(pub, append([]byte(RevocationsFormat+"\n"), body...), rvSig) {
		t.Fatal("an old system: the revocation list")
	}

	var cat Catalog
	must(t, json.Unmarshal(s.catalog, &cat))
	catSig, _ := b64.DecodeString(cat.Sig)
	if cat.Key == "" || !ed25519.Verify(pub, cat.signedBytes(), catSig) {
		t.Fatal("an old system: the catalog")
	}
}
