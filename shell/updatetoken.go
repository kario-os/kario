package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Restricted bundles (25-system-update.md §9): the update server serves
// a bundle latest.json marks restricted (one that carries closed
// third-party firmware) only with a download token, which the phone buys
// by its device attestation (ota.BundleApp). Such a bundle always
// downloads through the install proxy (updateproxy.go), which holds the
// token: the first is bought before RAUC starts, the next within
// bundleTokenRefresh of its expiry and after the server refused the one
// held (it restarted, which voids every token). A server that will not
// give this phone a token (403) fails the install as a bundle for another
// device; any other trouble getting one goes as a download that failed.

const bundleTokenRefresh = 5 * time.Minute

// errNotForDevice: the update server does not give this phone the
// restricted bundle.
var errNotForDevice = errors.New("the update server does not give this phone the bundle")

// bundleError: the shell did not get the restricted bundle to RAUC (no
// token, no proxy).
type bundleError struct{ err error }

func (e *bundleError) Error() string { return "the restricted bundle: " + e.err.Error() }
func (e *bundleError) Unwrap() error { return e.err }

func bundleFailed(err error) bool {
	var b *bundleError
	return errors.As(err, &b)
}

// tokenFetch buys a download token through cl: the token and how long it
// is good for.
type tokenFetch func(ctx context.Context, cl *http.Client) (string, time.Duration, error)

// bundleTokens is the install proxy's token for its bundle.
type bundleTokens struct {
	fetch tokenFetch
	cl    *http.Client
	now   func() time.Time
	mu    sync.Mutex
	token string
	renew time.Time
	// refused: the server would not give this phone a token.
	refused bool
}

// get is the token held, or a new one once it is due for renewal; none
// once the server refused this phone.
func (t *bundleTokens) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.refused:
		return "", errNotForDevice
	case t.token != "" && t.now().Before(t.renew):
		return t.token, nil
	}
	tok, ttl, err := t.fetch(ctx, t.cl)
	if err != nil {
		if errors.Is(err, errNotForDevice) {
			t.refused = true
		}
		return "", err
	}
	t.token, t.renew = tok, t.now().Add(ttl-min(bundleTokenRefresh, ttl/2))
	return tok, nil
}

// drop forgets tok, which the server refused, unless another took its
// place meanwhile.
func (t *bundleTokens) drop(tok string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token == tok {
		t.token = ""
	}
}

func (t *bundleTokens) wasRefused() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refused
}

// bundleToken buys the download token of the restricted bundle at
// bundleURL from the server it is on (the bundle's directory, as
// kario-otasrv serves it): the server's challenge, the keystore's
// attestation for ota.BundleApp over it and the bundle's name, the token.
// Off the loop, as attestReport.
func (c *Core) bundleToken(ctx context.Context, cl *http.Client, bundleURL string) (string, time.Duration, error) {
	if c.broker == nil {
		return "", 0, errors.New("no keystore to attest this device")
	}
	u, err := url.Parse(bundleURL)
	if err != nil {
		return "", 0, err
	}
	bundle := path.Base(u.Path)
	srv := url.URL{Scheme: u.Scheme, User: u.User, Host: u.Host, Path: path.Dir(u.Path)}
	base := strings.TrimSuffix(srv.String(), "/")
	var ch ota.BundleChallenge
	if err := postBundleJSON(ctx, cl, base+ota.BundleChallengePath, struct{}{}, &ch); err != nil {
		return "", 0, err
	}
	var att ui.Attestation
	if err := c.broker.rpc(svc.Keystored, svc.KeyAttest, map[string]string{"app": ota.BundleApp, "nonce": ota.BundleNonce(ch.Challenge, bundle)}, &att); err != nil {
		return "", 0, fmt.Errorf("the device's attestation: %w", err)
	}
	var tok ota.BundleToken
	if err := postBundleJSON(ctx, cl, base+ota.BundleTokenPath, ota.BundleTokenRequest{Bundle: bundle, Challenge: ch.Challenge, Attestation: &att}, &tok); err != nil {
		return "", 0, err
	}
	if tok.Token == "" || tok.ExpiresIn <= 0 {
		return "", 0, errors.New("update server: no download token in its answer")
	}
	ttl := time.Duration(tok.ExpiresIn) * time.Second
	c.log.Printf("system update: a download token for %s, good for %s", bundle, ttl)
	return tok.Token, ttl, nil
}

// postBundleJSON posts in and reads the answer into out; a 403 is
// errNotForDevice.
func postBundleJSON(ctx context.Context, cl *http.Client, to string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (%s: %s)", errNotForDevice, to, firstLine(strings.TrimSpace(string(raw)), 200))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("update server: %s: %s", to, resp.Status)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("update server: %s: %w", to, err)
	}
	return nil
}
