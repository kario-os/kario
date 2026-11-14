package shell

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/keystore"
)

func TestReportSignature(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := keystore.LoadRootPub(keystore.RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	r := CompatReport{System: Brand, Result: "PASS", Coverage: "1/1", About: map[string]any{"apps": 13},
		Cases: []CaseResult{{Name: "a", Status: "PASS", Seconds: 1.5}}, Capabilities: map[string]CapStat{}}
	r.Attestation, _ = ks.Attest(cts.Signer, cts.ReportNonce(r), "")
	raw, _ := json.MarshalIndent(r, "", "  ")
	if _, err := cts.VerifyReport(raw, root); err != nil {
		t.Fatalf("signed report: %v", err)
	}
	forged := strings.Replace(string(raw), `"status": "PASS"`, `"status": "SKIP"`, 1)
	if _, err := cts.VerifyReport([]byte(forged), root); err == nil {
		t.Fatal("an edited report verified")
	}
	dir2 := t.TempDir()
	if _, err := keystore.Open(dir2); err != nil {
		t.Fatal(err)
	}
	otherRoot, err := keystore.LoadRootPub(keystore.RootPubPath(dir2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cts.VerifyReport(raw, otherRoot); err == nil {
		t.Fatal("verified against another root")
	}
}
