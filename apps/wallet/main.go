// Wallet is an Alipay-style sample app built only on platform capabilities:
// device attestation, a rotating payment code shown on a secure surface, the
// system scanner, and payments confirmed in the system trusted UI. A mock
// "server" in this process verifies every signature the way a real payment
// backend would, against the manufacturer root.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type bill struct {
	id, title, kind string
	amount          int
	income          bool
	at              time.Time
	proof           *ui.KeyProof
}

// income is the data part of a "money received" push from the wallet's
// server; the notification part is shown by the system.
type income struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	From   string `json:"from"`
	Amount int    `json:"amount"` // cents
}

type order struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Merchant string `json:"merchant,omitempty"`
	To       string `json:"to,omitempty"`
	Memo     string `json:"memo,omitempty"`
	Amount   int    `json:"amount"`
}

type wallet struct {
	app      *sdk.App
	root     *keystore.Roots
	att      *ui.Attestation
	trustErr string
	balance  int
	bills    []*bill
	code     string
	codeAt   time.Time
	onCode   bool
	seq      int
	pending  *order
	push     string
	// asked: a payment another app asked for (intent.invoke), and its order.
	asked      *ui.Intent
	askedOrder order
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	w := &wallet{app: app, balance: 102400}
	app.OnStart = func(arg string) {
		app.Reset(w.homeScreen())
		w.attest()
		w.open(arg)
		// A real wallet uploads the token to its server after login.
		var tok ui.PushToken
		app.Go(ui.CapPushRegister, nil, &tok, func(err error) {
			if err == nil {
				w.push = tok.Token
			}
		})
	}
	app.OnEvent = func(name, value string) {
		switch name {
		case "push":
			w.onPush(value)
		case ui.EventNFCAPDU:
			w.onAPDU(value)
		case ui.EventOpen:
			w.open(value)
		}
	}
	app.OnAction = w.action
	app.OnPopped = func(id string) {
		if id == "paycode" {
			w.onCode = false
		}
		if id == "asked" {
			w.answer(false, "")
		}
	}
	go func() {
		for range time.Tick(time.Second) {
			app.Post(w.tick)
		}
	}()
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

func yuan(cents int) string {
	s := strconv.Itoa(cents / 100)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return fmt.Sprintf("¥%s.%02d", s, cents%100)
}

func nonce() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// attest is what a payment backend does at login: fresh nonce, device
// attestation, chain verified against the pinned manufacturer root.
func (w *wallet) attest() {
	root, err := keystore.LoadRootPub(os.Getenv(ui.EnvTrustRoot))
	if err != nil {
		w.trustErr = tr.T("Cannot load the manufacturer root certificate")
		w.app.Update(w.homeScreen())
		return
	}
	w.root = root
	n := nonce()
	var att ui.Attestation
	w.app.Go(ui.CapAttest, ui.AttestArgs{Nonce: n}, &att, func(err error) {
		if err == nil {
			err = keystore.VerifyAttestation(w.root, &att, w.app.ID, n)
		}
		if err == nil && att.Boot == keystore.BootUnlocked {
			err = errors.New(tr.T("The device is unlocked; Feiqian does not pay on unlocked devices"))
		}
		if err != nil {
			w.trustErr = err.Error()
		} else {
			w.att = &att
		}
		w.app.Update(w.homeScreen())
		if w.asked != nil {
			w.app.Update(w.askedScreen())
		}
	})
}

func (w *wallet) homeScreen() ui.Screen {
	trust := sdk.Styled(ui.StyleDim, tr.T("Verifying the device…"))
	switch {
	case w.att != nil:
		trust = sdk.Styled(ui.StyleOK, tr.T("✓ Device trusted · hardware key attestation (simulated)"))
	case w.trustErr != "":
		trust = sdk.Styled(ui.StyleWarn, tr.T("✗ Device not trusted: %s", w.trustErr))
	}
	item := func(id, icon, title, sub string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Icon: icon, Title: title, Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
	}
	return ui.Screen{ID: "home", Title: tr.T("Feiqian"), Right: "scan",
		Actions: []ui.Action{{ID: "scan", Label: tr.T("Scan"), Key: "s"}, {ID: "security", Label: tr.T("Security"), Key: "x"}},
		Body: []ui.Node{
			sdk.Styled(ui.StyleBig, yuan(w.balance)),
			sdk.Styled(ui.StyleDim, "            "+tr.T("Available balance")),
			trust,
			sdk.Section(tr.T("Shortcuts")),
			item("paycode", tr.T("P"), tr.T("Payment code"), tr.T("Show it to the merchant; it refreshes every 30 seconds")),
			item("scan", tr.T("S"), tr.T("Scan"), tr.T("The system scans; the app gets only the result")),
			item("transfer", tr.T("T"), tr.T("Transfer"), tr.T("Needs the system's secure confirmation")),
			item("bills", tr.T("B"), tr.T("Bills"), tr.T("%d bills", len(w.bills))),
			item("transit", tr.T("C"), tr.T("Transit card"), w.transitSummary()),
		}}
}

func (w *wallet) refreshCode() {
	ts := time.Now().Unix()
	payload := fmt.Sprintf("PAYCODE|U8848|%d", ts)
	var proof ui.KeyProof
	w.app.Go(ui.CapKeySign, ui.SignArgs{Key: "paycode", Payload: payload}, &proof, func(err error) {
		if err != nil {
			w.app.Toast(tr.T("Cannot make the payment code: %v", err))
			return
		}
		w.code, w.codeAt = payload+"|"+proof.Sig[:12], time.Now()
		if w.onCode {
			w.app.Update(w.paycodeScreen())
		}
	})
}

func (w *wallet) paycodeScreen() ui.Screen {
	left := 30 - int(time.Since(w.codeAt).Seconds())
	body := []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Generating…"))}
	if w.code != "" {
		body = []ui.Node{
			{Kind: ui.KindImage, Src: "qr:" + w.code, Secure: true},
			sdk.Styled(ui.StyleDim, tr.T("Refreshes every 30 seconds · %d s left", max(0, left))),
			sdk.Styled(ui.StyleWarn, tr.T("Secure surface: the system hides it from screenshots, recordings and remote casting")),
		}
	}
	return ui.Screen{ID: "paycode", Title: tr.T("Payment code"), Body: body, Actions: []ui.Action{{ID: "refresh", Label: tr.T("Refresh now"), Key: "r"}}}
}

func (w *wallet) transferScreen() ui.Screen {
	return ui.Screen{ID: "transfer", Title: tr.T("Transfer"), Body: []ui.Node{
		sdk.Styled(ui.StyleDim, tr.T("Balance %s", yuan(w.balance))),
		{Kind: ui.KindInput, ID: "to", Placeholder: tr.T("Recipient account (phone number or email)"), Keyboard: "email"},
		{Kind: ui.KindInput, ID: "amount", Placeholder: tr.T("Amount (yuan)"), Keyboard: "number"},
		{Kind: ui.KindInput, ID: "memo", Placeholder: tr.T("Note (optional)")},
		sdk.Button("next", tr.T("Next"), "next"),
	}}
}

func (w *wallet) billsScreen() ui.Screen {
	s := ui.Screen{ID: "bills", Title: tr.T("Bills")}
	for i := len(w.bills) - 1; i >= 0; i-- {
		b := w.bills[i]
		sub, sign := tr.T("%s · signature verified", b.at.Format("01-02 15:04")), "-"
		if b.income {
			sub, sign = tr.T("%s · received", b.at.Format("01-02 15:04")), "+"
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: b.id, Icon: b.kind, Title: b.title,
			Subtitle: sub, Trailing: sign + yuan(b.amount),
			Actions: []ui.Action{sdk.Open(tr.T("Details")), {ID: "copy", Label: tr.T("Copy order number"), Key: "c"},
				{ID: "refund", Label: tr.T("Request refund"), Group: ui.GroupDanger, Confirm: tr.T("Request a refund? The merchant has to agree.")}}})
	}
	if len(w.bills) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No bills yet")))
	}
	return s
}

