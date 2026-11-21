package shell

import (
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// A store app's call or text waits for the user, whatever it declared, and
// the user sees whom it goes to and what it says.
func TestStoreAppsConfirmCallsAndTexts(t *testing.T) {
	prank := &App{M: &Manifest{ID: "com.example.prank", Name: "Prank", Permissions: []string{ui.PermPhone, ui.PermSMS}, installed: true}, running: true}
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir()}, apps: []*App{prank}}
	b := &broker{c: c}
	replied := 0
	for _, m := range []ui.Msg{
		{T: ui.MsgReq, Cap: ui.CapTelDial, Args: json.RawMessage(`{"number":"16900000000"}`)},
		{T: ui.MsgReq, Cap: ui.CapSMSSend, Args: json.RawMessage(`{"to":"10690000","text":"TD unsubscribe"}`)},
	} {
		b.dispatchPhone(prank, m, func(any, error) { replied++ })
	}
	if replied != 0 || len(c.overlays) != 2 {
		t.Fatalf("%d replies, %d confirmations", replied, len(c.overlays))
	}
	want := []string{tr.T("“%s” asks to call %s", "Prank", "16900000000"), tr.T("“%s” wants to text %s: %s", "Prank", "10690000", "TD unsubscribe")}
	for i, o := range c.overlays {
		if co, ok := o.(*confirmOverlay); !ok || !strings.Contains(co.text, want[i]) {
			t.Fatalf("confirmation %d: %+v", i, o)
		}
	}
	c.overlays[1].(*confirmOverlay).onNo()
	if replied != 1 {
		t.Fatal("no answer after the user said no")
	}
}
