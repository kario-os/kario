package pkg

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestIdentityNumbers(t *testing.T) {
	// GB 11643's example, and a credit code built for these tests.
	for _, ok := range []string{"11010519491231002X", "11010519491231002x"} {
		if !ValidResidentID(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"110105194912310021", "1101051949123100", "1101051949123100AX"} {
		if ValidResidentID(bad) {
			t.Errorf("%s accepted", bad)
		}
	}
	if !ValidCreditCode("91110108MA01ABCD2H") || !ValidCreditCode("91310000MA1FL0000N") {
		t.Error("valid credit code refused")
	}
	for _, bad := range []string{"91110108MA01ABCD2J", "91110108MA01ABCDIH", "91110108MA01ABCD2"} {
		if ValidCreditCode(bad) {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestRevocations(t *testing.T) {
	state := t.TempDir()
	store, err := DevStore(filepath.Join(state, "store"), filepath.Join(state, "trust"))
	must(t, err)
	roots := LoadRoots(filepath.Join(state, "trust"))
	signed := store.SignRevocations(Revocations{Issued: 7, Revoked: []Revoked{{App: "com.example.notes", Reason: "Collects data unlawfully", At: 7}}})
	raw, _ := json.Marshal(signed)
	r, err := VerifyRevocations(raw, roots)
	if err != nil || len(r.Revoked) != 1 || r.Revoked[0].App != "com.example.notes" {
		t.Fatalf("verify: %v %+v", err, r)
	}
	forged := signed
	forged.Revoked = nil
	raw, _ = json.Marshal(forged)
	if _, err := VerifyRevocations(raw, roots); err == nil {
		t.Fatal("an emptied list verified")
	}
	other, err := DevStore(filepath.Join(state, "other"), filepath.Join(state, "trust2"))
	must(t, err)
	raw, _ = json.Marshal(other.SignRevocations(Revocations{Issued: 9}))
	if _, err := VerifyRevocations(raw, roots); err == nil {
		t.Fatal("a list from an untrusted store verified")
	}
}
