// Package account is the phone's side of signing in to the Kario account
// with a code (docs/19-core-apps.md §1.1): the account server sends a code
// to the account's email address, and the code back gets this phone a
// password of its own for CardDAV and CalDAV. That password is an app
// password the server mints for the device and names after it, which the
// account's page lists and revokes like the ones the user makes, and which
// the phone revokes itself when it signs out. The account's own requests
// (export, deletion, minor mode's page) take a session, never an app
// password: the phone holds no more than it syncs with.
//
// client.go is the phone's side of the HTTP API; package accounttest is a
// server in memory for tests and the selftest.
package account

import "time"

// A code is CodeLen digits, good for CodeTTL and CodeAttempts tries, once
// (the server's rules; the phone only shows them).
const (
	CodeLen      = 6
	CodeTTL      = 10 * time.Minute
	CodeAttempts = 5
)

// CodeSent answers a code's request: how it went ("email") and for how
// many seconds it holds.
type CodeSent struct {
	Sent      string `json:"sent"`
	ExpiresIn int    `json:"expires_in"`
}

// SignIn is a phone's sign-in with a code. Agree is the user's consent to
// the account's privacy terms, which a new account needs (a sign-in
// without it to an address with no account is refused, and the code holds
// for the next); Device names the phone on the account's page.
type SignIn struct {
	Email  string `json:"email"`
	Code   string `json:"code"`
	Agree  bool   `json:"agree,omitempty"`
	Device string `json:"device"`
}

// Device answers a sign-in: the app password the server made for the
// phone (ID and Name as the account's page lists it), shown this once,
// and what CardDAV and CalDAV sign in with: the user name (the account's
// identifier) and the server. NewAccount: the sign-in made the account.
type Device struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Created    string `json:"created"`
	Password   string `json:"password"`
	Username   string `json:"username"`
	Server     string `json:"server"`
	NewAccount bool   `json:"new_account,omitempty"`
}

// Failure is the body of an answer that is not a success: what went
// wrong, for a refused code the tries it has left, and Consent when no
// account has the address yet.
type Failure struct {
	Error        string `json:"error"`
	AttemptsLeft *int   `json:"attempts_left,omitempty"`
	Consent      bool   `json:"consent,omitempty"`
}