func (w *wallet) securityScreen() ui.Screen {
	body := []ui.Node{sdk.Section(tr.T("Device"))}
	if w.att != nil {
		body = append(body,
			sdk.Text(tr.T("Device ID: %s", w.att.DeviceID)),
			sdk.Text(tr.T("Boot state: %s", w.att.Boot)),
			sdk.Styled(ui.StyleOK, tr.T("Device certificate: issued by the manufacturer root ✓")))
	} else {
		body = append(body, sdk.Styled(ui.StyleWarn, tr.T("Device attestation failed")))
	}
	if w.push != "" {
		body = append(body, sdk.Section(tr.T("Push")), sdk.Text(tr.T("Registered: payment notices arrive by system push; the app need not stay running")))
	}
	body = append(body, sdk.Section(tr.T("Keys")),
		sdk.Text(tr.T("pay: payments and transfers, each confirmed securely by the system")),
		sdk.Text(tr.T("paycode: signs payment codes, no confirmation")),
		sdk.Text(tr.T("pay-bio: payments confirmed by fingerprint; made after the payment PIN, void once the fingerprints change")),
		sdk.Section(tr.T("How it works")),
		sdk.Styled(ui.StyleDim, tr.T("Private keys stay in the system key service (a TEE in production). The server verifies manufacturer root → device certificate → app key certificate → signature, one step at a time, and checks that what was signed is the text the secure confirmation showed.")))
	return ui.Screen{ID: "security", Title: tr.T("Security"), Body: body}
}

