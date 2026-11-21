package nfcd

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

const wallet = "com.phone.wallet"

func (w *world) endedReasons() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, ev := range w.ended {
		out = append(out, ev.Session+" "+ev.App+" "+ev.Reason)
	}
	return out
}

func (w *world) waitEnded(t *testing.T, n int) []string {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if got := w.endedReasons(); len(got) >= n {
			return got
		}
	}
	t.Fatalf("ended sessions %v, want %d", w.endedReasons(), n)
	return nil
}

// Leaving "Add door card" stops the read at once: the next read gets the
// reader without waiting the 20 s out.
func TestCancelRead(t *testing.T) {
	w := newWorld(t, t.TempDir())
	done := make(chan ui.NFCState, 1)
	t0 := time.Now()
	go func() {
		done <- w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetRead, Seconds: 20}).(ui.NFCState)
	}()
	time.Sleep(100 * time.Millisecond)
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetCancelRead})
	select {
	case st := <-done:
		if st.Read == nil || st.Read.Result != ui.NFCReadNone {
			t.Fatalf("cancelled read: %+v", st.Read)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the read went on after cancel_read")
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("the cancelled read took %v", d)
	}
	if r := w.read("04A1B2C3", false, false); r.Result != ui.NFCReadOK {
		t.Fatalf("the read after: %+v", r)
	}
	// Nothing to cancel is no error.
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetCancelRead})
}

// An app reads a smart card's balance in a session of its own; nobody
// else reaches it, and it ends when the app closes it.
func TestAppReaderSession(t *testing.T) {
	w := newWorld(t, t.TempDir())
	aid, _ := hex.DecodeString(transitAID)
	id := w.must(svc.NFCReaderOpen, svc.NFCReaderArgs{App: wallet}).(map[string]string)["session"]
	if _, err := w.call(svc.NFCReaderOpen, svc.NFCReaderArgs{App: "com.example.other"}); err == nil {
		t.Fatal("a second session opened while one is open")
	}
	if r := w.read("04A1B2C3", false, false); r.Result != ui.NFCReadNone {
		t.Fatalf("the card pack read during an app's session: %+v", r)
	}
	w.sim.HoldApplet(&nfc.Tag{UID: []byte{0x04, 0xC0, 0xFF, 0xEE}, SAK: 0x20, ISODep: true}, nfc.TransitApplet{AID: aid, Balance: 1234})
	tag := w.must(svc.NFCReaderWait, svc.NFCReaderArgs{Session: id, App: wallet, Seconds: 1}).(ui.NFCTag)
	if tag.UID != "04C0FFEE" || !tag.ISODep || tag.SAK != "20" || tag.Session != id || tag.Until <= time.Now().UnixMilli() {
		t.Fatalf("tag %+v", tag)
	}
	ex := func(app, apdu string) (string, error) {
		out, err := w.call(svc.NFCReaderExchange, svc.NFCReaderArgs{Session: id, App: app, APDU: apdu})
		if err != nil {
			return "", err
		}
		return out.(ui.NFCTransceiveRes).APDU, nil
	}
	if r, err := ex(wallet, strings.ToUpper(hex.EncodeToString(nfc.Select(aid)))); err != nil || r != "9000" {
		t.Fatalf("select: %s %v", r, err)
	}
	r, err := ex(wallet, hex.EncodeToString(nfc.GetBalance))
	b, _ := hex.DecodeString(r)
	if err != nil || len(b) != 6 || binary.BigEndian.Uint32(b) != 1234 {
		t.Fatalf("balance: %s %v", r, err)
	}
	if _, err := ex("com.example.other", "805C000204"); err == nil {
		t.Fatal("another app reached the card")
	}
	if _, err := ex(wallet, "80"); err == nil {
		t.Fatal("a command of one byte went to the card")
	}
	w.must(svc.NFCReaderClose, svc.NFCReaderArgs{Session: id, App: wallet})
	if got := w.waitEnded(t, 1); got[0] != id+" "+wallet+" closed" {
		t.Fatalf("ended %v", got)
	}
	if _, err := ex(wallet, "805C000204"); err == nil {
		t.Fatal("a command went through a closed session")
	}
	// The reader is free again.
	if r := w.read("04A1B2C3", false, false); r.Result != ui.NFCReadOK {
		t.Fatalf("the card pack after the session: %+v", r)
	}
}

