// Package antitheft is kario-antitheft/1, the anti-theft lock (as Android's
// Factory Reset Protection; docs/31-lock-and-biometrics.md §11). A phone
// with a Kario account signed in binds itself to the account on the
// account server. After a reset its owner did not confirm with the PIN (a
// wipe from the lock screen, a flashed data partition) the phone asks for
// that account before it can be used: the server checks a sign-in code it
// sends to the account's email address, or finds that the owner removed the
// phone on the account's page, and signs a Release for this phone and this
// attempt, which the phone checks against the server's key.
//
// Every request names the phone by a device attestation (keystore's, for
// App) over Nonce: what is asked, for which account, the server's one-time
// challenge and the phone's own nonce. A request cannot be replayed or
// carried to another account, and no phone can be bound or released in
// another's name.
//
// antitheft.go is what the server and the phone share; client.go is the
// phone's side of the HTTP API; package antitheftest is a server in memory
// for tests and the selftest.
package antitheft

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kario-os/kario/keyset"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

const (
	// App is the app a request's device attestation names; no installable
	// app can be called system.anything.
	App = "system.antitheft"
	// A challenge holds for ChallengeTTL, once.
	ChallengeTTL = 5 * time.Minute
	// A release is signed good for ReleaseTTL; a phone takes none good for
	// longer than MaxReleaseTTL.
	ReleaseTTL    = 10 * time.Minute
	MaxReleaseTTL = 15 * time.Minute
	// A code is CodeLen digits, good for CodeTTL and CodeAttempts tries; a
	// phone may have CodesPerHour sent an hour.
	CodeLen      = 6
	CodeTTL      = 10 * time.Minute
	CodeAttempts = 5
	CodesPerHour = 5
)

// What a request asks (Nonce's purpose).
const (
	PurposeBind    = "bind"
	PurposeUnbind  = "unbind"
	PurposeStatus  = "status"
	PurposeCode    = "code"
	PurposeRelease = "release"
)

// Nonce is what the phone's attestation covers for a request: its purpose,
// the account (the login when binding and unbinding, which the request's
// credentials name; the account's ID after a reset), the server's challenge
// and the phone's own nonce ("" for binding and unbinding).
func Nonce(purpose, account, challenge, own string) string {
	sum := sha256.Sum256([]byte("kario-antitheft/1\n" + purpose + "\n" + account + "\n" + challenge + "\n" + own))
	return hex.EncodeToString(sum[:])
}

// Request is every request's body (but the challenge's).
type Request struct {
	Challenge   string          `json:"challenge"`
	Account     string          `json:"account,omitempty"`
	Nonce       string          `json:"nonce,omitempty"`
	Code        string          `json:"code,omitempty"`
	Attestation *ui.Attestation `json:"attestation"`
}

// Challenge is the server's answer to a request for one.
type Challenge struct {
	Challenge string `json:"challenge"`
	ExpiresIn int    `json:"expires_in"`
}

// Bound is the server's answer to a binding: the account's ID, which a
// release names, and its email address masked, which the locked phone
// shows.
type Bound struct {
	Account string `json:"account"`
	Email   string `json:"email"`
}

// Status is whether the phone is bound to the account it asked about, and
// the account's email address masked; Release when it is not because the
// owner removed it.
type Status struct {
	Bound   bool    `json:"bound"`
	Email   string  `json:"email,omitempty"`
	Release *Signed `json:"release,omitempty"`
}

// CodeSent is the answer to a request for a code: where it went, masked.
type CodeSent struct {
	Email string `json:"email"`
}

// Release is what the server signs: the phone (Device) may be used again
// without the account (Account), for the attempt the phone named with its
// own nonce (Nonce), issued and good until Expires (Unix seconds).
type Release struct {
	Device  string `json:"device"`
	Account string `json:"account"`
	Nonce   string `json:"nonce"`
	Issued  int64  `json:"issued"`
	Expires int64  `json:"expires"`
}

// Signed is a release as the server hands it out: the exact bytes it signed
// and the Ed25519 signature over Message of them, both base64, and the ID
// of the key that signed (keyset.ID; none from a server before key IDs).
type Signed struct {
	Release string `json:"release"`
	Sig     string `json:"sig"`
	Key     string `json:"key,omitempty"`
}

