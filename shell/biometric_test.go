package shell

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/hal/fingerprint"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// A sensor in the OK key reads a finger only in a system prompt: it never
// unlocks, the lock screen does not offer it, prompts ask for a press of OK,
// and a prompt says when no TEE keeps the key.
func TestFingerprintInTheOKKey(t *testing.T) {
	c, _ := lockCore(t)
	c.lock.pin, c.lock.prefs.FingerUnlock = true, true
	c.lock.st = keystore.LockStatus{PIN: true, Sensor: "simulated", Fingers: []keystore.Finger{{ID: 1, Name: "Thumb"}},
		Security: keystore.Security}
	if !c.fingerUnlockReady() || !strings.Contains(c.lockHint(), tr.T("or touch the fingerprint sensor")) {
		t.Fatalf("a sensor of its own: ready %v, hint %q", c.fingerUnlockReady(), c.lockHint())
	}
	if c.fingerTouchText() != tr.T("Touch the fingerprint sensor to confirm, or enter the payment PIN") {
		t.Fatalf("a sensor of its own asks %q", c.fingerTouchText())
	}

	c.lock.st.Place = ui.FingerprintOKKey
	c.lockNow("test")
	if c.fingerUnlockReady() || c.lock.armed {
		t.Fatal("the OK key's sensor unlocks")
	}
	if h := c.lockHint(); h != tr.T("OK: enter the PIN") {
		t.Fatalf("hint %q", h)
	}
	c.Key(Named(KeyEnter))
	pad, ok := overlayOf[*lockPadOverlay](c)
	if !ok {
		t.Fatal("OK did not open the PIN pad")
	}
	if lines, _, _ := pad.lines(c); slices.Contains(lines, tr.T("A fingerprint unlocks it too.")) {
		t.Fatalf("the PIN pad offers the finger: %q", lines)
	}

	o := &authOverlay{bio: true, bioAsk: c.fingerTouchText(), bioStore: c.keyStoreText()}
	want := []string{tr.T("Rest an enrolled finger on the OK key to confirm (no need to press it), or enter the payment PIN"),
		tr.T("This phone has no secure hardware (TEE): the system's key service keeps the key and matches the finger.")}
	if got := o.bioLines(); !slices.Equal(got, want) {
		t.Fatalf("the payment prompt says %q", got)
	}
	c.lock.st.Security = ui.SecurityTEE
	if s := c.keyStoreText(); s != "" {
		t.Fatalf("with a TEE: %q", s)
	}
	if lines := (&enrolOverlay{okKey: true, steps: 5}).lines(); lines[0] != tr.T("Rest the same finger on the OK key, lifting it between touches, so all of it is read; no need to press.") {
		t.Fatalf("enrolling: %q", lines)
	}

	var got error
	if !c.securitySetting(ui.SettingArgs{Key: ui.SettingFingerprintUnlock, Value: 1}, func(err error) { got = err }) || got == nil {
		t.Fatalf("turning unlocking on for the OK key's sensor: %v", got)
	}
}

// keyRec is whatever a system prompt covers, as far as keys go.
type keyRec struct{ keys []string }

func (r *keyRec) render(*Core, *cv.Canvas)  {}
func (r *keyRec) frame(*Core) *FrameOverlay { return &FrameOverlay{} }
func (r *keyRec) key(_ *Core, k Key)        { r.keys = append(r.keys, k.Name) }

