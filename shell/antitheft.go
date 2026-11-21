package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/antitheft"
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The anti-theft lock (31-lock-and-biometrics.md §11, package antitheft).
// With a Kario account signed in, the phone binds itself to the account on
// the account server and keeps a record of it in the persistent area,
// which a factory reset does not reach, and the record's token in the data
// area. A reset from Settings with the owner's PIN takes the record away
// (unless the owner leaves the lock on); after any other way the data was
// erased (the lock screen's, a flashed data partition) the data area lacks
// the token, and the phone starts locked: a system screen asks for the
// account, by a code the server sends to its address, until the server
// signs a release for this phone.

type theftRecord struct {
	// Account is the account server's ID of the account, Email its address
	// masked (all the locked phone shows of it), Device this phone's ID.
	Account string `json:"account"`
	Email   string `json:"email"`
	Device  string `json:"device"`
	// Token is in the data area too (theftMarkPath) while it is the one
	// this phone was set up with.
	Token string `json:"token"`
	Since int64  `json:"since"`
	// Locked says how the phone came to be locked ("" until it is).
	Locked string `json:"locked,omitempty"`
}

type theftShell struct {
	rec *theftRecord
	// locked: the phone waits for the account (theftOverlay); armed: the
	// record is this data area's.
	locked, armed bool
	// arming: a binding is being asked for; not again before armAt.
	arming bool
	armAt  time.Time
	fails  int
}

const (
	theftTimeout  = 20 * time.Second
	theftRetryMin = time.Minute
	theftRetryMax = 6 * time.Hour
	// lockedByData: the data area was replaced without the Shell asking.
	lockedByData = "data"
)

func (c *Core) theftPath() string { return filepath.Join(c.cfg.PersistDir, "antitheft.json") }

func (c *Core) theftMarkPath() string {
	return filepath.Join(c.cfg.StateDir, "shell", "antitheft.json")
}

func (c *Core) saveTheft() error {
	if c.theft.rec == nil {
		if err := os.Remove(c.theftPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, _ := json.MarshalIndent(c.theft.rec, "", "  ")
	tmp := c.theftPath() + ".tmp"
	if err := os.MkdirAll(c.cfg.PersistDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.theftPath())
}

func (c *Core) theftMark() string {
	var m struct {
		Token string `json:"token"`
	}
	raw, _ := os.ReadFile(c.theftMarkPath())
	_ = json.Unmarshal(raw, &m)
	return m.Token
}

// startTheft reads the record at start and locks the phone when the data
// area is not the one it was set up with.
func (c *Core) startTheft() {
	if c.cfg.PersistDir == "" {
		return
	}
	raw, err := os.ReadFile(c.theftPath())
	if err != nil {
		return
	}
	rec := &theftRecord{}
	if err := json.Unmarshal(raw, rec); err != nil || rec.Account == "" {
		c.log.Printf("anti-theft lock: the record does not parse: %v", err)
		rec = &theftRecord{Account: "?", Locked: lockedByData}
	}
	c.theft.rec = rec
	if rec.Locked == "" && rec.Token != "" && c.theftMark() == rec.Token {
		c.theft.armed = true
		return
	}
	if rec.Locked == "" {
		rec.Locked = lockedByData
		_ = c.saveTheft()
	}
	c.log.Printf("anti-theft lock: the phone is locked (%s) until the account %s signs in", rec.Locked, rec.Email)
	c.theft.locked = true
	c.pushOverlay(&theftOverlay{})
}

// TheftLocked reports the anti-theft lock holding (selftest).
func (c *Core) TheftLocked() bool { return c.theft.locked }

func (c *Core) karioAccount() *accountRecord {
	for _, r := range c.accounts.file.Accounts {
		if r.Kind == ui.AccountKario {
			return r
		}
	}
	return nil
}

func (c *Core) theftAvailable() bool {
	return c.cfg.PersistDir != "" && c.cfg.AccountURL != ""
}

// theftCheck arms the lock for the Kario account signed in, and takes the
// record of one no longer signed in away.
func (c *Core) theftCheck() {
	t := &c.theft
	if !c.theftAvailable() || t.locked || t.arming || c.reset.busy {
		return
	}
	r := c.karioAccount()
	if r == nil {
		if t.rec != nil {
			c.log.Printf("anti-theft lock: off (no Kario account)")
			t.rec, t.armed = nil, false
			_ = c.saveTheft()
			_ = os.Remove(c.theftMarkPath())
		}
		return
	}
	if t.armed || c.now().Before(t.armAt) {
		return
	}
	t.arming = true
	rec := *r
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), theftTimeout)
		defer cancel()
		pass, err := c.openPassword(rec)
		var bound antitheft.Bound
		var device string
		if err == nil {
			user := strings.ToLower(rec.User)
			var req antitheft.Request
			req, err = c.theftRequest(ctx, antitheft.PurposeBind, user, "")
			if err == nil {
				device = req.Attestation.DeviceID
				bound, err = c.theftClient().Bind(ctx, user, pass, req)
			}
		}
		c.Post(func() { c.theftArmed(rec.ID, device, bound, err) })
	}()
}

