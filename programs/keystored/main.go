// phone-keystored owns device and app keys. Only the shell (broker) talks to
// it; the shell supplies the caller's app id, which apps cannot forge because
// it is bound to the socket the shell handed to the app process.
package keystored

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/kario-os/kario/hal/fingerprint"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	runDir := flag.String("run-dir", "/run/phone", "directory for service sockets")
	stateDir := flag.String("state-dir", "/var/lib/phone", "persistent state")
	exe, _ := os.Executable()
	sensorName := flag.String("fingerprint", "", `fingerprint sensor backend: "sim" (simulated) or "none"; by default the board's (share/phone/sku.json), simulated without one (a developer machine)`)
	flag.Parse()
	skuPath := filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "phone", "sku.json")
	board := readSKU(skuPath)
	if *sensorName == "" {
		*sensorName = board.sensor()
	}
	log.SetPrefix("phone-keystored: ")
	if err := os.MkdirAll(*runDir, 0o755); err != nil {
		log.Fatal(err)
	}
	ks, err := keystore.Open(*stateDir)
	if err != nil {
		log.Fatal(err)
	}
	sensor, err := fingerprint.Open(*sensorName)
	if err != nil {
		log.Printf("fingerprint sensor %q: %v; no fingerprints", *sensorName, err)
	}
	sim, _ := sensor.(*fingerprint.Sim)
	if sensor != nil {
		if err := ks.SetSensor(sensor); err != nil {
			log.Printf("fingerprint sensor: %v", err)
		}
		if err := ks.SetSensorPlace(board.FingerprintAt); err != nil {
			log.Printf("fingerprint sensor: %v", err)
		}
		log.Printf("fingerprint sensor: %s %s", sensor.Name(), board.FingerprintAt)
	}
	log.Printf("state in %s", *stateDir)
	log.Fatal(wire.Serve(filepath.Join(*runDir, svc.KeystoredSock), handler(ks, sim)))
}