// Message is what is signed for a release's bytes: the account server's
// key signs minor mode's policies too (package family), under prefixes of
// their own, so no signature passes for another kind.
func Message(payload []byte) []byte {
	return append([]byte("kario-antitheft-release/1\n"), payload...)
}

// Sign signs r with the server's key: an ed25519.PrivateKey or a
// crypto.Signer holding one (an HSM's).
func Sign(key crypto.Signer, r Release) (Signed, error) {
	if r.Device == "" || r.Account == "" || r.Nonce == "" {
		return Signed{}, errors.New("antitheft: a release names a phone, an account and the attempt")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return Signed{}, err
	}
	sig, id, err := keyset.Sign(key, Message(raw))
	if err != nil {
		return Signed{}, fmt.Errorf("antitheft: the server's key: %w", err)
	}
	return Signed{Release: base64.StdEncoding.EncodeToString(raw), Sig: base64.StdEncoding.EncodeToString(sig), Key: id}, nil
}

var (
	ErrSignature = errors.New("antitheft: the signature does not match the account server's key")
	ErrRelease   = errors.New("antitheft: the release is for another phone, account or attempt")
)

// Verify checks a signed release for this phone (device), the account it
// was bound to and the attempt it named (nonce): the server's signature by
// the key of keys the release names (by any of them without an ID), the
// three named, a span no longer than MaxReleaseTTL. The phone's clock
// is not asked: after a reset it may be anything, and the nonce, the
// phone's own and used once, is what makes a release fresh.
func Verify(keys keyset.Set, s Signed, device, account, nonce string) (Release, error) {
	raw, err1 := base64.StdEncoding.DecodeString(s.Release)
	sig, err2 := base64.StdEncoding.DecodeString(s.Sig)
	if err1 != nil || err2 != nil || keys.Verify(s.Key, Message(raw), sig) != nil {
		return Release{}, ErrSignature
	}
	var r Release
	if err := json.Unmarshal(raw, &r); err != nil {
		return Release{}, fmt.Errorf("antitheft: the release does not parse: %w", err)
	}
	switch {
	case r.Device != device || r.Account != account || r.Nonce != nonce || nonce == "":
		return Release{}, ErrRelease
	case r.Expires < r.Issued || r.Expires-r.Issued > int64(MaxReleaseTTL/time.Second):
		return Release{}, errors.New("antitheft: the release is good for too long")
	}
	return r, nil
}

// CheckDevice checks that the request comes from the device its attestation
// names: signed for App over nonce with the device's Ed25519 key, under the
// certifier's roots when the server has them (a provisioned phone); without
// them, under the device's own key, whose ID the device's must be (a phone
// before provisioning proves it holds the key, not that it is genuine).
func CheckDevice(roots *keystore.Roots, a *ui.Attestation, nonce string) error {
	if a == nil || a.Alg != "" {
		return errors.New("antitheft: an Ed25519 device attestation is needed")
	}
	if roots != nil {
		return keystore.VerifyAttestation(roots, a, App, nonce)
	}
	pub, err := base64.StdEncoding.DecodeString(a.DevicePub)
	sig, err2 := base64.StdEncoding.DecodeString(a.Sig)
	switch {
	case err != nil || err2 != nil || len(pub) != ed25519.PublicKeySize:
		return errors.New("antitheft: the attestation's key does not parse")
	case a.AppID != App || a.Nonce != nonce:
		return errors.New("antitheft: the attestation is for another app or request")
	case keystore.DeviceIDFor(pub) != a.DeviceID:
		return errors.New("antitheft: the device ID is not its key's")
	case !ed25519.Verify(pub, keystore.AttestMsg(a.DeviceID, a.AppID, a.Nonce, a.Boot), sig):
		return errors.New("antitheft: the attestation's signature does not check out")
	}
	return nil
}

// MaskEmail shows enough of an address for its owner to know it and no
// more: its first and last letters before the @, and the domain.
func MaskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	r := []rune(local)
	switch {
	case !ok:
		return "***"
	case len(r) <= 2:
		return string(r[:min(1, len(r))]) + "***@" + domain
	}
	return string(r[0]) + "***" + string(r[len(r)-1]) + "@" + domain
}
