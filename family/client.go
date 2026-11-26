package family

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

// The phone's side of the account server's API, under /v1/family/device;
// every request but Pair carries the token Pair gave:
//
//	POST   /v1/family/device/pair      Pair -> 201 Paired
//	GET    /v1/family/device           -> State
//	POST   /v1/family/device/confirm   Confirm -> 204
//	GET    /v1/family/device/policy    -> Signed, 204 none yet
//	PUT    /v1/family/device/report    Report -> 204
//	POST   /v1/family/device/location  Location -> 204
//	DELETE /v1/family/device           -> 204 (the phone ends the binding)
//	POST   /v1/family/device/requests  Ask -> 201 (200: the same ID again)
//	GET    /v1/family/device/requests  -> []AskState (today's)
//	POST   /v1/family/device/reset     ResetAsk -> 201
//	GET    /v1/family/device/reset     -> SignedReset, 204 none confirmed
//	POST   /v1/family/device/reset/done ResetAsk -> 204 (the new PIN is set)
//	DELETE /v1/family/device/reset     -> 204 (the phone withdraws it)
//
// 401, 404 and 410 are ErrGone: the binding (or the pairing) is no more.
type Client struct {
	Server string
	Token  string
	HTTP   *http.Client
	// Date is the server's clock in its last answer (its Date header),
	// zero before one.
	Date time.Time
}

// ErrGone: the server no longer knows the phone's binding.
var ErrGone = errors.New("family: the binding is gone")

// StatusError is any other answer that is not a success.
type StatusError struct {
	Code       int
	Msg        string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("family: the server answered %d %s", e.Code, e.Msg)
}

// ServerOK: the token may go to the server: HTTPS, or plain HTTP to this
// machine or a private network's address (development, the selftest).
func ServerOK(server string) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return fmt.Errorf("family: %q is not a server address", server)
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsPrivate())) {
		return nil
	}
	return errors.New("family: the account server must be HTTPS")
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	if err := ServerOK(c.Server); err != nil {
		return 0, err
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.Server, "/")+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
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
		return 0, fmt.Errorf("family: %w", err)
	}
	defer resp.Body.Close()
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		c.Date = d
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return resp.StatusCode, ErrGone
	case resp.StatusCode/100 != 2:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		se := &StatusError{Code: resp.StatusCode, Msg: e.Error}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			se.RetryAfter = time.Duration(s) * time.Second
		}
		return resp.StatusCode, se
	case out != nil && resp.StatusCode != http.StatusNoContent:
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("family: the server's answer does not parse: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// Pair starts a pairing; the client takes the token it gets.
func (c *Client) Pair(ctx context.Context, p Pair) (Paired, error) {
	c.Token = ""
	var out Paired
	if _, err := c.do(ctx, http.MethodPost, "/v1/family/device/pair", p, &out); err != nil {
		return Paired{}, err
	}
	if out.Device == "" || !strings.HasPrefix(out.Token, TokenPrefix) || len(out.Code) != CodeLen {
		return Paired{}, errors.New("family: the server's pairing answer is incomplete")
	}
	c.Token = out.Token
	return out, nil
}

func (c *Client) State(ctx context.Context) (State, error) {
	var out State
	_, err := c.do(ctx, http.MethodGet, "/v1/family/device", nil, &out)
	return out, err
}

func (c *Client) Confirm(ctx context.Context, allow bool) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/family/device/confirm", Confirm{Allow: allow, Consent: ConsentVersion}, nil)
	return err
}

// Policy is the latest signed policy, nil while the parent set none.
func (c *Client) Policy(ctx context.Context) (*Signed, error) {
	var out Signed
	code, err := c.do(ctx, http.MethodGet, "/v1/family/device/policy", nil, &out)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Report(ctx context.Context, r Report) error {
	_, err := c.do(ctx, http.MethodPut, "/v1/family/device/report", r, nil)
	return err
}

func (c *Client) Location(ctx context.Context, l Location) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/family/device/location", l, nil)
	return err
}

// Unbind ends the binding from the phone; one the server no longer has is
// ended too.
func (c *Client) Unbind(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/family/device", nil, nil)
	if errors.Is(err, ErrGone) {
		return nil
	}
	return err
}

// Ask sends a request for more time; sending the same one again is no
// second request.
func (c *Client) Ask(ctx context.Context, a Ask) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/family/device/requests", a, nil)
	return err
}

// Asks are today's requests and their answers.
func (c *Client) Asks(ctx context.Context) ([]AskState, error) {
	var out []AskState
	_, err := c.do(ctx, http.MethodGet, "/v1/family/device/requests", nil, &out)
	return out, err
}

// AskReset asks the parent to confirm a parental PIN reset.
func (c *Client) AskReset(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/family/device/reset", ResetAsk{ID: id}, nil)
	return err
}

// Reset is the signed reset the parent confirmed, nil while none is.
func (c *Client) Reset(ctx context.Context) (*SignedReset, error) {
	var out SignedReset
	code, err := c.do(ctx, http.MethodGet, "/v1/family/device/reset", nil, &out)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &out, nil
}

// ResetDone tells the server the new PIN is set.
func (c *Client) ResetDone(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/family/device/reset/done", ResetAsk{ID: id}, nil)
	return err
}

func (c *Client) CancelReset(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/family/device/reset", nil, nil)
	return err
}
