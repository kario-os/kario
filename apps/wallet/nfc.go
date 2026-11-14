package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Feiqian's contactless cards (the manifest's "nfc"): a transit card
// (category other, express capable) whose balance the app keeps and the
// gate debits, and a payment card (category payment: the system lets it
// answer only on an unlocked phone, when Feiqian is the payment app). The
// commands are the simplified flows of 30-nfc.md, not an operator's or
// EMV's: amounts are 4-byte big-endian cents.

const (
	transitAID = "F0465154524E53"
	payAID     = "F0465150415901"
)

var (
	swOK           = []byte{0x90, 0x00}
	swNotFound     = []byte{0x6A, 0x82}
	swNoINS        = []byte{0x6D, 0x00}
	swWrongLength  = []byte{0x67, 0x00}
	swInsufficient = []byte{0x94, 0x01}
	swConditions   = []byte{0x69, 0x85}
)

type trip struct {
	At      time.Time `json:"at"`
	Fare    int       `json:"fare"`
	Balance int       `json:"balance"`
}

// transitCard is kept in the app's data: the gate may start the app in
// the background, with the screen off.
type transitCard struct {
	Balance int    `json:"balance"`
	Trips   []trip `json:"trips"`
}

var transit = loadTransit()

func transitPath() string { return filepath.Join(os.Getenv("PHONE_DATA_DIR"), "transit.json") }

func loadTransit() *transitCard {
	t := &transitCard{Balance: 2000}
	if raw, err := os.ReadFile(transitPath()); err == nil {
		_ = json.Unmarshal(raw, t)
	}
	return t
}

func (t *transitCard) save() {
	raw, _ := json.Marshal(t)
	tmp := transitPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, transitPath())
	}
}

func cents(b []byte) int { return int(binary.BigEndian.Uint32(b)) }

func withSW(data []byte, sw []byte) []byte { return append(append([]byte(nil), data...), sw...) }

// onAPDU answers the reader's command for one of the cards.
func (w *wallet) onAPDU(value string) {
	var ev ui.NFCAPDU
	if json.Unmarshal([]byte(value), &ev) != nil {
		return
	}
	cmd, err := hex.DecodeString(ev.APDU)
	answer := func(resp []byte) {
		w.app.Go(ui.CapNFCReply, ui.NFCReplyArgs{Session: ev.Session, APDU: strings.ToUpper(hex.EncodeToString(resp))}, nil, func(err error) {
			if err != nil {
				log.Printf("nfc reply: %v", err)
			}
		})
	}
	switch {
	case err != nil || len(cmd) < 4:
		answer(swWrongLength)
	case ev.Card == "transit":
		before := len(transit.Trips)
		answer(transitAPDU(cmd))
		if len(transit.Trips) != before {
			transit.save()
			w.app.Update(w.homeScreen())
			w.app.Update(w.transitScreen())
		}
	case ev.Card == "pay":
		w.payAPDU(cmd, answer)
	default:
		answer(swNotFound)
	}
}

func selected(cmd []byte, aid string) (bool, bool) {
	if len(cmd) < 5 || cmd[1] != 0xA4 || cmd[2] != 0x04 || len(cmd) < 5+int(cmd[4]) {
		return false, false
	}
	return true, strings.EqualFold(hex.EncodeToString(cmd[5:5+int(cmd[4])]), aid)
}

// transitAPDU: SELECT, GET BALANCE (80 5C 00 02 04), DEBIT (80 54 01 00
// 08, the fare and the gate).
func transitAPDU(cmd []byte) []byte {
	if sel, ours := selected(cmd, transitAID); sel {
		if ours {
			return swOK
		}
		return swNotFound
	}
	switch {
	case cmd[0] == 0x80 && cmd[1] == 0x5C:
		return withSW(binary.BigEndian.AppendUint32(nil, uint32(transit.Balance)), swOK)
	case cmd[0] == 0x80 && cmd[1] == 0x54:
		if len(cmd) < 13 || cmd[4] != 8 {
			return swWrongLength
		}
		fare := cents(cmd[5:9])
		if fare <= 0 || fare > transit.Balance {
			return swInsufficient
		}
		transit.Balance -= fare
		transit.Trips = append(transit.Trips, trip{At: time.Now(), Fare: fare, Balance: transit.Balance})
		return withSW(binary.BigEndian.AppendUint32(nil, uint32(transit.Balance)), swOK)
	}
	return swNoINS
}

// payFCI is the PPSE directory listing the payment card's AID.
func payFCI() []byte {
	aid, _ := hex.DecodeString(payAID)
	ppse, _ := hex.DecodeString(ui.PPSE)
	label := []byte("FEIQIAN")
	entry := append(append([]byte{0x4F, byte(len(aid))}, aid...), append([]byte{0x50, byte(len(label))}, label...)...)
	dir := append([]byte{0x61, byte(len(entry))}, entry...)
	prop := append([]byte{0xBF, 0x0C, byte(len(dir))}, dir...)
	body := append(append([]byte{0x84, byte(len(ppse))}, ppse...), append([]byte{0xA5, byte(len(prop))}, prop...)...)
	return append([]byte{0x6F, byte(len(body))}, body...)
}

