package main

import (
	"bytes"
	"encoding/hex"
	"os"
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

// The card's record cut short (the power going while the gate debited it)
// is read from its copy; with neither whole the balance is nothing, not a
// new card's.
func TestTransitCardFromItsCopy(t *testing.T) {
	t.Setenv("PHONE_DATA_DIR", t.TempDir())
	saved := transit
	t.Cleanup(func() { transit = saved })
	transit = &transitCard{Balance: 300}
	if r := transitAPDU(nfc.Debit(120, 7)); nfc.SW(r) != 0x9000 {
		t.Fatalf("debit: %X", r)
	}
	transit.save()
	if err := os.Truncate(transitPath(), 12); err != nil {
		t.Fatal(err)
	}
	if got := loadTransit(); got.Balance != 180 || len(got.Trips) != 1 {
		t.Fatalf("cut short: %+v, want 180 and the trip from the copy", got)
	}
	for _, p := range []string{transitPath(), transitPath() + ".bak"} {
		if err := os.WriteFile(p, []byte(`{"balance":18`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := loadTransit(); got.Balance != 0 {
		t.Fatalf("neither whole: %+v, want nothing", got)
	}
	for _, p := range []string{transitPath(), transitPath() + ".bak"} {
		_ = os.Remove(p)
	}
	if got := loadTransit(); got.Balance != 2000 {
		t.Fatalf("a new card: %+v", got)
	}
}
