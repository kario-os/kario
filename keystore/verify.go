package keystore

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kario-os/kario/gm"
	"github.com/kario-os/kario/ui"
)

// Canonical messages. Relying servers rebuild these byte-for-byte.

func DeviceCertMsg(deviceID, devicePub string) []byte {
	return []byte("phone-cert-v0|device|" + deviceID + "|" + devicePub)
}

// DeviceCertMsgSM2 is the device's SM2 key under the same ID: the second
// chain, all SM2 from the certifier's root to the app's signature.
func DeviceCertMsgSM2(deviceID, devicePub string) []byte {
	return []byte("phone-cert-v0|device-sm2|" + deviceID + "|" + devicePub)
}

// AlgSM2 marks SM2 app keys (GM/T): SM3 digests, SM2 signatures with the
// default signer identity. Ed25519 keys have no algorithm name.
const AlgSM2 = "sm2"

// AppKeyCertMsgAlg is AppKeyCertMsg with the key's algorithm bound in, so a
// key cannot be presented as the other kind.
func AppKeyCertMsgAlg(deviceID, app, alias, alg string, userAuth bool, appPub string) []byte {
	if alg == "" {
		return AppKeyCertMsg(deviceID, app, alias, userAuth, appPub)
	}
	ua := "0"
	if userAuth {
		ua = "1"
	}
	return []byte("phone-cert-v0|appkey-" + alg + "|" + deviceID + "|" + app + "|" + alias + "|" + ua + "|" + appPub)
}

// AppKeyCertMsgBio certifies a biometric-bound key: it signs only after a
// fingerprint match, and only while the fingers enrolled when it was made
// stay the same.
func AppKeyCertMsgBio(deviceID, app, alias, alg, appPub string) []byte {
	kind := "appkey-bio"
	if alg != "" {
		kind += "-" + alg
	}
	return []byte("phone-cert-v0|" + kind + "|" + deviceID + "|" + app + "|" + alias + "|1|" + appPub)
}

func AppKeyCertMsg(deviceID, app, alias string, userAuth bool, appPub string) []byte {
	ua := "0"
	if userAuth {
		ua = "1"
	}
	return []byte("phone-cert-v0|appkey|" + deviceID + "|" + app + "|" + alias + "|" + ua + "|" + appPub)
}

func AttestMsg(deviceID, app, nonce, boot string) []byte {
	return []byte("phone-attest-v0|" + deviceID + "|" + app + "|" + nonce + "|" + boot)
}

func digest(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])
}

func digestSM3(parts ...string) string {
	h := gm.SM3([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])
}

func SignDigest(app, alias, payload string) string {
	return digest("phone-sign-v0", app, alias, payload)
}

func ConfirmDigest(app, alias, prompt, payload string) string {
	return digest("phone-confirm-v0", app, alias, prompt, payload)
}

// SignDigestAlg and ConfirmDigestAlg hash with SM3 for SM2 keys.
func SignDigestAlg(alg, app, alias, payload string) string {
	if alg == AlgSM2 {
		return digestSM3("phone-sign-v0", app, alias, payload)
	}
	return SignDigest(app, alias, payload)
}

func ConfirmDigestAlg(alg, app, alias, prompt, payload string) string {
	if alg == AlgSM2 {
		return digestSM3("phone-confirm-v0", app, alias, prompt, payload)
	}
	return ConfirmDigest(app, alias, prompt, payload)
}

// Roots are the certifier's root keys (root.pub): the Ed25519 one on the
// first line, the SM2 one on a line "sm2:<base64 point>". A root.pub
// without the second line verifies Ed25519 chains only.
type Roots struct {
	Ed25519 ed25519.PublicKey
	SM2     *gm.PublicKey
}

func ParseRoots(text string) (*Roots, error) {
	lines := strings.Fields(text)
	if len(lines) == 0 {
		return nil, errors.New("bad root public key")
	}
	pub, err := b64.DecodeString(lines[0])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("bad root public key")
	}
	r := &Roots{Ed25519: pub}
	for _, l := range lines[1:] {
		v, ok := strings.CutPrefix(l, "sm2:")
		if !ok {
			continue
		}
		raw, err := b64.DecodeString(v)
		if err != nil {
			return nil, errors.New("bad SM2 root public key")
		}
		if r.SM2, err = gm.ParsePublicKey(raw); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// String is the root.pub form.
func (r *Roots) String() string {
	s := b64.EncodeToString(r.Ed25519) + "\n"
	if r.SM2 != nil {
		s += "sm2:" + b64.EncodeToString(r.SM2.Bytes()) + "\n"
	}
	return s
}

func LoadRootPub(path string) (*Roots, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseRoots(string(b))
}

func verify(pubB64 string, msg []byte, sigB64 string) bool {
	pub, err1 := b64.DecodeString(pubB64)
	sig, err2 := b64.DecodeString(sigB64)
	return err1 == nil && err2 == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, msg, sig)
}

