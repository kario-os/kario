package keystore

import (
	"encoding/json"
	"testing"

	"github.com/kario-os/kario/ui"
)

// FuzzRoots is root.pub as provisioning installs it.
func FuzzRoots(f *testing.F) {
	dir := f.TempDir()
	if _, err := Open(dir); err != nil {
		f.Fatal(err)
	}
	roots, err := LoadRootPub(RootPubPath(dir))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(roots.String())
	f.Add("AAAA\nsm2:BAAA\n")
	f.Fuzz(func(t *testing.T, text string) {
		r, err := ParseRoots(text)
		if err != nil {
			return
		}
		again, err := ParseRoots(r.String())
		if err != nil || again.String() != r.String() {
			t.Fatalf("%q parses, its form %q does not read back: %v", text, r.String(), err)
		}
	})
}

// FuzzVerify is an attestation or a key proof a server checks (an install
// report, a payment's confirmation): every field is the sender's.
func FuzzVerify(f *testing.F) {
	dir := f.TempDir()
	ks, err := Open(dir)
	if err != nil {
		f.Fatal(err)
	}
	roots, err := LoadRootPub(RootPubPath(dir))
	if err != nil {
		f.Fatal(err)
	}
	for _, alg := range []string{"", AlgSM2} {
		if att, err := ks.Attest("com.example.bank", "nonce", alg); err == nil {
			raw, _ := json.Marshal(att)
			f.Add(raw)
		}
		if p, err := ks.Sign("com.example.bank", "pay", alg, "order 1"); err == nil {
			raw, _ := json.Marshal(p)
			f.Add(raw)
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var a ui.Attestation
		if json.Unmarshal(raw, &a) == nil {
			VerifyAttestation(roots, &a, "com.example.bank", "nonce")
		}
		var p ui.KeyProof
		if json.Unmarshal(raw, &p) == nil {
			VerifySign(roots, &p, "com.example.bank", "pay", "order 1")
			VerifyConfirm(roots, &p, "com.example.bank", "pay", "Pay?", "order 1")
		}
	})
}
