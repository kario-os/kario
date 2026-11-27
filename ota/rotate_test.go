package ota

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/kario-os/kario/keyset"
)

// The update server's offer key rotated (11-platform-architecture.md
// §15.1): latest.json signed with the old key and with the next one goes
// through an image that has both, through the image before only with the
// old key, through the image after only with the next. A server from
// before key IDs sends no X-Offer-Key and is checked against each key.
func TestOfferKeyRotation(t *testing.T) {
	_, oldKey, _ := ed25519.GenerateKey(rand.Reader)
	_, nextKey, _ := ed25519.GenerateKey(rand.Reader)
	oldPub, nextPub := oldKey.Public().(ed25519.PublicKey), nextKey.Public().(ed25519.PublicKey)
	body := []byte(`{"version":"0.9.0"}`)
	msg := func(issued int64) []byte { return OfferMessage(issued, body) }
	signed := func(k ed25519.PrivateKey) http.Header {
		h := http.Header{}
		if err := SignHeaders(h, k, 1_800_000_000, msg(1_800_000_000)); err != nil {
			t.Fatal(err)
		}
		return h
	}
	byOld, byNext := signed(oldKey), signed(nextKey)
	if byOld.Get(HeaderKey) != keyset.ID(oldPub) {
		t.Fatalf("X-Offer-Key %q", byOld.Get(HeaderKey))
	}
	oldServer := http.Header{}
	oldServer.Set(HeaderIssued, strconv.Itoa(1_800_000_000))
	oldServer.Set(HeaderSignature, base64.StdEncoding.EncodeToString(ed25519.Sign(oldKey, msg(1_800_000_000))))
	for name, c := range map[string]struct {
		keys keyset.Set
		h    http.Header
		want error
	}{
		"old image, old key":       {keyset.Set{oldPub}, byOld, nil},
		"old image, next key":      {keyset.Set{oldPub}, byNext, keyset.ErrUnknownKey},
		"old image, old server":    {keyset.Set{oldPub}, oldServer, nil},
		"both keys, old key":       {keyset.Set{oldPub, nextPub}, byOld, nil},
		"both keys, next key":      {keyset.Set{oldPub, nextPub}, byNext, nil},
		"both keys, old server":    {keyset.Set{oldPub, nextPub}, oldServer, nil},
		"old key retired, next":    {keyset.Set{nextPub}, byNext, nil},
		"old key retired, old":     {keyset.Set{nextPub}, byOld, keyset.ErrUnknownKey},
		"old key retired, old srv": {keyset.Set{nextPub}, oldServer, keyset.ErrSignature},
	} {
		issued, err := VerifyHeaders(c.h, c.keys, msg)
		if c.want == nil && (err != nil || issued != 1_800_000_000) || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %d %v", name, issued, err)
		}
	}
	other := byNext.Clone()
	other.Set(HeaderIssued, "1800000001")
	if _, err := VerifyHeaders(other, keyset.Set{nextPub}, msg); err == nil {
		t.Error("another signing time verified")
	}
	if _, err := VerifyHeaders(http.Header{}, keyset.Set{nextPub}, msg); err == nil {
		t.Error("an unsigned file verified")
	}
}
