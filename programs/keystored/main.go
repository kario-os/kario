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
	if *sensorName == "" {
		*sensorName = boardSensor(filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "phone", "sku.json"))
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
		ks.SetSensor(sensor)
		log.Printf("fingerprint sensor: %s", sensor.Name())
	}
	handle := func(method string, params json.RawMessage) (any, error) {
		var p struct {
			App, Key, Alg, Prompt, Payload, PIN, Nonce string
			Cert, Root                                 string
			CertSM2                                    string `json:"cert_sm2"`
			Rotate, On                                 bool
			BioKey                                     string `json:"bio_key"`
			Old, Token, Name, Finger                   string
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
		case keystore.MethodFPSimTouch:
			if sim == nil {
				return nil, errors.New("the fingerprint sensor is not simulated")
			}
			sim.Touch(p.Finger)
			return ok, nil
		}
		return nil, fmt.Errorf("unknown method %q", method)
	}
	log.Printf("state in %s", *stateDir)
	log.Fatal(wire.Serve(filepath.Join(*runDir, svc.KeystoredSock), handle))
}

// boardSensor is the fingerprint backend the board names in sku.json: a
// phone has no sensor until its driver exists (most need the vendor's TEE),
// the emulator a simulated one; no sku.json is a developer machine.
func boardSensor(path string) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "sim"
	}
	var sku struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err != nil || json.Unmarshal(raw, &sku) != nil || sku.Fingerprint == "" {
		return "none"
	}
	return sku.Fingerprint
}
