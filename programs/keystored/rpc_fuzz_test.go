package keystored

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/hal/fingerprint"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.KeyAttest, svc.KeyForget, svc.KeyCSR, svc.KeyInstallCert, svc.KeySetUnlocked, svc.KeySign,
	svc.KeyPushKey, svc.KeyPushOpen, svc.KeyConfirm, keystore.MethodConfirmBio,
	keystore.MethodLockStatus, keystore.MethodLockSetPIN, keystore.MethodLockClearPIN, keystore.MethodLockVerify,
	keystore.MethodParentStatus, keystore.MethodParentSetPIN, keystore.MethodParentClearPIN, keystore.MethodParentVerify,
	keystore.MethodParentResetPIN, keystore.MethodFPChallenge, keystore.MethodFPEnroll, keystore.MethodFPEnrollNext,
	keystore.MethodFPAuth, keystore.MethodFPCancel, keystore.MethodFPRename, keystore.MethodFPRemove,
	keystore.MethodSecretSeal, keystore.MethodSecretOpen, keystore.MethodFPSimTouch, keystore.MethodFPSimPlace}

// FuzzRPC is what the Shell writes on keystored.sock: app keys, the
// payment PIN, the lock-screen and parental PINs, fingerprints on the
// simulated sensor, and sealed secrets. Each input starts from the same
// freshly made store.
func FuzzRPC(f *testing.F) {
	pick := func(method string, params any) string { return rpcfuzz.Pick(rpcMethods, method, params) }
	const app = "com.example.bank"
	payload := base64.StdEncoding.EncodeToString([]byte("pay 12.00 to shop"))
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.KeyCSR, nil),
		rpcfuzz.Line(2, svc.KeyAttest, map[string]string{"app": app, "nonce": "n-1"}),
		rpcfuzz.Line(3, svc.KeyAttest, map[string]string{"app": app, "nonce": "n-2", "alg": keystore.AlgSM2}),
		rpcfuzz.Line(4, svc.KeySign, map[string]string{"app": app, "key": "login", "payload": payload}),
		rpcfuzz.Line(5, svc.KeySign, map[string]string{"app": app, "key": "login-sm2", "alg": keystore.AlgSM2, "payload": payload}),
		rpcfuzz.Line(6, svc.KeyConfirm, map[string]string{"app": app, "key": "pay", "prompt": "Pay 12.00", "payload": payload, "pin": keystore.DefaultPIN, "bio_key": "pay-bio"}),
		rpcfuzz.Line(7, svc.KeyConfirm, map[string]string{"app": app, "key": "pay", "prompt": "Pay 12.00", "payload": payload, "pin": "000000"}),
		rpcfuzz.Line(8, keystore.MethodConfirmBio, map[string]string{"app": app, "key": "pay-bio", "prompt": "Pay 12.00", "payload": payload, "token": "t"}),
		rpcfuzz.Line(9, svc.KeyForget, map[string]string{"app": app}))
	rpcfuzz.Seed(f,
		pick(svc.KeyPushKey, map[string]any{"app": app}),
		pick(svc.KeyPushKey, map[string]any{"app": app, "rotate": true}),
		pick(svc.KeyPushOpen, map[string]string{"app": app, "payload": "bm90IHNlYWxlZA=="}),
		pick(keystore.MethodSecretSeal, map[string]string{"purpose": "family:dev1", "data": base64.StdEncoding.EncodeToString([]byte("secret"))}),
		pick(keystore.MethodSecretOpen, map[string]string{"purpose": "family:dev1", "sealed": "AAAA"}),
		pick(svc.KeySetUnlocked, map[string]bool{"on": true}),
		pick(svc.KeyInstallCert, map[string]string{"cert": "x", "cert_sm2": "y", "root": "z"}))
	rpcfuzz.Seed(f,
		pick(keystore.MethodLockStatus, nil),
		pick(keystore.MethodLockSetPIN, map[string]string{"old": "", "pin": "2468"}),
		pick(keystore.MethodFPChallenge, nil),
		pick(keystore.MethodLockVerify, map[string]any{"pin": "2468", "challenge": 1}),
		pick(keystore.MethodFPEnroll, map[string]string{"token": "t", "name": "Thumb"}),
		pick(keystore.MethodFPSimPlace, map[string]string{"name": "ok-key"}),
		pick(keystore.MethodFPAuth, map[string]string{}),
		pick(keystore.MethodFPSimTouch, map[string]string{"finger": "thumb"}),
		pick(keystore.MethodFPEnrollNext, nil),
		pick(keystore.MethodFPCancel, nil),
		pick(keystore.MethodFPRename, map[string]any{"id": 1, "name": "Index"}),
		pick(keystore.MethodFPRemove, map[string]any{"token": "t", "id": 1}),
		pick(keystore.MethodLockClearPIN, map[string]string{"old": "2468"}))
	rpcfuzz.Seed(f,
		pick(keystore.MethodParentStatus, nil),
		pick(keystore.MethodParentSetPIN, map[string]string{"pin": "1357"}),
		pick(keystore.MethodParentVerify, map[string]string{"pin": "9999"}),
		pick(keystore.MethodParentResetPIN, map[string]string{"pin": "1111"}),
		pick(keystore.MethodParentClearPIN, map[string]string{"old": "1357"}))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"lock.verify","params":{"pin":"1","challenge":18446744073709551615}}`,
		`{"id":2,"method":"fp.rename","params":{"id":-9223372036854775808,"name":""}}`,
		`{"id":3,"method":"secret.seal","params":{"purpose":"","data":"!!"}}`,
		`{"id":4,"method":"sign","params":{"app":"","key":"","alg":"rsa","payload":""}}`)
	template := f.TempDir()
	if _, err := keystore.Open(template); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := filepath.Join(t.TempDir(), "state")
		if err := os.CopyFS(dir, os.DirFS(template)); err != nil {
			t.Fatal(err)
		}
		ks, err := keystore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		sim := fingerprint.NewSim()
		if err := ks.SetSensor(sim); err != nil {
			t.Fatal(err)
		}
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: handler(ks, sim), Cancel: ks.CancelSensor}, data)
	})
}