func (c *Core) theftArmed(id, device string, bound antitheft.Bound, err error) {
	t := &c.theft
	t.arming = false
	if r := c.karioAccount(); r == nil || r.ID != id || t.locked || c.reset.busy {
		return
	}
	if err != nil {
		t.fails++
		t.armAt = c.now().Add(min(theftRetryMax, theftRetryMin<<min(t.fails-1, 9)))
		c.log.Printf("anti-theft lock: not armed yet: %v", err)
		return
	}
	t.fails = 0
	token := theftNonce()
	mark, _ := json.Marshal(map[string]string{"token": token})
	// The data area's token first: a record without it would lock the phone.
	_ = os.MkdirAll(filepath.Dir(c.theftMarkPath()), 0o700)
	if err := os.WriteFile(c.theftMarkPath(), mark, 0o600); err != nil {
		c.log.Printf("anti-theft lock: %v", err)
		return
	}
	t.rec = &theftRecord{Account: bound.Account, Email: bound.Email, Device: device, Token: token, Since: c.now().Unix()}
	if err := c.saveTheft(); err != nil {
		c.log.Printf("anti-theft lock: %v", err)
		t.rec = nil
		return
	}
	t.armed = true
	c.log.Printf("anti-theft lock: armed for %s on %s", bound.Email, device)
}

// theftForReset sets the record for a factory reset: kept, and so the lock
// holds, when lock; else gone, and the server told the binding ended.
func (c *Core) theftForReset(why string, lock bool) []func(context.Context) {
	t := &c.theft
	if t.rec == nil {
		return nil
	}
	if lock {
		t.rec.Locked = why
		if err := c.saveTheft(); err != nil {
			c.log.Printf("anti-theft lock: %v", err)
		}
		return nil
	}
	t.rec, t.armed = nil, false
	if err := c.saveTheft(); err != nil {
		c.log.Printf("anti-theft lock: %v", err)
	}
	if r := c.karioAccount(); r != nil {
		return []func(context.Context){c.theftUnbind(*r)}
	}
	return nil
}

// theftUnbind tells the server the phone is no longer bound to the account.
func (c *Core) theftUnbind(r accountRecord) func(context.Context) {
	return func(ctx context.Context) {
		pass, err := c.openPassword(r)
		if err == nil {
			user := strings.ToLower(r.User)
			var req antitheft.Request
			if req, err = c.theftRequest(ctx, antitheft.PurposeUnbind, user, ""); err == nil {
				err = c.theftClient().Unbind(ctx, user, pass, req)
			}
		}
		c.Post(func() {
			if err != nil {
				c.log.Printf("anti-theft lock: the server did not hear the binding ended: %v", err)
			} else {
				c.log.Printf("anti-theft lock: the server forgot the binding")
			}
		})
	}
}

// theftSignOut guards signing out of the Kario account while the lock is
// armed for it: the lock-screen PIN first (else whoever holds the phone
// unlocked could take the lock off, then erase it from the lock screen),
// then the record goes and the server hears of it.
func (c *Core) theftSignOut(r *accountRecord, then func(error)) {
	if r.Kind != ui.AccountKario || c.theft.rec == nil {
		then(nil)
		return
	}
	off := func() {
		rec := *r
		c.theft.rec, c.theft.armed = nil, false
		if err := c.saveTheft(); err != nil {
			c.log.Printf("anti-theft lock: %v", err)
		}
		_ = os.Remove(c.theftMarkPath())
		c.log.Printf("anti-theft lock: off (signed out)")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), theftTimeout)
			defer cancel()
			c.theftUnbind(rec)(ctx)
		}()
		then(nil)
	}
	if !c.lock.pin {
		off()
		return
	}
	c.askLockPIN("signout", 0, func(_ string, err error) {
		if err != nil {
			then(err)
			return
		}
		off()
	})
}

func (c *Core) theftClient() *antitheft.Client {
	return &antitheft.Client{Server: c.cfg.AccountURL, HTTP: &http.Client{Timeout: theftTimeout}}
}