// okKeyCore is the Shell with phone-keystored over its socket: a real store
// with the simulated sensor in the OK key, the lock-screen PIN, the thumb
// enrolled and the wallet's fingerprint-bound key.
func okKeyCore(t *testing.T) (*locTest, *fingerprint.Sim) {
	lt, _ := stCore(t)
	c := lt.c
	c.cfg.RunDir = t.TempDir()
	c.broker.clients = map[string]*wire.Client{}
	ks, err := keystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sim := fingerprint.NewSim()
	if err := errors.Join(ks.SetSensor(sim), ks.SetSensorPlace(ui.FingerprintOKKey)); err != nil {
		t.Fatal(err)
	}
	press := func(finger string) {
		go func() {
			for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !sim.Touch(finger); time.Sleep(time.Millisecond) {
			}
		}()
	}
	if _, err := ks.SetLockPIN("", "2468"); err != nil {
		t.Fatal(err)
	}
	ch, _ := ks.EnrollChallenge()
	pin, err := ks.VerifyLockPIN("2468", ch)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := ks.EnrollBegin(pin.Token, "Thumb")
	for i := 0; err == nil && i < steps; i++ {
		press("thumb")
		_, err = ks.EnrollNext(context.Background())
	}
	if err = errors.Join(err, ks.EnsureBioKey("com.phone.wallet", "pay-bio", "")); err != nil {
		t.Fatal(err)
	}
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ App, Key, Alg, Prompt, Payload, Token, Finger string }
		_ = json.Unmarshal(params, &p)
		switch method {
		case keystore.MethodLockStatus:
			return ks.LockStatus(), nil
		case keystore.MethodFPAuth:
			return ks.Authenticate(context.Background(), p.App, p.Key)
		case keystore.MethodFPCancel:
			ks.CancelSensor()
			return map[string]bool{}, nil
		case keystore.MethodConfirmBio:
			return ks.ConfirmBio(p.App, p.Key, p.Alg, p.Prompt, p.Payload, p.Token)
		case keystore.MethodFPSimTouch:
			return map[string]bool{"read": sim.Touch(p.Finger)}, nil
		}
		return nil, errors.New("unknown method " + method)
	})
	c.lock.prefs.FingerPay = true
	c.lockRefresh(nil)
	lt.pump("lock.status", func() bool { return c.lock.st.Place == ui.FingerprintOKKey && len(c.lock.st.Fingers) == 1 })
	return lt, sim
}

// The sensor under the OK key's cap reads a finger resting on it, without a
// press, and only while a system prompt asks for one; OK stays a key. A
// touch on the lock screen does nothing; in the payment prompt OK alone
// confirms nothing, the touch confirms, and the press that came with it is
// the system's, not the screen's the prompt uncovered.
func TestOKKeyReadsATouchNotAPress(t *testing.T) {
	lt, sim := okKeyCore(t)
	c := lt.c
	c.lockNow("test")
	if err := c.TouchFinger("thumb"); err != nil {
		t.Fatal(err)
	}
	if _, ok := overlayOf[*lockPadOverlay](c); ok || !c.secureLocked() || sim.Powered() {
		t.Fatalf("a touch on the lock screen: pad %v, locked %v, powered %v", ok, c.secureLocked(), sim.Powered())
	}
	c.Key(Named(KeyEnter))
	if _, ok := overlayOf[*lockPadOverlay](c); !ok {
		t.Fatal("OK on the lock screen did not open the PIN pad")
	}
	c.Key(Named(KeyEsc))
	c.unlockNow("test")

	wallet := &App{M: &Manifest{ID: "com.phone.wallet", Name: "Feiqian"}}
	args := ui.AuthConfirmArgs{Key: "pay", BioKey: "pay-bio", Prompt: "Pay ¥18.00", Payload: "{}"}
	var proof ui.KeyProof
	answered := false
	under := &keyRec{}
	c.pushOverlay(under)
	o := &authOverlay{app: wallet, prompt: args.Prompt, started: time.Now(), cancel: func() {}, submit: func(string) {}}
	c.pushOverlay(o)
	c.startBioConfirm(o, wallet.M.ID, args, func(v any, err error) {
		answered = err == nil
		proof, _ = v.(ui.KeyProof)
	})
	lt.pump("200 ms into the prompt", func() bool { return time.Since(c.overlayShown[o]) > 200*time.Millisecond })
	if sim.Powered() {
		t.Fatal("the sensor listens before the prompt takes answers")
	}
	lt.pump("the sensor on for the prompt", sim.Powered)
	c.Key(Runes("12"))
	c.Key(Named(KeyEnter))
	if !c.showing(o) || answered || o.err != tr.T("Enter all %d digits of the payment PIN", pinLen) {
		t.Fatalf("OK in the prompt: showing %v, answered %v, %q", c.showing(o), answered, o.err)
	}
	if err := c.TouchFinger("thumb"); err != nil {
		t.Fatal(err)
	}
	lt.pump("the finger's answer", func() bool { return answered })
	if !proof.Bio || c.showing(o) {
		t.Fatalf("proof %+v, prompt showing %v", proof, c.showing(o))
	}
	c.Key(Named(KeyEnter))
	if len(under.keys) != 0 {
		t.Fatalf("the press that came with the touch reached the screen under the prompt: %q", under.keys)
	}
	c.Key(Named(KeyEnter))
	if !slices.Equal(under.keys, []string{KeyEnter}) {
		t.Fatalf("the next OK: %q", under.keys)
	}
	lt.pump("the sensor off after the prompt", func() bool { return !sim.Powered() })
}
