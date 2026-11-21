// Package ota is the part of the system update protocol a phone and an
// update server share (25-system-update.md §0): what the server signs for
// latest.json and the system data it serves (holidays.go), and how a
// phone signs its install report.
package ota

import (
	"crypto"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/kario-os/kario/keyset"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

// OfferMessage is what an update server signs for latest.json: the time
// and the exact bytes it serves.
func OfferMessage(issued int64, body []byte) []byte {
	return append([]byte(fmt.Sprintf("kario-offer/1\n%d\n", issued)), body...)
}

// The headers a file the update server signs goes out with (latest.json
// over OfferMessage, system data over DataMessage): when it was signed,
// the Ed25519 signature (base64) and the ID of the key that made it
// (keyset.ID; none from a server before key IDs).
const (
	HeaderIssued    = "X-Offer-Issued"
	HeaderSignature = "X-Offer-Signature"
	HeaderKey       = "X-Offer-Key"
)

// SignHeaders signs msg, made for the time issued, with key (an
// ed25519.PrivateKey or a crypto.Signer holding one, an HSM's) into h.
func SignHeaders(h http.Header, key crypto.Signer, issued int64, msg []byte) error {
	sig, id, err := keyset.Sign(key, msg)
	if err != nil {
		return err
	}
	h.Set(HeaderIssued, strconv.FormatInt(issued, 10))
	h.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	h.Set(HeaderKey, id)
	return nil
}

// VerifyHeaders checks the signature in h over msg of the time h gives,
// by the key of keys that h names (by any of them when h names none), and
// gives that time.
func VerifyHeaders(h http.Header, keys keyset.Set, msg func(issued int64) []byte) (int64, error) {
	issued, err := strconv.ParseInt(h.Get(HeaderIssued), 10, 64)
	if err != nil {
		return 0, errors.New("no signing time")
	}
	sig, err := base64.StdEncoding.DecodeString(h.Get(HeaderSignature))
	if err != nil {
		return 0, keyset.ErrSignature
	}
	return issued, keys.Verify(h.Get(HeaderKey), msg(issued), sig)
}

// ReportApp is the app an install report's attestation names; app IDs
// under "system." are not installable.
const ReportApp = "system.ota-report"

// Report is how an install went, as a phone posts it to /report. The
// rollout bucket is the only thing about the phone in the report itself;
// the attestation (the device's, for ReportApp, with ReportNonce) lets a
// server that holds the certifier's root count genuine phones, each once.
type Report struct {
	Version string `json:"version"`
	Outcome string `json:"outcome"`
	Bucket  int    `json:"bucket"`
	// Kind is "" for how the install went, KindDay for the report a day
	// after the new slot was marked good: Crashes counts the app crashes
	// since, so a rollout can also stop for a version that runs badly.
	Kind        string          `json:"kind,omitempty"`
	Crashes     int             `json:"crashes,omitempty"`
	Attestation *ui.Attestation `json:"attestation,omitempty"`
}

// KindDay is the report a day after an update.
const KindDay = "day"

const (
	OutcomeGood     = "good"
	OutcomeRollback = "rollback"
)

// ReportNonce binds an attestation to the report's fields.
func ReportNonce(r Report) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("kario-report/2\n%s\n%s\n%d\n%s\n%d", r.Version, r.Outcome, r.Bucket, r.Kind, r.Crashes)))
	return hex.EncodeToString(h[:])
}

// VerifyReport checks that a device under roots signed the report as it
// stands.
func VerifyReport(r Report, roots *keystore.Roots) error {
	if r.Attestation == nil {
		return errors.New("the report is not signed by the device")
	}
	return keystore.VerifyAttestation(roots, r.Attestation, ReportApp, ReportNonce(r))
}