// theftRequest is a request for purpose: the server's challenge, and the
// device's attestation over the nonce.
func (c *Core) theftRequest(ctx context.Context, purpose, account, own string) (antitheft.Request, error) {
	ch, err := c.theftClient().Challenge(ctx)
	if err != nil {
		return antitheft.Request{}, err
	}
	var att ui.Attestation
	if err := c.broker.rpc(svc.Keystored, svc.KeyAttest, map[string]string{"app": antitheft.App, "nonce": antitheft.Nonce(purpose, account, ch, own)}, &att); err != nil {
		return antitheft.Request{}, fmt.Errorf("the key service: %w", err)
	}
	return antitheft.Request{Challenge: ch, Account: account, Nonce: own, Attestation: &att}, nil
}

func theftNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// theftUnlocked ends the lock: the record goes, the phone is the new
// owner's to set up.
func (c *Core) theftUnlocked(how string) {
	t := &c.theft
	t.locked, t.rec = false, nil
	if err := c.saveTheft(); err != nil {
		c.log.Printf("anti-theft lock: %v", err)
	}
	for i := len(c.overlays) - 1; i >= 0; i-- {
		if o, ok := c.overlays[i].(*theftOverlay); ok {
			c.popOverlay(o)
		}
	}
	c.log.Printf("anti-theft lock: released (%s)", how)
	c.showToast(tr.T("The anti-theft lock is off: the phone is yours to set up"))
}

// theftOverlay is the locked phone's system screen: a code sent to the
// account's address, typed in, releases it; so does the owner removing the
// phone on the account's page (OK checks). Emergency numbers can be called
// from it. Nothing else of the phone is reachable behind it.
type theftOverlay struct {
	digits string
	sent   string // where the code went, masked
	busy   bool
	note   string
	bad    bool
}

func (o *theftOverlay) key(c *Core, k Key) {
	if o.busy {
		return
	}
	switch k.Name {
	case KeyBackspace:
		if o.digits != "" {
			o.digits = o.digits[:len(o.digits)-1]
		}
	case KeyRunes:
		for _, r := range c.fnRunes(k.Runes) {
			if r >= '0' && r <= '9' && len(o.digits) < antitheft.CodeLen {
				o.digits += string(r)
			}
		}
	case KeyTab:
		o.send(c)
	case KeyEnter:
		if len(o.digits) == antitheft.CodeLen {
			o.release(c)
		} else {
			o.check(c)
		}
	case KeyCall:
		if c.emergencyNumber(o.digits) {
			n := o.digits
			o.digits = ""
			c.emergencyCall(n)
			return
		}
		o.note, o.bad = tr.T("Only emergency numbers can be called while locked"), true
	case KeyF4:
		c.hangupCellCall()
	}
}

// ask runs one request to the server off the loop; done hears its outcome
// on the loop.
func (o *theftOverlay) ask(c *Core, purpose string, run func(ctx context.Context, cl *antitheft.Client, req antitheft.Request) error, done func(error)) {
	rec := c.theft.rec
	if rec == nil || !c.theftAvailable() {
		o.note, o.bad = tr.T("This phone has no Kario account server: it cannot be released here"), true
		return
	}
	o.busy, o.note, o.bad = true, tr.T("Asking the Kario account server…"), false
	nonce := theftNonce()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), theftTimeout)
		defer cancel()
		req, err := c.theftRequest(ctx, purpose, rec.Account, nonce)
		if err == nil {
			err = run(ctx, c.theftClient(), req)
		}
		c.Post(func() {
			o.busy = false
			done(err)
		})
	}()
}

func (o *theftOverlay) failed(err error) {
	var se *antitheft.StatusError
	switch {
	case errors.As(err, &se) && se.Code == http.StatusUnauthorized:
		o.note = tr.T("The code is wrong or has expired")
	case errors.As(err, &se) && se.Code == http.StatusTooManyRequests:
		o.note = tr.T("Too many codes asked for; try again later")
	default:
		o.note = tr.T("The Kario account server cannot be reached: %v", err)
	}
	o.bad = true
}

func (o *theftOverlay) send(c *Core) {
	var to string
	o.ask(c, antitheft.PurposeCode, func(ctx context.Context, cl *antitheft.Client, req antitheft.Request) (err error) {
		to, err = cl.SendCode(ctx, req)
		return err
	}, func(err error) {
		if err != nil {
			o.failed(err)
			return
		}
		o.sent, o.digits = to, ""
		o.note, o.bad = tr.T("A code went to %s", to), false
	})
}