func (w *wallet) action(e sdk.ActionEvent) {
	if w.nfcAction(e) {
		return
	}
	switch e.Screen {
	case "asked":
		switch e.Action {
		case "asked_pay":
			o := w.askedOrder
			w.pay(o, func(paid bool) {
				w.answer(paid, o.ID)
				w.app.Pop()
			})
		case "asked_cancel":
			w.answer(false, "")
			w.app.Pop()
		}
	case "home":
		target := e.Action
		if e.Action == ui.ActionOpen {
			target = e.Node
		}
		switch target {
		case "paycode":
			w.onCode = true
			w.app.Push(w.paycodeScreen())
			w.refreshCode()
		case "scan":
			w.scan()
		case "transfer":
			w.app.Push(w.transferScreen())
		case "bills":
			w.app.Push(w.billsScreen())
		case "security":
			w.app.Push(w.securityScreen())
		}
	case "paycode":
		w.refreshCode()
	case "transfer":
		amt, err := parseYuan(e.Fields["amount"])
		to := strings.TrimSpace(e.Fields["to"])
		switch {
		case to == "":
			w.app.Toast(tr.T("Enter the recipient's account"))
		case err != nil || amt <= 0:
			w.app.Toast(tr.T("The amount is not valid"))
		case amt > w.balance:
			w.app.Toast(tr.T("Insufficient balance"))
		default:
			w.seq++
			w.pay(order{ID: fmt.Sprintf("T%d%03d", time.Now().Unix(), w.seq), Kind: "transfer", To: to, Memo: e.Fields["memo"], Amount: amt}, nil)
		}
	case "confirm":
		if e.Action == "pay" && w.pending != nil {
			w.pay(*w.pending, nil)
		}
	case "result":
		w.app.Reset(w.homeScreen())
	case "bills":
		b := w.billByID(e.Node)
		if b == nil {
			return
		}
		switch {
		case e.Action == ui.ActionOpen && b.income:
			w.app.Push(ui.Screen{ID: "bill", Title: tr.T("Bill details"), Body: []ui.Node{
				sdk.Styled(ui.StyleBig, "+"+yuan(b.amount)),
				sdk.Text(b.title), sdk.Text(tr.T("Serial number: %s", b.id)), sdk.Text(tr.T("Time: %s", b.at.Format("2006-01-02 15:04:05"))),
				sdk.Styled(ui.StyleDim, tr.T("Pushed by the server; if the app is frozen, the system wakes it briefly to record it")),
			}})
		case e.Action == ui.ActionOpen:
			w.app.Push(ui.Screen{ID: "bill", Title: tr.T("Bill details"), Body: []ui.Node{
				sdk.Styled(ui.StyleBig, "-"+yuan(b.amount)),
				sdk.Text(b.title), sdk.Text(tr.T("Order number: %s", b.id)), sdk.Text(tr.T("Time: %s", b.at.Format("2006-01-02 15:04:05"))),
				sdk.Styled(ui.StyleOK, tr.T("Signature: device %s · confirmed by the user", b.proof.DeviceID)),
			}})
		case e.Action == "copy":
			w.app.Go(ui.CapClipboardSet, ui.ClipArgs{Text: b.id}, nil, func(error) {})
		case e.Action == "refund":
			w.app.Toast(tr.T("Refund requested"))
		}
	}
}

