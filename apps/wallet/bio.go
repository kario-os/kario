package main

import "github.com/kario-os/kario/ui"

// bioKey is the key a fingerprint confirms payments with. The system
// offers the fingerprint when the user turned it on for payments, makes
// this key when the payment PIN confirms, and voids it when the
// fingerprints change (the PIN then confirms and makes it again); the
// server takes a proof from either key, the certificate saying which.
const bioKey = "pay-bio"

// payKey is the key the server expects the proof from.
func payKey(p *ui.KeyProof) string {
	if p.KeyAlias == bioKey && p.Bio {
		return bioKey
	}
	return "pay"
}

func howConfirmed(p *ui.KeyProof) string {
	if p.Bio {
		return tr.T("Confirmed with a fingerprint")
	}
	return tr.T("Confirmed with the payment PIN")
}
