package shell

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/account"
	"github.com/kario-os/kario/ui"
)

// Signing in to the Kario account with a code (account.add with SendCode,
// then Code; package account): the account server sends a code to the
// address, and for the code back makes this phone a password of its own,
// an app password named after the phone, which the account's page lists
// and revokes like the others and which opens the sync (CardDAV, CalDAV,
// the anti-theft lock's binding) but not the account itself. From there it
// is an account like one signed in with a typed password: phone-keystored
// seals the password, and the record keeps its name (Device); nobody sees
// the password. A sign-in that fails after the server made the password
// revokes it. Signing out revokes it on the server, the anti-theft lock's
// binding ended first (it signs in with the same password); when the
// server cannot be told, the account goes from the phone all the same and
// Settings says to sign the phone out on the account's page
// (AccountState.Unrevoked) and, when the binding could not be ended either,
// to remove the phone from the anti-theft lock there (LockLeft). Nothing
// logs the address, the code or the password.

const (
	accountCodeTimeout = 30 * time.Second
	maxAccountDevice   = 64
)

func (c *Core) accountClient() *account.Client {
	return &account.Client{Server: c.cfg.AccountURL, HTTP: &http.Client{Timeout: accountCodeTimeout}, UserAgent: Brand + " Account"}
}

// accountDeviceName is the name the account's page gives this phone's
// password: the product's, or the system's and its SKU's.
func (c *Core) accountDeviceName() string {
	name := Brand
	switch {
	case c.profile != nil && c.profile.Product != "":
		name = c.profile.Product
	case c.cfg.SKU.ID != "":
		name = Brand + " (" + c.cfg.SKU.ID + ")"
	}
	for utf8.RuneCountInString(name) > maxAccountDevice {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	return name
}

// sendAccountCode has the Kario account server send a sign-in code to
// user, and answers ui.AccountCode.
func (c *Core) sendAccountCode(user string, reply func(any, error)) {
	a := &c.accounts
	a.addBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), accountCodeTimeout)
		defer cancel()
		sent, err := c.accountClient().RequestCode(ctx, user)
		c.Post(func() {
			a.addBusy = false
			if err != nil {
				c.log.Printf("accounts: no sign-in code from %s: %s", hostOf(c.cfg.AccountURL), accountErrLog(err))
				reply(nil, errors.New(firstNonEmpty(accountCodeText(err), accountProblemText(problemOther, accountErrLog(err)))))
				return
			}
			c.log.Printf("accounts: %s sent a sign-in code", hostOf(c.cfg.AccountURL))
			reply(ui.AccountCode{To: user, ExpiresIn: sent.ExpiresIn}, nil)
		})
	}()
}

// signInWithCode signs in to the Kario account with a code, off the loop:
// the password the server made for the phone, its name, and the account's
// user name.
func (c *Core) signInWithCode(ctx context.Context, user, code string, agree bool, device string) (pass, name, username string, err error) {
	d, err := c.accountClient().SignIn(ctx, account.SignIn{Email: user, Code: code, Agree: agree, Device: device})
	if err != nil {
		return "", "", "", err
	}
	return d.Password, d.Name, d.Username, nil
}

// revokeDevice has the server revoke the password it made for this phone,
// off the loop; one the server no longer takes (revoked on the page) is
// revoked already.
func (c *Core) revokeDevice(ctx context.Context, user, pass string) error {
	err := c.accountClient().SignOut(ctx, user, pass)
	var se *account.StatusError
	if errors.As(err, &se) && se.Code == http.StatusUnauthorized {
		return nil
	}
	return err
}

// revokeAfterFailure revokes a password the server made for a sign-in
// that then failed on the phone, in the background.
func (c *Core) revokeAfterFailure(user, pass string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), accountCodeTimeout)
		defer cancel()
		err := c.revokeDevice(ctx, user, pass)
		c.Post(func() {
			if err != nil {
				c.log.Printf("accounts: the password the server made for a failed sign-in is not revoked: %s", accountErrLog(err))
			}
		})
	}()
}

// signOutServer tells the Kario account server of a sign-out once the
// account is gone from the phone (rec is a copy): unbind (the anti-theft
// lock's, nil for none) first, then the password the server made for the
// phone (an account signed in with a code) revoked. It answers
// account.state when both are done: what the server could not be told is
// in it, the password to sign out on the account's page (Unrevoked) and
// the binding left there (LockLeft, which theftUnbind keeps).
func (c *Core) signOutServer(rec accountRecord, unbind func(context.Context) error, saved error, reply func(any, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), theftTimeout)
		defer cancel()
		if unbind != nil {
			_ = unbind(ctx)
		}
		var err error
		if rec.Device != "" {
			var pass string
			if pass, err = c.openPassword(rec); err == nil {
				err = c.revokeDevice(ctx, rec.User, pass)
			}
		}
		c.Post(func() {
			st := c.accountState()
			switch {
			case rec.Device == "":
			case err != nil:
				c.log.Printf("accounts: %s's password on %s is not revoked: %s", rec.ID, hostOf(c.cfg.AccountURL), accountErrLog(err))
				st.Unrevoked = rec.Device
			default:
				c.log.Printf("accounts: %s's password on %s revoked", rec.ID, hostOf(c.cfg.AccountURL))
			}
			reply(st, saved)
		})
	}()
}

// accountErrLog is an account error for the log: the server's status
// only, as its message may name the address.
func accountErrLog(err error) string {
	var se *account.StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("the server answered %d", se.Code)
	}
	return err.Error()
}

// accountCodeText says why a code was not sent or did not sign in, "" for
// an error that is not the account server's.
func accountCodeText(err error) string {
	var se *account.StatusError
	var ne *account.NetError
	switch {
	case account.NeedsConsent(err):
		return ui.ErrAccountConsent
	case errors.As(err, &ne):
		return tr.T("Cannot reach the Kario account server")
	case !errors.As(err, &se):
		return ""
	}
	if left, ok := account.WrongCode(err); ok {
		if left > 0 {
			return tr.T("The code is wrong. Tries left: %d", left)
		}
		return tr.T("The code is wrong, has expired or was tried too often: send a new one")
	}
	if wait, ok := account.TooMany(err); ok {
		if wait > 0 {
			return tr.T("Too many tries: try again in %d min", int(math.Ceil(wait.Minutes())))
		}
		return tr.T("Too many tries: try again later")
	}
	switch se.Code {
	case http.StatusBadRequest:
		return tr.T("The Kario account does not take this e-mail address")
	case http.StatusConflict:
		return tr.T("The Kario account has as many app-specific passwords as it may: revoke one or sign a phone out on the account's page")
	}
	return tr.T("The server answered with an error (%s)", fmt.Sprint(se.Code))
}

// deviceAuthText is why an account signed in with a code stopped syncing
// when the server refuses its password.
func deviceAuthText() string {
	return tr.T("The Kario account no longer takes this phone's sign-in (signed out on the account's page?): sign out and sign in again")
}

// codeArgsText checks a sign-in with a code before it goes to the server:
// why not, "" when it may.
func codeArgsText(kind, server, user string) string {
	switch {
	case kind != ui.AccountKario:
		return tr.T("Only the Kario account signs in with a code")
	case !strings.Contains(user, "@"):
		return tr.T("Enter the e-mail address of your Kario account")
	case account.ServerOK(server) != nil:
		return tr.T("The server is not on HTTPS: the password would go unencrypted")
	}
	return ""
}