func (w *wallet) billByID(id string) *bill {
	for _, b := range w.bills {
		if b.id == id {
			return b
		}
	}
	return nil
}

func parseYuan(s string) (int, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	return int(f*100 + 0.5), nil
}

func (w *wallet) scan() {
	var res ui.ScanResult
	w.app.Go(ui.CapScanQR, nil, &res, func(err error) {
		if err != nil {
			w.app.Toast(tr.T("Scan: %v", err))
			return
		}
		parts := strings.Split(res.Text, "|")
		if len(parts) != 4 || parts[0] != "PAY" {
			w.app.Toast(tr.T("Unrecognized QR code"))
			return
		}
		amt, _ := strconv.Atoi(parts[3])
		w.seq++
		o := order{ID: fmt.Sprintf("P%d%03d", time.Now().Unix(), w.seq), Kind: "pay", Merchant: parts[2], Amount: amt}
		w.pending = &o
		w.app.Push(ui.Screen{ID: "confirm", Title: tr.T("Confirm payment"), Body: []ui.Node{
			sdk.Section(tr.T("Order")),
			sdk.Text(tr.T("Payee: %s (%s)", o.Merchant, parts[1])),
			sdk.Styled(ui.StyleBig, yuan(o.Amount)),
			sdk.Styled(ui.StyleDim, tr.T("Order number: %s", o.ID)),
			sdk.Button("pay", tr.T("Confirm payment"), "pay"),
		}})
	})
}

func (o order) prompt() string {
	if o.Kind == "transfer" {
		return tr.T("Transfer to “%s”: %s", o.To, yuan(o.Amount))
	}
	return tr.T("Pay “%s”: %s", o.Merchant, yuan(o.Amount))
}

// open handles a deep link: a payment another app asks for ("pay").
func (w *wallet) open(link string) {
	raw, ok := strings.CutPrefix(link, ui.IntentPrefix)
	if !ok {
		return
	}
	var in ui.Intent
	var req struct {
		To     string `json:"to"`
		Amount int    `json:"amount"`
		Memo   string `json:"memo"`
	}
	if json.Unmarshal([]byte(raw), &in) != nil || in.Action != "pay" {
		return
	}
	w.answer(false, "")
	w.asked = &in
	if json.Unmarshal([]byte(in.Payload), &req) != nil || req.To == "" || req.Amount <= 0 || req.Amount > w.balance {
		w.answer(false, "")
		w.app.Toast(tr.T("The payment “%s” asked for is not valid; nothing was paid", in.FromName))
		return
	}
	w.seq++
	w.askedOrder = order{ID: fmt.Sprintf("I%d%03d", time.Now().Unix(), w.seq), Kind: "transfer", To: req.To, Memo: req.Memo, Amount: req.Amount}
	w.app.Push(w.askedScreen())
}

// askedScreen is the payment another app asked for, with the device check
// it waits on.
func (w *wallet) askedScreen() ui.Screen {
	check := sdk.Styled(ui.StyleDim, tr.T("Verifying the device…"))
	switch {
	case w.att != nil:
		check = sdk.Styled(ui.StyleOK, tr.T("Device trusted"))
	case w.trustErr != "":
		check = sdk.Styled(ui.StyleWarn, w.trustErr)
	}
	return ui.Screen{ID: "asked", Title: tr.T("Payment request"), Body: []ui.Node{
		sdk.Styled(ui.StyleBig, tr.T("“%s” asks for a payment", w.asked.FromName)),
		sdk.Text(w.askedOrder.prompt()),
		sdk.Styled(ui.StyleDim, tr.T("Balance %s", yuan(w.balance))),
		check,
		sdk.Button("asked_pay", tr.T("Pay"), "asked_pay"),
		sdk.Button("asked_cancel", tr.T("Don't pay"), "asked_cancel"),
	}}
}

// answer tells the app that asked how its payment went.
func (w *wallet) answer(paid bool, bill string) {
	if w.asked == nil {
		return
	}
	result := ""
	if paid {
		raw, _ := json.Marshal(map[string]string{"bill": bill, "amount": yuan(w.askedOrder.Amount)})
		result = string(raw)
	}
	w.app.Go(ui.CapIntentReply, ui.IntentReplyArgs{ID: w.asked.ID, OK: paid, Result: result}, nil, func(err error) {
		if err != nil {
			log.Printf("intent reply: %v", err)
		}
	})
	w.asked = nil
}

