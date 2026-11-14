// Package keystore is the prototype of the hardware-backed keystore. Private
// keys never leave this service; apps receive signatures and certificate
// chains that a relying server can verify against the manufacturer root.
//
// Prototype simplifications (production in parentheses): Ed25519 (SM2 and
// ECDSA P-256), files under the state dir (OP-TEE trusted application), a
// root key generated on first boot (offline manufacturer HSM, device cert
// provisioned at the factory), PIN checked here (checked inside the TEE with
// the PIN entered through the trusted UI path).
package keystore

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/kario-os/kario/gm"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/ui"
)

const (
	DefaultPIN  = "123456"
	maxFailures = 5
	BootState   = "verified-boot:simulated"
	// BootUnlocked is what an unlocked device attests: the owner may have
	// replaced system components, so relying parties (payments) can refuse.
	BootUnlocked = "unlocked"
)

// UnlockedPath is the flag of an unlocked device (in production a fuse or
// the TEE's rollback-protected storage).
func UnlockedPath(stateDir string) string { return filepath.Join(stateDir, "keystore", "unlocked") }

// Unlocked: the owner unlocked the device (SetUnlocked).
func (s *Store) Unlocked() bool {
	_, err := os.Stat(UnlockedPath(s.dir))
	return err == nil
}

