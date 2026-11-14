package keystore_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/wire"
)

func TestProvision(t *testing.T) {
	state := t.TempDir()
	ks, err := keystore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	devRoot, _ := keystore.LoadRootPub(keystore.RootPubPath(state))
	sock := filepath.Join(t.TempDir(), "keystored.sock")
	go func() {
		_ = wire.Serve(sock, func(method string, params json.RawMessage) (any, error) {
			var p struct {
				Cert, Root string
				CertSM2    string `json:"cert_sm2"`
			}
			_ = json.Unmarshal(params, &p)
			switch method {
			case "csr":
				id, pub, pubSM2 := ks.CSR()
				return map[string]string{"device_id": id, "device_pub": pub, "device_pub_sm2": pubSM2}, nil
			case "install_cert":
				return map[string]bool{"ok": true}, ks.InstallCert(p.Cert, p.CertSM2, p.Root)
			}
			return nil, nil
		})
	}()
	time.Sleep(100 * time.Millisecond)
	ca, err := keystore.LoadCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ks.Sign("com.example.a", "k", "", "x")
	gmKey, err := ks.Sign("com.example.a", "g", keystore.AlgSM2, "x")
	if err != nil || keystore.VerifySign(devRoot, gmKey, "com.example.a", "g", "x") != nil {
		t.Fatalf("SM2 chain before provisioning: %v", err)
	}
	id, err := keystore.Provision(ca, sock)
	if err != nil {
		t.Fatal(err)
	}
	if !ks.Provisioned() {
		t.Fatal("not provisioned")
	}
	root, _ := keystore.LoadRootPub(keystore.RootPubPath(state))
	for _, alg := range []string{"", keystore.AlgSM2} {
		att, err := ks.Attest("com.example.a", "n", alg)
		if err != nil || att.DeviceID != id || keystore.VerifyAttestation(root, att, "com.example.a", "n") != nil {
			t.Fatalf("the %q attestation does not chain to the CA: %v", alg, err)
		}
		if keystore.VerifyAttestation(devRoot, att, "com.example.a", "n") == nil {
			t.Fatalf("the development root still certifies the device (%q)", alg)
		}
	}
	if again, _ := ks.Sign("com.example.a", "g", keystore.AlgSM2, "x"); again.AppPub != gmKey.AppPub || keystore.VerifySign(root, again, "com.example.a", "g", "x") != nil {
		t.Fatal("SM2 app key lost or not chained after provisioning")
	}
	if root.SM2 == nil || ca.Root != root.String() {
		t.Fatal("root.pub does not carry the CA's SM2 root")
	}
	if again, _ := ks.Sign("com.example.a", "k", "", "x"); again.AppPub != key.AppPub || keystore.VerifySign(root, again, "com.example.a", "k", "x") != nil {
		t.Fatal("app keys lost or not chained after provisioning")
	}
	reopened, err := keystore.Open(state)
	if err != nil || !reopened.Provisioned() {
		t.Fatal("provisioning not kept")
	}
	other, _ := keystore.LoadCA(t.TempDir())
	cid, cpub, cpubSM2 := ks.CSR()
	goodSM2, _ := keystore.SignDeviceCertSM2(ca.SM2, cid, cpubSM2)
	if ks.InstallCert(keystore.SignDeviceCert(other.Key, cid, cpub), goodSM2, ca.Root) == nil {
		t.Fatal("a certificate from another CA was installed")
	}
	if ks.InstallCert(keystore.SignDeviceCert(ca.Key, "PH-someoneelse", cpub), goodSM2, ca.Root) == nil {
		t.Fatal("a certificate for another device was installed")
	}
	otherSM2, _ := keystore.SignDeviceCertSM2(other.SM2, cid, cpubSM2)
	if ks.InstallCert(keystore.SignDeviceCert(ca.Key, cid, cpub), otherSM2, ca.Root) == nil {
		t.Fatal("an SM2 certificate from another CA was installed")
	}
	if ks.InstallCert(keystore.SignDeviceCert(ca.Key, cid, cpub), "", ca.Root) == nil {
		t.Fatal("installed without the SM2 certificate the root calls for")
	}
}
