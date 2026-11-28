package ota

import (
	"encoding/json"
	"testing"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

// The bundle nonce and the token protocol's JSON as kario-otasrv has them:
// the vector is what the server's own bundleNonce gives.
func TestBundleNonce(t *testing.T) {
	const challenge, bundle = "0123456789abcdef0123456789abcdef", "phone-bundle-0.7.1-oneplus-fajita.raucb"
	if got := BundleNonce(challenge, bundle); got != "277b6ccb48e17f10272b705169f99cb1631394f8cdce02c7cb850a45d83fa6a4" {
		t.Fatalf("BundleNonce = %s", got)
	}
	if BundleNonce(challenge, "phone-delta-0.7.0-0.7.1.raucb") == BundleNonce(challenge, bundle) || BundleNonce(challenge[1:], bundle) == BundleNonce(challenge, bundle) {
		t.Fatal("the nonce does not bind the bundle and the challenge")
	}
	for v, want := range map[any]string{
		BundleChallenge{Challenge: "c", ExpiresIn: 300}:                                                    `{"challenge":"c","expires_in":300}`,
		BundleTokenRequest{Bundle: bundle, Challenge: "c", Attestation: &ui.Attestation{AppID: BundleApp}}: `{"bundle":"` + bundle + `","challenge":"c","attestation":{"device_id":"","device_pub":"","device_cert":"","app_id":"system.ota-bundle","nonce":"","boot":"","sig":""}}`,
		BundleToken{Token: "1.x", ExpiresIn: 3600}:                                                         `{"token":"1.x","expires_in":3600}`,
	} {
		raw, _ := json.Marshal(v)
		if string(raw) != want {
			t.Errorf("%T: %s, want %s", v, raw, want)
		}
	}
	dir := t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := keystore.LoadRootPub(keystore.RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	att, err := ks.Attest(BundleApp, BundleNonce(challenge, bundle), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := keystore.VerifyAttestation(roots, att, BundleApp, BundleNonce(challenge, bundle)); err != nil {
		t.Fatalf("the phone's attestation: %v", err)
	}
	if keystore.VerifyAttestation(roots, att, ReportApp, BundleNonce(challenge, bundle)) == nil {
		t.Fatal("a bundle attestation verified as another app's")
	}
}

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