// verifyApp checks a signature made by a key of the given algorithm (an app
// key, or the device key of that algorithm's chain).
func verifyApp(alg, pubB64 string, msg []byte, sigB64 string) bool {
	if alg != AlgSM2 {
		return alg == "" && verify(pubB64, msg, sigB64)
	}
	raw, err1 := b64.DecodeString(pubB64)
	sig, err2 := b64.DecodeString(sigB64)
	if err1 != nil || err2 != nil {
		return false
	}
	pub, err := gm.ParsePublicKey(raw)
	return err == nil && gm.Verify(pub, gm.DefaultUID, msg, sig)
}

// verifyDevice checks the device key's certificate on the chain of the
// given algorithm: Ed25519 ("") or SM2.
func verifyDevice(roots *Roots, alg, deviceID, devicePub, deviceCert string) error {
	if roots == nil {
		return errors.New("no trusted root")
	}
	if alg == AlgSM2 {
		if roots.SM2 == nil {
			return errors.New("the trusted root has no SM2 key")
		}
		sig, err := b64.DecodeString(deviceCert)
		if err != nil || !gm.Verify(roots.SM2, gm.DefaultUID, DeviceCertMsgSM2(deviceID, devicePub), sig) {
			return errors.New("device SM2 certificate not issued by the trusted root")
		}
		return nil
	}
	sig, err := b64.DecodeString(deviceCert)
	if alg != "" || err != nil || !ed25519.Verify(roots.Ed25519, DeviceCertMsg(deviceID, devicePub), sig) {
		return errors.New("device certificate not issued by the trusted root")
	}
	return nil
}

func VerifyAttestation(roots *Roots, a *ui.Attestation, wantApp, wantNonce string) error {
	if err := verifyDevice(roots, a.Alg, a.DeviceID, a.DevicePub, a.DeviceCert); err != nil {
		return err
	}
	if a.AppID != wantApp || a.Nonce != wantNonce {
		return errors.New("attestation is for another app or nonce")
	}
	if !verifyApp(a.Alg, a.DevicePub, AttestMsg(a.DeviceID, a.AppID, a.Nonce, a.Boot), a.Sig) {
		return errors.New("attestation signature invalid")
	}
	return nil
}

// VerifyConfirm checks the full chain for a user-confirmed signature.
// For an SM2 key the whole chain is SM2: the device's SM2 key certifies
// the app key, the certifier's SM2 root the device key. A proof with Bio
// is checked against the biometric-bound key's certificate.
func VerifyConfirm(roots *Roots, p *ui.KeyProof, wantApp, alias, prompt, payload string) error {
	if err := verifyDevice(roots, p.Alg, p.DeviceID, p.DevicePub, p.DeviceCert); err != nil {
		return err
	}
	if p.AppID != wantApp || p.KeyAlias != alias {
		return fmt.Errorf("key belongs to %s/%s", p.AppID, p.KeyAlias)
	}
	cert := AppKeyCertMsgAlg(p.DeviceID, p.AppID, p.KeyAlias, p.Alg, true, p.AppPub)
	if p.Bio {
		cert = AppKeyCertMsgBio(p.DeviceID, p.AppID, p.KeyAlias, p.Alg, p.AppPub)
	}
	if !p.UserAuth || !verifyApp(p.Alg, p.DevicePub, cert, p.AppKeyCert) {
		return errors.New("key is not attested as user-auth-bound")
	}
	if want := ConfirmDigestAlg(p.Alg, wantApp, alias, prompt, payload); p.Digest != want {
		return errors.New("signed content differs from the order")
	}
	if !verifyApp(p.Alg, p.AppPub, []byte(p.Digest), p.Sig) {
		return errors.New("signature invalid")
	}
	return nil
}

func VerifySign(roots *Roots, p *ui.KeyProof, wantApp, alias, payload string) error {
	if err := verifyDevice(roots, p.Alg, p.DeviceID, p.DevicePub, p.DeviceCert); err != nil {
		return err
	}
	if p.AppID != wantApp || p.Digest != SignDigestAlg(p.Alg, wantApp, alias, payload) {
		return errors.New("signed content mismatch")
	}
	if !verifyApp(p.Alg, p.DevicePub, AppKeyCertMsgAlg(p.DeviceID, p.AppID, p.KeyAlias, p.Alg, p.UserAuth, p.AppPub), p.AppKeyCert) || !verifyApp(p.Alg, p.AppPub, []byte(p.Digest), p.Sig) {
		return errors.New("signature invalid")
	}
	return nil
}