// payAPDU: SELECT the PPSE and the card, then PURCHASE (80 70 00 00 08,
// the amount and the terminal), answered with a cryptogram the app's key
// signs. The system has already decided the phone may pay.
func (w *wallet) payAPDU(cmd []byte, answer func([]byte)) {
	if sel, ppse := selected(cmd, ui.PPSE); sel {
		_, ours := selected(cmd, payAID)
		switch {
		case ppse:
			answer(withSW(payFCI(), swOK))
		case ours:
			answer(swOK)
		default:
			answer(swNotFound)
		}
		return
	}
	if cmd[0] != 0x80 || cmd[1] != 0x70 {
		answer(swNoINS)
		return
	}
	if len(cmd) < 13 || cmd[4] != 8 {
		answer(swWrongLength)
		return
	}
	amount, terminal := cents(cmd[5:9]), binary.BigEndian.Uint32(cmd[9:13])
	if amount <= 0 || amount > w.balance {
		answer(swInsufficient)
		return
	}
	w.seq++
	id := fmt.Sprintf("N%d%03d", time.Now().Unix(), w.seq)
	var proof ui.KeyProof
	w.app.Go(ui.CapKeySign, ui.SignArgs{Key: "nfcpay", Payload: fmt.Sprintf("NFCPAY|%s|%d|%08X", id, amount, terminal)}, &proof, func(err error) {
		if err != nil {
			answer(swConditions)
			return
		}
		sum := sha256.Sum256([]byte(proof.Sig))
		w.balance -= amount
		w.bills = append(w.bills, &bill{id: id, title: tr.T("Contactless payment"), kind: tr.T("N"), amount: amount, at: time.Now(), proof: &proof})
		w.app.Update(w.homeScreen())
		w.app.Update(w.billsScreen())
		answer(withSW(sum[:8], swOK))
	})
}

func (w *wallet) transitSummary() string {
	return tr.T("Balance %s · %d trips", yuan(transit.Balance), len(transit.Trips))
}

func (w *wallet) transitScreen() ui.Screen {
	s := ui.Screen{ID: "transit", Title: tr.T("Transit card"), Actions: []ui.Action{{ID: "topup", Label: tr.T("Top up ¥20.00"), Key: "t"},
		{ID: "readcard", Label: tr.T("Read a transit card's balance"), Key: "r"}},
		Body: []ui.Node{
			sdk.Styled(ui.StyleBig, yuan(transit.Balance)),
			sdk.Styled(ui.StyleDim, tr.T("Hold the phone to the gate. As the express transit card (in Cards) it also works with the screen off and the phone locked")),
			sdk.Section(tr.T("Trips")),
		}}
	for i := len(transit.Trips) - 1; i >= 0 && i >= len(transit.Trips)-10; i-- {
		t := transit.Trips[i]
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("trip%d", i), Title: tr.T("Gate"),
			Subtitle: t.At.Format("01-02 15:04"), Trailing: "-" + yuan(t.Fare)})
	}
	if len(transit.Trips) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No trips yet")))
	}
	return s
}

// nfcAction handles the transit card's page; false for other actions.
func (w *wallet) nfcAction(e sdk.ActionEvent) bool {
	switch {
	case e.Screen == "home" && (e.Node == "transit" && e.Action == ui.ActionOpen):
		w.app.Push(w.transitScreen())
	case e.Screen == "transit" && e.Action == "topup":
		if w.balance < 2000 {
			w.app.Toast(tr.T("Insufficient balance"))
			return true
		}
		w.balance -= 2000
		transit.Balance += 2000
		transit.save()
		w.app.Update(w.transitScreen())
		w.app.Update(w.homeScreen())
	case e.Screen == "transit" && e.Action == "readcard":
		w.readCard()
	default:
		return false
	}
	return true
}

// readCard reads the balance of a physical transit card of the simplified
// flow held to the phone (reader mode, nfc.read): SELECT its AID, then GET
// BALANCE, which closes the session.
func (w *wallet) readCard() {
	var tag ui.NFCTag
	w.app.Go(ui.CapNFCRead, ui.NFCReadArgs{Seconds: 20}, &tag, func(err error) {
		switch {
		case err != nil && strings.HasSuffix(err.Error(), ui.ErrCancelled):
			w.app.Toast(tr.T("Reading stopped"))
		case err != nil && strings.HasSuffix(err.Error(), ui.ErrNFCNoCard):
			w.app.Toast(tr.T("No card was found"))
		case err != nil:
			w.app.Toast(err.Error())
		case tag.Session == "":
			w.app.Toast(tr.T("Not a transit card this app can read"))
		default:
			w.readBalance(tag.Session)
		}
	})
}

func (w *wallet) readBalance(session string) {
	aid, _ := hex.DecodeString(transitAID)
	sel := append(append([]byte{0x00, 0xA4, 0x04, 0x00, byte(len(aid))}, aid...), 0x00)
	notOurs := func() {
		w.app.Go(ui.CapNFCTransceive, ui.NFCTransceiveArgs{Session: session, Close: true}, nil, nil)
		w.app.Toast(tr.T("Not a transit card this app can read"))
	}
	var res ui.NFCTransceiveRes
	w.app.Go(ui.CapNFCTransceive, ui.NFCTransceiveArgs{Session: session, APDU: hex.EncodeToString(sel)}, &res, func(err error) {
		if err != nil || !strings.EqualFold(res.APDU, "9000") {
			notOurs()
			return
		}
		var bal ui.NFCTransceiveRes
		w.app.Go(ui.CapNFCTransceive, ui.NFCTransceiveArgs{Session: session, APDU: "805C000204", Close: true}, &bal, func(err error) {
			r, herr := hex.DecodeString(bal.APDU)
			if err != nil || herr != nil || len(r) != 6 || !strings.HasSuffix(strings.ToUpper(bal.APDU), "9000") {
				w.app.Toast(tr.T("Not a transit card this app can read"))
				return
			}
			w.app.Toast(tr.T("The card holds %s", yuan(cents(r[:4]))))
		})
	})
}
