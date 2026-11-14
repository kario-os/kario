// Package ota is the part of the system update protocol a phone and an
// update server share (25-system-update.md §0): what the server signs for
// latest.json and the system data it serves (holidays.go), and how a
// phone signs its install report.
package ota

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

// OfferMessage is what an update server signs for latest.json: the time
// and the exact bytes it serves.
func OfferMessage(issued int64, body []byte) []byte {
	return append([]byte(fmt.Sprintf("kario-offer/1\n%d\n", issued)), body...)
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
