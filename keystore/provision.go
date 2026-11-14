package keystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/gm"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// The factory station's side of provisioning (cmd/phone-provision): the
// certifier's CA signs each device's certificate, so relying parties trust
// the CA's root instead of anything a device says about itself. In
// production the CA key sits in an offline HSM and the station talks to the
// device over USB.

// CA is the certifier's signing keys (Ed25519 and SM2) and their roots in
// root.pub form.
type CA struct {
	Key  ed25519.PrivateKey
	SM2  *gm.PrivateKey
	Root string
}

// LoadCA reads the CA in dir, making the keys it lacks on first use.
func LoadCA(dir string) (*CA, error) {
	keyPath := filepath.Join(dir, "ca.key")
	var key ed25519.PrivateKey
	if raw, err := os.ReadFile(keyPath); err == nil {
		seed, err := b64.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("corrupt %s", keyPath)
		}
		key = ed25519.NewKeyFromSeed(seed)
	} else {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, []byte(b64.EncodeToString(k.Seed())+"\n"), 0o600); err != nil {
			return nil, err
		}
		key = k
	}
	sm2, err := loadOrCreateSM2(filepath.Join(dir, "ca-sm2.key"))
	if err != nil {
		return nil, err
	}
	roots := &Roots{Ed25519: key.Public().(ed25519.PublicKey), SM2: &sm2.PublicKey}
	ca := &CA{Key: key, SM2: sm2, Root: roots.String()}
	return ca, os.WriteFile(filepath.Join(dir, "root.pub"), []byte(ca.Root), 0o644)
}

// CSR is what a device's keystore hands the station to certify: its ID and
// public keys.
type CSR struct {
	DeviceID     string `json:"device_id"`
	DevicePub    string `json:"device_pub"`
	DevicePubSM2 string `json:"device_pub_sm2"`
}

// DeviceCerts are a device's certificates and the roots they chain to.
type DeviceCerts struct {
	Cert    string `json:"cert"`
	CertSM2 string `json:"cert_sm2"`
	Root    string `json:"root"`
}

// ProvisionApp is the app the device's attestation names when a station
// asks for its certificates: it proves the device holds the key in its CSR.
const ProvisionApp = "system.provision"

// ReadCSR asks the keystore behind sock for the device's public keys.
func ReadCSR(sock string) (CSR, error) {
	var csr CSR
	cl, err := wire.Dial(sock)
	if err != nil {
		return csr, err
	}
	defer cl.Close()
	if err := cl.Call(svc.KeyCSR, nil, &csr); err != nil {
		return csr, err
	}
	if csr.DeviceID == "" || csr.DevicePub == "" || csr.DevicePubSM2 == "" {
		return csr, errors.New("the device sent no key")
	}
	return csr, nil
}

// AttestFor has the keystore behind sock attest nonce as the device, for
// ProvisionApp.
func AttestFor(sock, nonce string) (*ui.Attestation, error) {
	cl, err := wire.Dial(sock)
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	var att ui.Attestation
	return &att, cl.Call(svc.KeyAttest, map[string]string{"app": ProvisionApp, "nonce": nonce}, &att)
}

// VerifyPossession checks that the device in csr signed the attestation
// with the key the CSR names (before any certificate exists for it).
func VerifyPossession(csr CSR, a *ui.Attestation, nonce string) error {
	if a == nil || a.DeviceID != csr.DeviceID || a.DevicePub != csr.DevicePub || a.AppID != ProvisionApp || a.Nonce != nonce {
		return errors.New("the attestation is for another device, app or nonce")
	}
	if !verifyApp(a.Alg, csr.DevicePub, AttestMsg(a.DeviceID, a.AppID, a.Nonce, a.Boot), a.Sig) {
		return errors.New("the device did not sign the attestation with its key")
	}
	return nil
}

// Certify signs the device's keys.
func (ca *CA) Certify(csr CSR) (DeviceCerts, error) {
	certSM2, err := SignDeviceCertSM2(ca.SM2, csr.DeviceID, csr.DevicePubSM2)
	if err != nil {
		return DeviceCerts{}, err
	}
	return DeviceCerts{Cert: SignDeviceCert(ca.Key, csr.DeviceID, csr.DevicePub), CertSM2: certSM2, Root: ca.Root}, nil
}

// InstallCerts hands the keystore behind sock its certificates.
func InstallCerts(sock string, c DeviceCerts) error {
	cl, err := wire.Dial(sock)
	if err != nil {
		return err
	}
	defer cl.Close()
	return cl.Call(svc.KeyInstallCert, map[string]string{"cert": c.Cert, "cert_sm2": c.CertSM2, "root": c.Root}, nil)
}

// Provision certifies the device behind the keystore socket with a CA at
// hand (a test, or a station holding the CA itself): it asks for the
// device's public keys, signs them and installs the certificates with the
// roots.
func Provision(ca *CA, sock string) (string, error) {
	csr, err := ReadCSR(sock)
	if err != nil {
		return "", err
	}
	certs, err := ca.Certify(csr)
	if err != nil {
		return "", err
	}
	return csr.DeviceID, InstallCerts(sock, certs)
}