// handler answers keystored.sock; sim is the sensor when it is simulated.
func handler(ks *keystore.Store, sim *fingerprint.Sim) wire.Handler {
	return func(method string, params json.RawMessage) (any, error) {
		var p struct {
			App, Key, Alg, Prompt, Payload, PIN, Nonce string
			Cert, Root                                 string
			CertSM2                                    string `json:"cert_sm2"`
			Rotate, On                                 bool
			BioKey                                     string `json:"bio_key"`
			Old, Token, Name, Finger                   string
			Purpose, Data, Sealed                      string
			Challenge                                  uint64
			ID                                         int
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		ok := map[string]bool{"ok": true}
		switch method {
		case svc.KeyAttest:
			return ks.Attest(p.App, p.Nonce, p.Alg)
		case svc.KeyForget:
			return ok, ks.Forget(p.App)
		case svc.KeyCSR:
			id, pub, pubSM2 := ks.CSR()
			return map[string]any{"device_id": id, "device_pub": pub, "device_pub_sm2": pubSM2, "provisioned": ks.Provisioned()}, nil
		case svc.KeyInstallCert:
			return ok, ks.InstallCert(p.Cert, p.CertSM2, p.Root)
		case svc.KeySetUnlocked:
			return map[string]bool{"unlocked": p.On}, ks.SetUnlocked(p.On)
		case svc.KeySign:
			return ks.Sign(p.App, p.Key, p.Alg, p.Payload)
		case svc.KeyPushKey:
			if p.Rotate {
				k, err := ks.RotatePush(p.App)
				return map[string]string{"key": k}, err
			}
			k, err := ks.PushKey(p.App)
			return map[string]string{"key": k}, err
		case svc.KeyPushOpen:
			plain, err := ks.OpenPush(p.App, p.Payload)
			return map[string]string{"data": base64.StdEncoding.EncodeToString(plain)}, err
		case svc.KeyConfirm:
			proof, left, err := ks.Confirm(p.App, p.Key, p.Alg, p.Prompt, p.Payload, p.PIN)
			if errors.Is(err, keystore.ErrWrongPIN) {
				return nil, fmt.Errorf("wrong_pin:%d", left)
			}
			if err == nil && p.BioKey != "" {
				// The payment PIN just confirmed: the biometric key may be
				// made (again) now, and only now.
				if berr := ks.EnsureBioKey(p.App, p.BioKey, p.Alg); berr != nil {
					log.Printf("%s: biometric key %s: %v", p.App, p.BioKey, berr)
				}
			}
			return proof, err
		case keystore.MethodConfirmBio:
			return ks.ConfirmBio(p.App, p.Key, p.Alg, p.Prompt, p.Payload, p.Token)
		case keystore.MethodLockStatus:
			return ks.LockStatus(), nil
		case keystore.MethodLockSetPIN:
			r, err := ks.SetLockPIN(p.Old, p.PIN)
			if r.OK {
				log.Printf("lock-screen PIN set")
			}
			return r, err
		case keystore.MethodLockClearPIN:
			r, err := ks.ClearLockPIN(p.Old)
			if r.OK {
				log.Printf("lock-screen PIN removed, and the fingerprints with it")
			}
			return r, err
		case keystore.MethodLockVerify:
			r, err := ks.VerifyLockPIN(p.PIN, p.Challenge)
			if err == nil && !r.OK {
				log.Printf("lock-screen PIN wrong (%d in a row)", r.Failures)
			}
			return r, err
		case keystore.MethodParentStatus:
			return ks.ParentStatus(), nil
		case keystore.MethodParentSetPIN:
			r, err := ks.SetParentPIN(p.Old, p.PIN)
			if r.OK {
				log.Printf("parental PIN set")
			}
			return r, err
		case keystore.MethodParentClearPIN:
			r, err := ks.ClearParentPIN(p.Old)
			if r.OK {
				log.Printf("parental PIN removed")
			}
			return r, err
		case keystore.MethodParentVerify:
			r, err := ks.VerifyParentPIN(p.PIN)
			if err == nil && !r.OK {
				log.Printf("parental PIN wrong (%d in a row)", r.Failures)
			}
			return r, err
		case keystore.MethodParentResetPIN:
			r, err := ks.ResetParentPIN(p.PIN)
			if r.OK {
				log.Printf("parental PIN reset through the parent's account")
			}
			return r, err
		case keystore.MethodFPChallenge:
			c, err := ks.EnrollChallenge()
			return map[string]uint64{"challenge": c}, err
		case keystore.MethodFPEnroll:
			steps, err := ks.EnrollBegin(p.Token, p.Name)
			return map[string]int{"steps": steps}, err
		case keystore.MethodFPEnrollNext:
			st, err := ks.EnrollNext(context.Background())
			if st.Finger != nil {
				log.Printf("fingerprint %d enrolled", st.Finger.ID)
			}
			return st, err
		case keystore.MethodFPAuth:
			r, err := ks.Authenticate(context.Background(), p.App, p.Key)
			if r.Disabled {
				log.Printf("fingerprints off until the PIN: %d failed matches", r.Failures)
			}
			return r, err
		case keystore.MethodFPCancel:
			ks.CancelSensor()
			return ok, nil
		case keystore.MethodFPRename:
			return ok, ks.RenameFinger(p.ID, p.Name)
		case keystore.MethodFPRemove:
			err := ks.RemoveFinger(p.Token, p.ID)
			if err == nil {
				log.Printf("fingerprint %d removed", p.ID)
			}
			return ok, err
		case keystore.MethodSecretSeal:
			plain, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				return nil, err
			}
			sealed, err := ks.SealSecret(p.Purpose, plain)
			return map[string]string{"sealed": sealed}, err
		case keystore.MethodSecretOpen:
			plain, err := ks.OpenSecret(p.Purpose, p.Sealed)
			return map[string]string{"data": base64.StdEncoding.EncodeToString(plain)}, err
		case keystore.MethodFPSimTouch:
			if sim == nil {
				return nil, errors.New("the fingerprint sensor is not simulated")
			}
			return map[string]bool{"read": sim.Touch(p.Finger)}, nil
		case keystore.MethodFPSimPlace:
			if sim == nil {
				return nil, errors.New("the fingerprint sensor is not simulated")
			}
			return ok, ks.SetSensorPlace(p.Name)
		}
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

// skuSensor is what sku.json says of the fingerprint sensor: the board's
// backend, and where the keypad has it (FingerprintAt).
type skuSensor struct {
	missing, bad  bool
	Fingerprint   string `json:"fingerprint"`
	FingerprintAt string `json:"fingerprint_at"`
}

func readSKU(path string) skuSensor {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return skuSensor{missing: true}
	}
	var s skuSensor
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return skuSensor{bad: true}
	}
	return s
}

// sensor is the fingerprint backend the board names: a phone has no sensor
// until its driver exists (most need the vendor's TEE), the emulator a
// simulated one; no sku.json is a developer machine.
func (s skuSensor) sensor() string {
	switch {
	case s.missing:
		return "sim"
	case s.bad || s.Fingerprint == "":
		return "none"
	}
	return s.Fingerprint
}