// SetUnlocked unlocks or locks the device again. Either way every app key
// (apps', mini-programs', push keys) is erased first, as Android does when
// the bootloader is unlocked: what was protected under one state is never
// usable under the other.
func (s *Store) SetUnlocked(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range []string{"apps", "push"} {
		if err := os.RemoveAll(filepath.Join(s.dir, "keystore", d)); err != nil {
			return err
		}
	}
	if on {
		return os.WriteFile(UnlockedPath(s.dir), []byte("unlocked by the owner\n"), 0o600)
	}
	if err := os.Remove(UnlockedPath(s.dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

var b64 = base64.StdEncoding

type Store struct {
	dir      string
	mu       sync.Mutex
	device   ed25519.PrivateKey
	deviceID string
	devCert  string
	// The SM2 chain: the device's SM2 key and its certificate from the
	// certifier's SM2 root (empty if the factory issued none).
	deviceSM2  *gm.PrivateKey
	devCertSM2 string
	// lk: the lock-screen PIN and the fingerprints (lock.go, biometric.go).
	lk lockState
}

type pinState struct {
	Salt     string `json:"salt"`
	Hash     string `json:"hash"`
	Failures int    `json:"failures"`
}

type appKey struct {
	Alg      string `json:"alg,omitempty"`
	Priv     string `json:"priv"`
	Pub      string `json:"pub"`
	Cert     string `json:"cert"`
	UserAuth bool   `json:"user_auth"`
	// Bio: the key signs only after a fingerprint match, and only while
	// the set of fingers is BioSet (biokey.go).
	Bio    bool   `json:"bio,omitempty"`
	BioSet string `json:"bio_set,omitempty"`
}

func RootPubPath(stateDir string) string { return filepath.Join(stateDir, "factory", "root.pub") }

func Open(stateDir string) (*Store, error) {
	s := &Store{dir: stateDir}
	// factory/ is world-traversable so sandboxed apps can read root.pub; the
	// private key inside stays 0600.
	factory := filepath.Join(stateDir, "factory")
	if err := os.MkdirAll(factory, 0o755); err != nil {
		return nil, err
	}
	if err := os.Chmod(factory, 0o755); err != nil {
		return nil, err
	}
	dev, err := loadOrCreateKey(filepath.Join(stateDir, "keystore", "device.key"))
	if err != nil {
		return nil, err
	}
	pub := dev.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	s.device = dev
	s.deviceID = "PH-" + hex.EncodeToString(sum[:6])
	if s.deviceSM2, err = loadOrCreateSM2(filepath.Join(stateDir, "keystore", "device-sm2.key")); err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(deviceCertPath(stateDir)); err == nil {
		// Provisioned at the factory: the certifier's roots signed this
		// device's certificates (InstallCert).
		s.devCert = string(trim(raw))
		if raw, err := os.ReadFile(deviceCertSM2Path(stateDir)); err == nil {
			s.devCertSM2 = string(trim(raw))
		}
	} else {
		// Development: roots made on this device certify it, so the chains
		// verify but prove nothing about the device (phone-provision
		// replaces them).
		root, err := loadOrCreateKey(filepath.Join(factory, "root.key"))
		if err != nil {
			return nil, err
		}
		rootSM2, err := loadOrCreateSM2(filepath.Join(factory, "root-sm2.key"))
		if err != nil {
			return nil, err
		}
		roots := &Roots{Ed25519: root.Public().(ed25519.PublicKey), SM2: &rootSM2.PublicKey}
		if err := os.WriteFile(RootPubPath(stateDir), []byte(roots.String()), 0o644); err != nil {
			return nil, err
		}
		s.devCert = SignDeviceCert(root, s.deviceID, b64.EncodeToString(pub))
		if s.devCertSM2, err = SignDeviceCertSM2(rootSM2, s.deviceID, s.devicePubSM2()); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(s.pinPath()); errors.Is(err, os.ErrNotExist) {
		if err := s.setPIN(DefaultPIN); err != nil {
			return nil, err
		}
	}
	if err := s.openLock(); err != nil {
		return nil, err
	}
	return s, nil
}

func deviceCertPath(stateDir string) string { return filepath.Join(stateDir, "factory", "device.cert") }

func deviceCertSM2Path(stateDir string) string {
	return filepath.Join(stateDir, "factory", "device-sm2.cert")
}

func (s *Store) devicePubSM2() string { return b64.EncodeToString(s.deviceSM2.PublicKey.Bytes()) }

// SignDeviceCertSM2 is SignDeviceCert for the device's SM2 key.
func SignDeviceCertSM2(ca *gm.PrivateKey, deviceID, devicePub string) (string, error) {
	sig, err := gm.Sign(rand.Reader, ca, gm.DefaultUID, DeviceCertMsgSM2(deviceID, devicePub))
	return b64.EncodeToString(sig), err
}

// SignDeviceCert is what the certifier's CA does for a device (factory
// provisioning, phone-provision): it certifies the device's public key under
// its ID.
func SignDeviceCert(ca ed25519.PrivateKey, deviceID, devicePub string) string {
	return b64.EncodeToString(ed25519.Sign(ca, DeviceCertMsg(deviceID, devicePub)))
}

// Provisioned: the device certificate came from the factory, not from a
// root made on the device.
func (s *Store) Provisioned() bool {
	_, err := os.Stat(deviceCertPath(s.dir))
	return err == nil
}

// CSR is what the device hands the factory station: its ID and public key
// (the private key never leaves the keystore).
func (s *Store) CSR() (deviceID, devicePub, devicePubSM2 string) {
	return s.deviceID, b64.EncodeToString(s.device.Public().(ed25519.PublicKey)), s.devicePubSM2()
}

// InstallCert installs the certificates the certifier's CA issued for this
// device, with that CA's roots (root.pub text), after checking that they
// certify this very device. With an SM2 root the SM2 certificate is
// required. The development roots, if any, are deleted. App keys stay
// valid: they are certified by the device keys, which do not change.
func (s *Store) InstallCert(cert, certSM2, rootsText string) error {
	roots, err := ParseRoots(rootsText)
	if err != nil {
		return err
	}
	id, pub, pubSM2 := s.CSR()
	if err := verifyDevice(roots, "", id, pub, cert); err != nil {
		return err
	}
	if roots.SM2 != nil || certSM2 != "" {
		if err := verifyDevice(roots, AlgSM2, id, pubSM2, certSM2); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.WriteFile(RootPubPath(s.dir), []byte(roots.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(deviceCertPath(s.dir), []byte(cert+"\n"), 0o644); err != nil {
		return err
	}
	if certSM2 != "" {
		if err := os.WriteFile(deviceCertSM2Path(s.dir), []byte(certSM2+"\n"), 0o644); err != nil {
			return err
		}
	}
	for _, k := range []string{"root.key", "root-sm2.key"} {
		if err := os.Remove(filepath.Join(s.dir, "factory", k)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	s.devCert, s.devCertSM2 = cert, certSM2
	return nil
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		seed, err := b64.DecodeString(string(trim(b)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("corrupt key %s", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return priv, os.WriteFile(path, []byte(b64.EncodeToString(priv.Seed())+"\n"), 0o600)
}

func loadOrCreateSM2(path string) (*gm.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		raw, err := b64.DecodeString(string(trim(b)))
		if err != nil {
			return nil, fmt.Errorf("corrupt key %s", path)
		}
		return gm.NewPrivateKey(raw)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	k, err := gm.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return k, os.WriteFile(path, []byte(b64.EncodeToString(k.Bytes())+"\n"), 0o600)
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (s *Store) pinPath() string { return filepath.Join(s.dir, "keystore", "pin.json") }

func (s *Store) setPIN(pin string) error {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	st := pinState{Salt: hex.EncodeToString(salt), Hash: pinHash(salt, pin)}
	return writeJSON(s.pinPath(), st)
}

func pinHash(salt []byte, pin string) string {
	h := sha256.Sum256(append(append([]byte{}, salt...), pin...))
	return hex.EncodeToString(h[:])
}

func writeJSON(path string, v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// safeName is an app ID or key alias: one path element, never "." or "..".
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// MaxOrigin bounds a mini-program's name inside an identity.
const MaxOrigin = 64

// keyDir is where an identity's keys live. An identity is an app ID, or
// "app/origin" for a mini-program hosted by the app: its keys are its own
// (a host cannot sign as its mini-programs, nor they as the host or each
// other) and sit under the host's directory, so they go with the host.
func (s *Store) keyDir(identity string) (string, error) {
	app, origin, hosted := strings.Cut(identity, "/")
	if !safeName.MatchString(app) {
		return "", errors.New("invalid app id")
	}
	dir := filepath.Join(s.dir, "keystore", "apps", app)
	if !hosted {
		return dir, nil
	}
	if origin == "" || utf8.RuneCountInString(origin) > MaxOrigin || !utf8.ValidString(origin) ||
		strings.ContainsFunc(origin, func(r rune) bool { return r == '/' || unicode.IsControl(r) }) {
		return "", errors.New("invalid mini-program origin")
	}
	sum := sha256.Sum256([]byte(origin))
	return filepath.Join(dir, "origin-"+hex.EncodeToString(sum[:8])), nil
}

// Forget deletes every key of an app: its own, its mini-programs' and its
// push key. Called on uninstall, so an app later installed under the same
// ID (possibly from another developer) starts without them.
func (s *Store) Forget(app string) error {
	if !safeName.MatchString(app) {
		return errors.New("invalid app id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(s.dir, "keystore", "apps", app)); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(s.dir, "keystore", "push", app+".key"), filepath.Join(s.dir, "keystore", "push", app+".prev.key")} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

type signer func(msg []byte) (string, error)

func (s *Store) key(app, alias string, userAuth bool, alg string) (signer, *appKey, error) {
	if alg != "" && alg != AlgSM2 {
		return nil, nil, fmt.Errorf("unknown key algorithm %q", alg)
	}
	dir, err := s.keyDir(app)
	if err != nil {
		return nil, nil, err
	}
	if !safeName.MatchString(alias) {
		return nil, nil, errors.New("invalid key alias")
	}
	path := filepath.Join(dir, alias+".json")
	var k appKey
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &k); err != nil {
			return nil, nil, err
		}
		if k.Bio {
			return nil, nil, fmt.Errorf("key %q is bound to the fingerprints", alias)
		}
		if k.UserAuth != userAuth {
			return nil, nil, fmt.Errorf("key %q exists with user_auth=%v", alias, k.UserAuth)
		}
		if k.Alg != alg {
			return nil, nil, fmt.Errorf("key %q exists with algorithm %q", alias, algName(k.Alg))
		}
	} else {
		k, err = s.newKey(alg, func(pub string) []byte { return AppKeyCertMsgAlg(s.deviceID, app, alias, alg, userAuth, pub) })
		if err != nil {
			return nil, nil, err
		}
		k.UserAuth = userAuth
		if err := writeJSON(path, k); err != nil {
			return nil, nil, err
		}
	}
	sign, err := k.signer()
	return sign, &k, err
}

// newKey makes an app key of the algorithm, certified by the device key of
// that algorithm's chain over certMsg(its public half).
func (s *Store) newKey(alg string, certMsg func(pub string) []byte) (appKey, error) {
	k := appKey{Alg: alg}
	if alg == AlgSM2 {
		if s.devCertSM2 == "" {
			return k, errors.New("this device has no SM2 certificate")
		}
		priv, err := gm.GenerateKey(rand.Reader)
		if err != nil {
			return k, err
		}
		k.Priv, k.Pub = b64.EncodeToString(priv.Bytes()), b64.EncodeToString(priv.PublicKey.Bytes())
		sig, err := gm.Sign(rand.Reader, s.deviceSM2, gm.DefaultUID, certMsg(k.Pub))
		if err != nil {
			return k, err
		}
		k.Cert = b64.EncodeToString(sig)
		return k, nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return k, err
	}
	k.Priv, k.Pub = b64.EncodeToString(priv.Seed()), b64.EncodeToString(pub)
	k.Cert = b64.EncodeToString(ed25519.Sign(s.device, certMsg(k.Pub)))
	return k, nil
}

func (k *appKey) signer() (signer, error) {
	raw, err := b64.DecodeString(k.Priv)
	if err != nil {
		return nil, err
	}
	if k.Alg == AlgSM2 {
		priv, err := gm.NewPrivateKey(raw)
		if err != nil {
			return nil, err
		}
		return func(msg []byte) (string, error) {
			sig, err := gm.Sign(rand.Reader, priv, gm.DefaultUID, msg)
			return b64.EncodeToString(sig), err
		}, nil
	}
	priv := ed25519.NewKeyFromSeed(raw)
	return func(msg []byte) (string, error) { return b64.EncodeToString(ed25519.Sign(priv, msg)), nil }, nil
}

func algName(alg string) string {
	if alg == "" {
		return "ed25519"
	}
	return alg
}

// PushKey is the public half of the app's end-to-end push key (X25519),
// created on first use; the private half never leaves the keystore (in
// production the TEE), which opens sealed push data for the app instead.
func (s *Store) PushKey(app string) (string, error) {
	k, err := s.pushKey(app)
	if err != nil {
		return "", err
	}
	return push.PublicKey(k), nil
}

// RotatePush replaces the app's push key (an app rotates it when its server
// asks, or after a suspected leak) and returns the new public half. The
// previous key still opens what was sealed before the server learnt the new
// one; the one before it is gone, and so is whatever was sealed to it.
func (s *Store) RotatePush(app string) (string, error) {
	cur, err := s.pushPath(app)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(cur); err == nil {
		if err := os.Rename(cur, prevKey(cur)); err != nil {
			return "", err
		}
	}
	k, err := push.KeyFile(cur)
	if err != nil {
		return "", err
	}
	return push.PublicKey(k), nil
}

// OpenPush decrypts push data sealed to the app's key, or to the key it
// replaced.
func (s *Store) OpenPush(app, sealed string) ([]byte, error) {
	k, err := s.pushKey(app)
	if err != nil {
		return nil, err
	}
	plain, err := push.Open(k, sealed)
	if err == nil {
		return plain, nil
	}
	cur, _ := s.pushPath(app)
	if raw, rerr := os.ReadFile(prevKey(cur)); rerr == nil {
		if prev, perr := ecdh.X25519().NewPrivateKey(raw); perr == nil {
			if p, oerr := push.Open(prev, sealed); oerr == nil {
				return p, nil
			}
		}
	}
	return nil, err
}

func prevKey(cur string) string { return strings.TrimSuffix(cur, ".key") + ".prev.key" }

func (s *Store) pushPath(app string) (string, error) {
	if !safeName.MatchString(app) {
		return "", errors.New("invalid app id")
	}
	return filepath.Join(s.dir, "keystore", "push", app+".key"), nil
}

func (s *Store) pushKey(app string) (*ecdh.PrivateKey, error) {
	p, err := s.pushPath(app)
	if err != nil {
		return nil, err
	}
	return push.KeyFile(p)
}

func (s *Store) proof(app, alias string, sign signer, k *appKey, digest string) (*ui.KeyProof, error) {
	sig, err := sign([]byte(digest))
	if err != nil {
		return nil, err
	}
	devPub, devCert := s.deviceChain(k.Alg)
	return &ui.KeyProof{
		Alg: k.Alg, Digest: digest, Sig: sig,
		AppID: app, KeyAlias: alias, AppPub: k.Pub, AppKeyCert: k.Cert, UserAuth: k.UserAuth, Bio: k.Bio,
		DeviceID: s.deviceID, DevicePub: devPub, DeviceCert: devCert,
	}, nil
}

// deviceChain is the device key and certificate of an algorithm's chain.
func (s *Store) deviceChain(alg string) (pub, cert string) {
	if alg == AlgSM2 {
		return s.devicePubSM2(), s.devCertSM2
	}
	return b64.EncodeToString(s.device.Public().(ed25519.PublicKey)), s.devCert
}

// Sign signs with an app-scoped key that does not require user presence
// (e.g. rotating payment codes).
func (s *Store) Sign(app, alias, alg, payload string) (*ui.KeyProof, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sign, k, err := s.key(app, alias, false, alg)
	if err != nil {
		return nil, err
	}
	return s.proof(app, alias, sign, k, SignDigestAlg(alg, app, alias, payload))
}

var ErrWrongPIN = errors.New("wrong_pin")
var ErrLocked = errors.New("locked")

// Confirm verifies the PIN collected by the trusted UI and signs the prompt and
// payload with a user-auth-bound key.
func (s *Store) Confirm(app, alias, alg, prompt, payload, pin string) (*ui.KeyProof, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st pinState
	b, err := os.ReadFile(s.pinPath())
	if err != nil {
		return nil, 0, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, 0, err
	}
	if st.Failures >= maxFailures {
		return nil, 0, ErrLocked
	}
	salt, _ := hex.DecodeString(st.Salt)
	if pinHash(salt, pin) != st.Hash {
		st.Failures++
		_ = writeJSON(s.pinPath(), st)
		return nil, maxFailures - st.Failures, ErrWrongPIN
	}
	if st.Failures != 0 {
		st.Failures = 0
		_ = writeJSON(s.pinPath(), st)
	}
	sign, k, err := s.key(app, alias, true, alg)
	if err != nil {
		return nil, 0, err
	}
	p, err := s.proof(app, alias, sign, k, ConfirmDigestAlg(alg, app, alias, prompt, payload))
	return p, maxFailures, err
}

// Attest vouches for the device and its boot state to the app's server, on
// the Ed25519 chain or (alg "sm2") the SM2 one.
func (s *Store) Attest(app, nonce, alg string) (*ui.Attestation, error) {
	if _, err := s.keyDir(app); err != nil {
		return nil, err
	}
	if alg != "" && alg != AlgSM2 {
		return nil, fmt.Errorf("unknown key algorithm %q", alg)
	}
	boot := BootState
	if s.Unlocked() {
		boot = BootUnlocked
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pub, cert := s.deviceChain(alg)
	msg := AttestMsg(s.deviceID, app, nonce, boot)
	var sig string
	if alg == AlgSM2 {
		if cert == "" {
			return nil, errors.New("this device has no SM2 certificate")
		}
		raw, err := gm.Sign(rand.Reader, s.deviceSM2, gm.DefaultUID, msg)
		if err != nil {
			return nil, err
		}
		sig = b64.EncodeToString(raw)
	} else {
		sig = b64.EncodeToString(ed25519.Sign(s.device, msg))
	}
	return &ui.Attestation{Alg: alg, DeviceID: s.deviceID, DevicePub: pub, DeviceCert: cert, AppID: app, Nonce: nonce, Boot: boot, Sig: sig}, nil
}