// pay asks the system for a user-confirmed signature, then verifies it as the
// payment server would before moving any money. With done (a payment
// another app asked for) the caller hears how it went instead of a result
// page: the user is back in that app.
func (w *wallet) pay(o order, done func(paid bool)) {
	finish := func(paid bool) {
		if done != nil {
			done(paid)
		}
	}
	if w.att == nil {
		w.app.Toast(tr.T("The device failed verification; it cannot pay"))
		finish(false)
		return
	}
	raw, _ := json.Marshal(o)
	prompt, payload := o.prompt(), string(raw)
	var proof ui.KeyProof
	w.app.Go(ui.CapAuthConfirm, ui.AuthConfirmArgs{Key: "pay", BioKey: bioKey, Prompt: prompt, Payload: payload}, &proof, func(err error) {
		if err != nil {
			w.app.Toast(tr.T("Cancelled: %v", err))
			finish(false)
			return
		}
		if err := keystore.VerifyConfirm(w.root, &proof, w.app.ID, payKey(&proof), prompt, payload); err != nil {
			if done != nil {
				w.app.Toast(tr.T("Server signature check failed: %v", err))
				finish(false)
				return
			}
			w.app.Replace(ui.Screen{ID: "result", Title: tr.T("Payment failed"), Body: []ui.Node{
				sdk.Styled(ui.StyleWarn, tr.T("Server signature check failed: %v", err)), sdk.Button("done", tr.T("Back"), "done")}})
			return
		}
		w.balance -= o.Amount
		title, icon := o.Merchant, tr.T("P")
		if o.Kind == "transfer" {
			title, icon = tr.T("Transfer to %s", o.To), tr.T("T")
		}
		w.bills = append(w.bills, &bill{id: o.ID, title: title, kind: icon, amount: o.Amount, at: time.Now(), proof: &proof})
		if done != nil {
			w.app.Update(w.homeScreen())
			finish(true)
			return
		}
		w.app.Replace(ui.Screen{ID: "result", Title: tr.T("Payment successful"), Body: []ui.Node{
			sdk.Styled(ui.StyleBig, "✓ "+tr.T("Payment successful")),
			sdk.Styled(ui.StyleBig, yuan(o.Amount)),
			sdk.Text(title),
			sdk.Styled(ui.StyleOK, tr.T("Server signature check: passed")),
			sdk.Text(howConfirmed(&proof)),
			sdk.Styled(ui.StyleDim, tr.T("Manufacturer root → device %s → app key (user verified) → signature", proof.DeviceID)),
			sdk.Button("done", tr.T("Done"), "done"),
		}})
		w.app.Update(w.homeScreen())
	})
}

func (w *wallet) onPush(value string) {
	var ev ui.PushEvent
	var in income
	if json.Unmarshal([]byte(value), &ev) != nil {
		return
	}
	data := ev.Data
	if push.IsSealed(data) {
		// Sealed by the wallet's server to the app's push key: only the
		// keystore opens it, the gateway and the shell saw ciphertext.
		var res ui.PushOpenRes
		if err := w.app.Call(ui.CapPushOpen, ui.PushOpenArgs{Data: data}, &res); err != nil {
			log.Printf("push %s: %v", ev.ID, err)
			return
		}
		plain, err := base64.StdEncoding.DecodeString(res.Data)
		if err != nil {
			return
		}
		data = string(plain)
	}
	if json.Unmarshal([]byte(data), &in) != nil || in.Type != "income" || in.Amount <= 0 {
		return
	}
	if in.ID == "" {
		in.ID = ev.ID
	}
	if w.billByID(in.ID) != nil {
		return
	}
	w.balance += in.Amount
	w.bills = append(w.bills, &bill{id: in.ID, title: tr.T("From %s", in.From), kind: tr.T("R"), amount: in.Amount, income: true, at: time.UnixMilli(ev.Sent)})
	w.app.Update(w.homeScreen())
	w.app.Update(w.billsScreen())
}

func (w *wallet) tick() {
	if !w.onCode {
		return
	}
	if time.Since(w.codeAt) >= 30*time.Second {
		w.refreshCode()
	}
	w.app.Update(w.paycodeScreen())
}
