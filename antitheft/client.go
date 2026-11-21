package antitheft

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

// The phone's side of the account server's API, under /v1/antitheft/:
//
//	POST /v1/antitheft/challenge  -> Challenge
//	POST /v1/antitheft/bind       Request (Basic: the login, an app password) -> Bound
//	POST /v1/antitheft/unbind     Request (Basic) -> 204
//	POST /v1/antitheft/status     Request -> Status
//	POST /v1/antitheft/code       Request -> 202 CodeSent (429: too many)
//	POST /v1/antitheft/release    Request with the code -> Signed (401: wrong code)
//
// The account's own page removes a phone with its session (DELETE
// /v1/antitheft/devices/{device}); the phone does not use it.
type Client struct {
	Server string
	HTTP   *http.Client
}

// StatusError is an answer that is not a success.
type StatusError struct {
	Code       int
	Msg        string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("antitheft: the server answered %d %s", e.Code, e.Msg)
}

// ServerOK: credentials may go to the server: HTTPS, or plain HTTP to this
// machine or a private network's address (development, the selftest).
func ServerOK(server string) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return fmt.Errorf("antitheft: %q is not a server address", server)
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsPrivate())) {
		return nil
	}
	return errors.New("antitheft: the account server must be HTTPS")
}

func (c *Client) do(ctx context.Context, path, user, pass string, in, out any) error {
	if err := ServerOK(c.Server); err != nil {
		return err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.Server, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("antitheft: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		se := &StatusError{Code: resp.StatusCode, Msg: e.Error}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			se.RetryAfter = time.Duration(s) * time.Second
		}
		return se
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("antitheft: the server's answer does not parse: %w", err)
		}
	}
	return nil
}

// Challenge is a new one-time challenge for the next request.
func (c *Client) Challenge(ctx context.Context) (string, error) {
	var out Challenge
	if err := c.do(ctx, "/v1/antitheft/challenge", "", "", struct{}{}, &out); err != nil {
		return "", err
	}
	if out.Challenge == "" {
		return "", errors.New("antitheft: the server gave no challenge")
	}
	return out.Challenge, nil
}

// Bind binds the phone to the account the login and app password sign in to.
func (c *Client) Bind(ctx context.Context, user, pass string, r Request) (Bound, error) {
	var out Bound
	if err := c.do(ctx, "/v1/antitheft/bind", user, pass, r, &out); err != nil {
		return Bound{}, err
	}
	if out.Account == "" {
		return Bound{}, errors.New("antitheft: the server's answer names no account")
	}
	return out, nil
}

// Unbind ends the phone's binding to the account.
func (c *Client) Unbind(ctx context.Context, user, pass string, r Request) error {
	return c.do(ctx, "/v1/antitheft/unbind", user, pass, r, nil)
}

func (c *Client) Status(ctx context.Context, r Request) (Status, error) {
	var out Status
	err := c.do(ctx, "/v1/antitheft/status", "", "", r, &out)
	return out, err
}

// SendCode has the server send a sign-in code to the account's address.
func (c *Client) SendCode(ctx context.Context, r Request) (string, error) {
	var out CodeSent
	err := c.do(ctx, "/v1/antitheft/code", "", "", r, &out)
	return out.Email, err
}

// Release is the signed release for the code in r.
func (c *Client) Release(ctx context.Context, r Request) (Signed, error) {
	var out Signed
	err := c.do(ctx, "/v1/antitheft/release", "", "", r, &out)
	return out, err
}
