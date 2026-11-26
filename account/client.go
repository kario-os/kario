package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The phone's side of the account server's API:
//
//	POST   /v1/auth/code    {"email"} -> 202 CodeSent (429: too many, Retry-After)
//	POST   /v1/auth/device  SignIn -> 201 Device (401: a wrong or expired code,
//	                        with the tries it has left; 403 with "consent": no
//	                        account has the address and the user did not agree
//	                        to make one; 409: the account has all the app
//	                        passwords it may)
//	DELETE /v1/auth/device  (Basic: the user name, the phone's password) -> 204
//	                        (401: the password no longer signs in)
type Client struct {
	Server    string
	HTTP      *http.Client
	UserAgent string
}

// StatusError is an answer that is not a success. AttemptsLeft is a
// refused code's tries left (0: ask for a new one); Consent: no account
// has the address yet.
type StatusError struct {
	Code         int
	Msg          string
	RetryAfter   time.Duration
	AttemptsLeft int
	Consent      bool
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("account: the server answered %d %s", e.Code, e.Msg)
}

// NetError: the server could not be reached.
type NetError struct{ Err error }

func (e *NetError) Error() string {
	return "account: the server could not be reached: " + e.Err.Error()
}
func (e *NetError) Unwrap() error { return e.Err }

// WrongCode: the server refused the code (wrong, expired, or tried too
// often); left is how many tries it has left.
func WrongCode(err error) (left int, ok bool) {
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusUnauthorized {
		return se.AttemptsLeft, true
	}
	return 0, false
}

// NeedsConsent: no account has the address yet; the code holds for a
// sign-in that agrees to make one.
func NeedsConsent(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusForbidden && se.Consent
}

// TooMany: the server's limits; wait is its Retry-After (0: none given).
func TooMany(err error) (wait time.Duration, ok bool) {
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusTooManyRequests {
		return se.RetryAfter, true
	}
	return 0, false
}

// ServerOK: codes and passwords may go to the server: HTTPS, or plain
// HTTP to this machine or a private network's address (development, the
// selftest), as package carddav lets credentials go.
func ServerOK(server string) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return fmt.Errorf("account: %q is not a server address", server)
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())) {
		return nil
	}
	return errors.New("account: the account server must be HTTPS")
}

func (c *Client) do(ctx context.Context, method, path, user, pass string, in, out any) error {
	if err := ServerOK(c.Server); err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.Server, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &NetError{Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		var f Failure
		_ = json.Unmarshal(raw, &f)
		se := &StatusError{Code: resp.StatusCode, Msg: f.Error, Consent: f.Consent}
		if f.AttemptsLeft != nil {
			se.AttemptsLeft = *f.AttemptsLeft
		}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			se.RetryAfter = time.Duration(s) * time.Second
		}
		return se
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("account: the server's answer does not parse: %w", err)
		}
	}
	return nil
}

// RequestCode has the server send a sign-in code to the address.
func (c *Client) RequestCode(ctx context.Context, email string) (CodeSent, error) {
	var out CodeSent
	err := c.do(ctx, http.MethodPost, "/v1/auth/code", "", "", map[string]string{"email": email}, &out)
	return out, err
}

// SignIn signs the phone in with a code: the password the server made for
// it.
func (c *Client) SignIn(ctx context.Context, in SignIn) (Device, error) {
	var out Device
	if err := c.do(ctx, http.MethodPost, "/v1/auth/device", "", "", in, &out); err != nil {
		return Device{}, err
	}
	if out.Password == "" || out.Username == "" {
		return Device{}, errors.New("account: the server's answer has no password")
	}
	return out, nil
}

// SignOut revokes the phone's password, the one it signs in with.
func (c *Client) SignOut(ctx context.Context, user, pass string) error {
	return c.do(ctx, http.MethodDelete, "/v1/auth/device", user, pass, nil, nil)
}
