package shell

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeNfcd stands in for phone-nfcd: the state it answers names the cards
// of the last policy it took.
type fakeNfcd struct {
	mu    sync.Mutex
	cards []ui.NFCCardInfo
}

func (f *fakeNfcd) handle(method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case svc.NFCPolicy:
		var p svc.NFCPolicyArgs
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		f.cards = nil
		for _, cd := range p.Cards {
			f.cards = append(f.cards, ui.NFCCardInfo{ID: cd.App + "/" + cd.Card, Name: cd.Name, App: cd.App, AppName: cd.AppName})
		}
	case svc.NFCState:
		return ui.NFCState{Cards: f.cards}, nil
	}
	return map[string]bool{"ok": true}, nil
}

// The card pack opened right after the language changed lists the cards in
// the new language: what it asks nfcd waits for the policy in force.
func TestNFCStateFollowsTheLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.DefaultLang) })
	i18n.SetLang(i18n.SimplifiedChinese)
	wallet := &App{M: &Manifest{ID: "com.example.wallet", Name: "Wallet", Lang: i18n.English, Permissions: []string{ui.PermNFC},
		NFC: []ui.NFCCard{{ID: "transit", Category: ui.NFCOther, Description: "Transit card", AIDs: []string{"F0465154524E53"},
			Locales: map[string]string{i18n.Spanish: "Tarjeta de transporte"}}}}}
	lt := newLocTest(t, wallet)
	c := lt.c
	c.cfg.RunDir = t.TempDir()
	c.ev = make(chan func(), 512)
	c.broker.clients = map[string]*wire.Client{}
	f := &fakeNfcd{}
	if _, err := wire.ServeHub(filepath.Join(c.cfg.RunDir, svc.NfcdSock), f.handle); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.stopping.Store(true) })
	c.startNFC()
	lt.pump("the policy to reach nfcd", func() bool { return c.nfc.up && c.nfc.applied != "" && c.nfc.applied == c.nfc.sent })

	i18n.SetLang(i18n.Spanish)
	var st ui.NFCState
	done := false
	c.broker.dispatchNFC(wallet, ui.Msg{Cap: ui.CapNFCState}, func(v any, err error) {
		if err != nil {
			t.Error(err)
		}
		st, done = v.(ui.NFCState), true
	})
	lt.pump("the state", func() bool { return done })
	if len(st.Cards) != 1 || st.Cards[0].Name != "Tarjeta de transporte" {
		t.Fatalf("cards %+v, want the transit card named in Spanish", st.Cards)
	}
}

// A policy nfcd did not take goes again with the next syncNFC; what waited
// for it goes on, while one sent after it is still on its way.
func TestNFCPolicyNotTaken(t *testing.T) {
	c := &Core{}
	c.nfc.up, c.nfc.applied, c.nfc.sent = true, "a", "c"
	ran := 0
	c.nfc.fresh = []func(){func() { ran++ }}
	c.nfcApplied("b", errors.New("timeout"))
	if ran != 0 || c.nfc.sent != "c" {
		t.Fatalf("an older policy failed: ran %d, sent %q", ran, c.nfc.sent)
	}
	c.nfcApplied("c", errors.New("timeout"))
	if ran != 1 || c.nfc.sent != "" || c.nfc.fresh != nil {
		t.Fatalf("the last policy failed: ran %d, sent %q", ran, c.nfc.sent)
	}
	c.nfc.sent = "d"
	c.nfc.fresh = []func(){func() { ran++ }}
	c.nfcApplied("d", nil)
	if ran != 2 || c.nfc.applied != "d" {
		t.Fatalf("taken: ran %d, applied %q", ran, c.nfc.applied)
	}
}
