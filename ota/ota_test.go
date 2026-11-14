package ota

import (
	"testing"

	"github.com/kario-os/kario/keystore"
)

// A report verifies only as the device signed it, and only under the
// device's root.
func TestVerifyReport(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := keystore.LoadRootPub(keystore.RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	r := Report{Version: "0.7.0", Outcome: OutcomeGood, Bucket: 42, Kind: KindDay, Crashes: 3}
	att, err := ks.Attest(ReportApp, ReportNonce(r), "")
	if err != nil {
		t.Fatal(err)
	}
	r.Attestation = att
	if err := VerifyReport(r, roots); err != nil {
		t.Fatalf("a signed report: %v", err)
	}
	changed := r
	changed.Outcome = OutcomeRollback
	fewer := r
	fewer.Crashes = 0
	if VerifyReport(changed, roots) == nil || VerifyReport(fewer, roots) == nil {
		t.Fatal("a changed report verified")
	}
	if VerifyReport(Report{Version: "0.7.0", Outcome: OutcomeGood}, roots) == nil {
		t.Fatal("an unsigned report verified")
	}
	otherDir := t.TempDir()
	if _, err := keystore.Open(otherDir); err != nil {
		t.Fatal(err)
	}
	otherRoots, err := keystore.LoadRootPub(keystore.RootPubPath(otherDir))
	if err != nil {
		t.Fatal(err)
	}
	if VerifyReport(r, otherRoots) == nil {
		t.Fatal("verified under another root")
	}
}
