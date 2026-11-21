package main

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/ui"
)

// The wallet's side of the simplified flows, against the simulated
// readers' commands (hal/nfc).
func TestTransitCard(t *testing.T) {
	transit = &transitCard{Balance: 300}
	aid, _ := hex.DecodeString(transitAID)
	if r := transitAPDU(nfc.Select(aid)); nfc.SW(r) != 0x9000 {
		t.Fatalf("select: %X", r)
	}
	if r := transitAPDU(nfc.Select([]byte{0xF0, 1, 2, 3, 4})); nfc.SW(r) != 0x6A82 {
		t.Fatalf("another AID: %X", r)
	}
	if r := transitAPDU(nfc.GetBalance); nfc.SW(r) != 0x9000 || cents(r[:4]) != 300 {
		t.Fatalf("balance: %X", r)
	}
	if r := transitAPDU(nfc.Debit(200, 7)); nfc.SW(r) != 0x9000 || cents(r[:4]) != 100 || len(transit.Trips) != 1 {
		t.Fatalf("debit: %X, %+v", r, transit)
	}
	if r := transitAPDU(nfc.Debit(200, 7)); nfc.SW(r) != 0x9401 || transit.Balance != 100 {
		t.Fatalf("debit past the balance: %X, %+v", r, transit)
	}
}

func TestPaymentDirectory(t *testing.T) {
	aid, _ := hex.DecodeString(payAID)
	got := nfc.TLVFind(payFCI(), 0x4F)
	if len(got) != 1 || !bytes.Equal(got[0], aid) {
		t.Fatalf("the PPSE lists %X", got)
	}
	ppse, _ := hex.DecodeString(ui.PPSE)
	if sel, ok := selected(nfc.Select(ppse), ui.PPSE); !sel || !ok {
		t.Fatal("the PPSE select")
	}
}