// A UID card has no session; a session waiting for a card stops at once
// when closed; a card taken away ends it; so does the time.
func TestAppReaderEnds(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.sim.Hold(&nfc.Tag{UID: []byte{0x04, 1, 2, 3}, SAK: 0x08})
	id := w.must(svc.NFCReaderOpen, svc.NFCReaderArgs{App: wallet}).(map[string]string)["session"]
	tag := w.must(svc.NFCReaderWait, svc.NFCReaderArgs{Session: id, App: wallet, Seconds: 1}).(ui.NFCTag)
	if tag.UID != "04010203" || tag.ISODep || tag.Session != "" {
		t.Fatalf("UID card: %+v", tag)
	}
	if got := w.waitEnded(t, 1); got[0] != id+" "+wallet+" done" {
		t.Fatalf("ended %v", got)
	}

	w.sim.Hold(nil)
	id = w.must(svc.NFCReaderOpen, svc.NFCReaderArgs{App: wallet}).(map[string]string)["session"]
	errs := make(chan error, 1)
	t0 := time.Now()
	go func() {
		_, err := w.call(svc.NFCReaderWait, svc.NFCReaderArgs{Session: id, App: wallet, Seconds: 20})
		errs <- err
	}()
	time.Sleep(100 * time.Millisecond)
	w.must(svc.NFCReaderClose, svc.NFCReaderArgs{Session: id, App: wallet})
	if err := <-errs; err == nil || err.Error() != ui.ErrCancelled || time.Since(t0) > 2*time.Second {
		t.Fatalf("closed while waiting: %v after %v", err, time.Since(t0))
	}
	if _, err := w.call(svc.NFCReaderWait, svc.NFCReaderArgs{Session: id, App: wallet, Seconds: 1}); err == nil || err.Error() != ui.ErrCancelled {
		t.Fatalf("a wait after the close: %v", err)
	}

	id = w.must(svc.NFCReaderOpen, svc.NFCReaderArgs{App: wallet}).(map[string]string)["session"]
	if _, err := w.call(svc.NFCReaderWait, svc.NFCReaderArgs{Session: id, App: wallet, Seconds: 1}); err == nil || err.Error() != ui.ErrNFCNoCard {
		t.Fatalf("no card: %v", err)
	}

	w.sim.HoldApplet(&nfc.Tag{UID: []byte{0x04, 9, 9, 9}, SAK: 0x20, ISODep: true}, nil)
	id = w.must(svc.NFCReaderOpen, svc.NFCReaderArgs{App: wallet}).(map[string]string)["session"]
	w.must(svc.NFCReaderWait, svc.NFCReaderArgs{Session: id, App: wallet, Seconds: 1})
	w.sim.Hold(nil)
	if _, err := w.call(svc.NFCReaderExchange, svc.NFCReaderArgs{Session: id, App: wallet, APDU: "805C000204"}); err == nil {
		t.Fatal("a card taken away answered")
	}
	if got := w.waitEnded(t, 4); got[3] != id+" "+wallet+" lost" {
		t.Fatalf("ended %v", got)
	}

	old := readerHold
	readerHold = 100 * time.Millisecond
	defer func() { readerHold = old }()
	w.sim.HoldApplet(&nfc.Tag{UID: []byte{0x04, 9, 9, 9}, SAK: 0x20, ISODep: true}, nil)
	id = w.must(svc.NFCReaderOpen, svc.NFCReaderArgs{App: wallet}).(map[string]string)["session"]
	w.must(svc.NFCReaderWait, svc.NFCReaderArgs{Session: id, App: wallet, Seconds: 1})
	if got := w.waitEnded(t, 5); got[4] != id+" "+wallet+" timeout" {
		t.Fatalf("ended %v", got)
	}
}

// twoAIDs selects the transit card, then the payment card's AID: the
// transit card's app hears it was deselected, the payment card's that the
// reader left.
type twoAIDs struct{}

func (twoAIDs) Loop() []nfc.PollingFrame {
	return []nfc.PollingFrame{{Type: nfc.FrameOn}, {Type: nfc.FrameA}, {Type: nfc.FrameB}, {Type: nfc.FrameOff}}
}

func (twoAIDs) Talk(c nfc.Link) (bool, string) {
	transit, _ := hex.DecodeString(transitAID)
	pay, _ := hex.DecodeString(payAID)
	for _, aid := range [][]byte{transit, pay} {
		if r, err := c.Transceive(nfc.Select(aid)); err != nil || nfc.SW(r) != 0x9000 {
			return false, "select"
		}
	}
	c.Halt()
	return true, "both"
}

func TestDeactivated(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(false, true, walletCards...)
	left := func(n int) []string {
		t.Helper()
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			w.mu.Lock()
			got := []string{}
			for _, ev := range w.left {
				got = append(got, ev.Session+" "+ev.App+" "+ev.Card+" "+ev.Reason)
			}
			w.mu.Unlock()
			if len(got) >= n {
				return got
			}
		}
		t.Fatalf("fewer than %d deactivations", n)
		return nil
	}
	w.tap(t, svc.NFCTapArgs{Kind: "gate", Name: "g", AID: transitAID, Fare: 100}, true, "Feiqian transit card")
	if got := left(1); len(got) != 1 || !strings.HasSuffix(got[0], " "+wallet+" transit "+ui.NFCDeactivatedLinkLoss) {
		t.Fatalf("after the gate: %v", got)
	}
	w.tap(t, svc.NFCTapArgs{Kind: "pay", Name: "till", AID: payAID, Amount: 100}, true, "Feiqian pay")
	if got := left(2); len(got) != 2 || !strings.HasSuffix(got[1], " pay "+ui.NFCDeactivatedLinkLoss) {
		t.Fatalf("after the till (PPSE, then the card: one selection): %v", got)
	}
	if r := w.sim.Tap(twoAIDs{}, 500*time.Millisecond); !r.OK {
		t.Fatalf("two AIDs: %+v", r)
	}
	got := left(4)
	if !strings.HasSuffix(got[2], " transit "+ui.NFCDeactivatedDeselected) || !strings.HasSuffix(got[3], " pay "+ui.NFCDeactivatedLinkLoss) {
		t.Fatalf("after selecting two cards: %v", got)
	}
	// A door card's tap tells no app anything.
	w.door("Home", "04A1B2C3")
	w.tap(t, svc.NFCTapArgs{Kind: "door", Name: "d", Accept: []string{"04A1B2C3"}}, true, "Home")
	time.Sleep(100 * time.Millisecond)
	if got := left(4); len(got) != 4 {
		t.Fatalf("after a door: %v", got)
	}
}