// verified takes a release the server signed: for this phone, the account
// it was bound to and this attempt, under the product's key.
func (o *theftOverlay) verified(c *Core, s antitheft.Signed, req antitheft.Request) error {
	key, err := familyKey(c.cfg.FamilyKey)
	if err != nil {
		return err
	}
	_, err = antitheft.Verify(key, s, req.Attestation.DeviceID, req.Account, req.Nonce)
	return err
}

func (o *theftOverlay) release(c *Core) {
	code := o.digits
	var signed antitheft.Signed
	var asked antitheft.Request
	o.ask(c, antitheft.PurposeRelease, func(ctx context.Context, cl *antitheft.Client, req antitheft.Request) (err error) {
		req.Code, asked = code, req
		signed, err = cl.Release(ctx, req)
		return err
	}, func(err error) {
		o.digits = ""
		if err == nil {
			err = o.verified(c, signed, asked)
			if err != nil {
				o.note, o.bad = tr.T("The server's answer does not check out: %v", err), true
				return
			}
			c.theftUnlocked("the account's code")
			return
		}
		o.failed(err)
	})
}

func (o *theftOverlay) check(c *Core) {
	var st antitheft.Status
	var asked antitheft.Request
	o.ask(c, antitheft.PurposeStatus, func(ctx context.Context, cl *antitheft.Client, req antitheft.Request) (err error) {
		asked = req
		st, err = cl.Status(ctx, req)
		return err
	}, func(err error) {
		switch {
		case err != nil:
			o.failed(err)
		case st.Release != nil:
			if err := o.verified(c, *st.Release, asked); err != nil {
				o.note, o.bad = tr.T("The server's answer does not check out: %v", err), true
				return
			}
			c.theftUnlocked("removed from the account")
		default:
			o.note, o.bad = tr.T("Still locked to %s: send a code there, or remove the phone on the account's page", c.theft.rec.Email), true
		}
	})
}

func (o *theftOverlay) lines(c *Core) (lines []string, note string, bad bool) {
	email := "?"
	if rec := c.theft.rec; rec != nil {
		email = rec.Email
	}
	lines = append(lines, tr.T("This phone was erased without its owner's PIN. It was signed in to the Kario account %s: sign in to that account to use it.", email))
	if o.sent != "" {
		lines = append(lines, tr.T("Enter the code sent to %s.", o.sent))
	} else {
		lines = append(lines, tr.T("The left soft key sends a sign-in code to that address."))
	}
	lines = append(lines, tr.T("Removed the phone on the account's page? Press OK to check."))
	if call := c.liveCell(); call != nil {
		lines = append(lines, tr.T("A call is going on with %s: the end key hangs up.", call.Number))
	}
	return lines, o.note, o.bad
}

func (o *theftOverlay) footer() []string {
	return []string{tr.T("Left soft key: send a code · Digits: the code · OK: confirm"), tr.T("Call key: emergency numbers only")}
}

func (o *theftOverlay) frame(c *Core) *FrameOverlay {
	lines, note, bad := o.lines(c)
	return &FrameOverlay{Kind: "auth", Title: tr.T("Anti-theft lock"), Lines: lines, PIN: [2]int{len(o.digits), antitheft.CodeLen},
		Note: note, NoteBad: bad, Footer: o.footer()}
}

func (o *theftOverlay) render(c *Core, can *cv.Canvas) {
	lines, note, bad := o.lines(c)
	W, H := c.cfg.W, c.cfg.H
	bs := st(colFG, colTrustBG)
	can.Box(0, 1, W, H-1, st(colTrusted, colTrustBG), true)
	can.Text(2, 2, "■ "+tr.T("Anti-theft lock"), bold(st(colTrusted, colTrustBG)), W-4)
	y := 4
	for _, l := range lines {
		for _, w := range cv.Wrap(l, W-6) {
			can.Text(3, y, w, bs, W-6)
			y++
		}
	}
	y++
	can.Text(3, y, o.digits+strings.Repeat("_", max(0, antitheft.CodeLen-len(o.digits))), bold(bs), W-6)
	y += 2
	if note != "" {
		sty := st(colAccent, colTrustBG)
		if bad {
			sty = bold(st(colDanger, colTrustBG))
		}
		for _, w := range cv.Wrap(note, W-6) {
			can.Text(3, y, w, sty, W-6)
			y++
		}
	}
	for i, f := range o.footer() {
		can.Text(3, H-2-2+i, cv.Truncate(f, W-6), st(colDim, colTrustBG), W-6)
	}
}
