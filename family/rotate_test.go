package family_test

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/kario-os/kario/family"
	"github.com/kario-os/kario/keyset"
)

// hsmKey is a policy key held elsewhere (an HSM): only a crypto.Signer.
type hsmKey struct{ k ed25519.PrivateKey }

func (h hsmKey) Public() crypto.PublicKey { return h.k.Public() }
func (h hsmKey) Sign(r io.Reader, msg []byte, o crypto.SignerOpts) ([]byte, error) {
	return h.k.Sign(r, msg, o)
}

// The account server's policy key rotated (11-platform-architecture.md
// §15.1): a phone whose image has the old key and the next one takes a
// policy, a grant and a reset from either; one with the old key alone
// only the old key's; once the old key left the image, what it signed is
// refused. A policy from a server before key IDs carries none and still
// verifies; the key may be in an HSM.
func TestRotation(t *testing.T) {
	_, oldKey, _ := ed25519.GenerateKey(rand.Reader)
	_, nextKey, _ := ed25519.GenerateKey(rand.Reader)
	oldPub, nextPub := oldKey.Public().(ed25519.PublicKey), nextKey.Public().(ed25519.PublicKey)
	now := time.Unix(1_800_000_000, 0)
	today := now.Format(time.DateOnly)
	type signed struct {
		p family.Signed
		g family.SignedGrant
		r family.SignedReset
	}
	sign := func(k crypto.Signer) signed {
		t.Helper()
		p, err1 := family.Sign(k, family.Policy{Device: "fd1", Version: 1, Issued: now.Unix(), Expires: now.Add(time.Hour).Unix(), Rules: rules()})
		g, err2 := family.SignGrant(k, family.Grant{Device: "fd1", Request: "q1a2b3c4d5", App: "com.phone.calc", Date: today, Minutes: 15,
			Issued: now.Unix(), Expires: now.Add(time.Hour).Unix()})
		r, err3 := family.SignReset(k, family.Reset{Device: "fd1", ID: "r1a2b3c4d5", Issued: now.Unix(), Expires: now.Add(family.ResetTTL).Unix()})
		if err := errors.Join(err1, err2, err3); err != nil {
			t.Fatal(err)
		}
		return signed{p, g, r}
	}
	verify := func(keys keyset.Set, s signed) []error {
		_, e1 := family.Verify(keys, s.p, "fd1", 0, now)
		_, e2 := family.VerifyGrant(keys, s.g, "fd1", today, now)
		_, e3 := family.VerifyReset(keys, s.r, "fd1", now)
		return []error{e1, e2, e3}
	}
	byOld, byNext := sign(hsmKey{oldKey}), sign(nextKey)
	if byOld.p.Key != keyset.ID(oldPub) || byNext.g.Key != keyset.ID(nextPub) || byNext.r.Key != keyset.ID(nextPub) {
		t.Fatal("signed without the key's ID")
	}
	unnamed := byOld
	unnamed.p.Key, unnamed.g.Key, unnamed.r.Key = "", "", ""
	for name, c := range map[string]struct {
		keys keyset.Set
		s    signed
		ok   bool
	}{
		"old image, old key":          {keyset.Set{oldPub}, byOld, true},
		"old image, next key":         {keyset.Set{oldPub}, byNext, false},
		"both keys, old key":          {keyset.Set{oldPub, nextPub}, byOld, true},
		"both keys, next key":         {keyset.Set{oldPub, nextPub}, byNext, true},
		"both keys, no key ID":        {keyset.Set{nextPub, oldPub}, unnamed, true},
		"old key retired, next key":   {keyset.Set{nextPub}, byNext, true},
		"old key retired, old key":    {keyset.Set{nextPub}, byOld, false},
		"old key retired, no key ID":  {keyset.Set{nextPub}, unnamed, false},
		"no key ID, before rotations": {keyset.Set{oldPub}, unnamed, true},
	} {
		for i, err := range verify(c.keys, c.s) {
			if c.ok != (err == nil) || err != nil && !errors.Is(err, family.ErrSignature) {
				t.Errorf("%s: [policy grant reset][%d]: %v", name, i, err)
			}
		}
	}
	// A phone from before key IDs reads the policy as it did.
	raw, _ := json.Marshal(byNext.p)
	var old struct{ Policy, Sig string }
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if _, err := family.Verify(keyset.Set{nextPub}, family.Signed{Policy: old.Policy, Sig: old.Sig}, "fd1", 0, now); err != nil {
		t.Fatalf("without the key ID: %v", err)
	}
}
