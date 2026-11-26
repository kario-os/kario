package shell

import (
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// On a board without NFC phone-nfcd exits and leaves no socket: an app's
// NFC requests answer at once that the phone has none (Settings and Cards
// show it), not after the broker's dial retries with a socket error.
func TestNoNFCWithoutNfcd(t *testing.T) {
	c := snapCore(t)
	app := &App{M: &Manifest{ID: appCards, Name: "Cards", Permissions: []string{ui.PermSystem}}, running: true}
	for _, m := range []ui.Msg{req(ui.CapNFCState, nil), req(ui.CapNFCSet, ui.NFCSetArgs{Key: ui.NFCSetEnabled, On: true})} {
		var r answer
		t0 := time.Now()
		c.broker.dispatchNFC(app, m, r.reply)
		for r.n == 0 && time.Since(t0) < 5*time.Second {
			select {
			case f := <-c.Events():
				f()
			case <-time.After(10 * time.Millisecond):
			}
		}
		if d := time.Since(t0); r.err == nil || r.err.Error() != "this phone has no NFC" || d > time.Second {
			t.Errorf("%s: %v after %v", m.Cap, r.err, d)
		}
	}
}
