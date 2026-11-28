package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

// ErrRefused: the unit is not in factory mode (provisioned, or its
// keystore did not answer).
var ErrRefused = errors.New("factory mode is not available on the unit")

// Unit is a runner's client: the station's over the USB link
// ("10.71.0.1:7171"), the Shell's and root's tools' over the runner's
// socket ("unix:/run/phone/factory.sock").
type Unit struct {
	base string
	http *http.Client
}

// USBAddr is the unit's address on the USB link (docs/27-usb-typec.md
// §3.4), with the runner's port.
var USBAddr = fmt.Sprintf("10.71.0.1:%d", Port)

// Dial is a client of the runner at addr (nothing is reached until a call).
// It never goes through a proxy: the unit is on a link of its own.
func Dial(addr string) *Unit {
	if path, ok := strings.CutPrefix(addr, "unix:"); ok {
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}}
		return &Unit{base: "http://factory", http: &http.Client{Transport: tr}}
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &Unit{base: strings.TrimSuffix(addr, "/"), http: &http.Client{Transport: tr}}
}

// do sends a request and decodes the answer into out; a 403 is
// ErrRefused, another error status the runner's message.
func (u *Unit) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := u.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode != http.StatusOK {
		var e apiError
		_ = json.Unmarshal(raw, &e)
		if res.StatusCode == http.StatusForbidden {
			if out != nil && json.Unmarshal(raw, out) == nil && e.Error == "" {
				return res.StatusCode, nil
			}
			return res.StatusCode, fmt.Errorf("%w: %s", ErrRefused, e.Error)
		}
		return res.StatusCode, fmt.Errorf("runner: %s %s: %d %s", method, path, res.StatusCode, e.Error)
	}
	if out != nil {
		return res.StatusCode, json.Unmarshal(raw, out)
	}
	return res.StatusCode, nil
}

func timeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// Info is which unit this is.
func (u *Unit) Info() (UnitInfo, error) {
	ctx, cancel := timeout(time.Minute)
	defer cancel()
	var info UnitInfo
	_, err := u.do(ctx, "GET", PathUnit, nil, &info)
	return info, err
}

// Test runs the test on the unit; a refused one comes back as a report
// with Refused set.
func (u *Unit) Test(mode string, wait time.Duration) (*Report, error) {
	ctx, cancel := timeout(wait + 5*time.Minute)
	defer cancel()
	var rep Report
	if _, err := u.do(ctx, "POST", PathTest, TestRequest{Mode: mode, WaitS: int(wait / time.Second)}, &rep); err != nil {
		return nil, err
	}
	if rep.Schema != Schema {
		return nil, fmt.Errorf("the unit answered a report of schema %q, not %q", rep.Schema, Schema)
	}
	return &rep, nil
}

// CSR is the unit's keystore's csr.
func (u *Unit) CSR() (keystore.CSR, error) {
	ctx, cancel := timeout(time.Minute)
	defer cancel()
	var csr keystore.CSR
	_, err := u.do(ctx, "GET", PathCSR, nil, &csr)
	return csr, err
}

// Attest has the unit attest nonce as keystore.ProvisionApp.
func (u *Unit) Attest(nonce string) (*ui.Attestation, error) {
	ctx, cancel := timeout(time.Minute)
	defer cancel()
	var att ui.Attestation
	if _, err := u.do(ctx, "POST", PathAttest, map[string]string{"nonce": nonce}, &att); err != nil {
		return nil, err
	}
	return &att, nil
}

// InstallCerts hands the unit's keystore its certificates.
func (u *Unit) InstallCerts(c keystore.DeviceCerts) error {
	ctx, cancel := timeout(time.Minute)
	defer cancel()
	_, err := u.do(ctx, "POST", PathInstall, c, nil)
	return err
}

// Keystore is a unit's keystore as the station provisions it
// (docs/22-developer-api.md §11): on the line through the unit's runner
// over USB (*Unit), on a developer's machine through the keystore's
// socket (KeystoreSock). It is all the station needs of a unit to
// provision it.
type Keystore interface {
	CSR() (keystore.CSR, error)
	Attest(nonce string) (*ui.Attestation, error)
	InstallCerts(keystore.DeviceCerts) error
}

var (
	_ Keystore = (*Unit)(nil)
	_ Keystore = KeystoreSock("")
)

// KeystoreSock is a keystore reached through its socket.
type KeystoreSock string

func (k KeystoreSock) CSR() (keystore.CSR, error) { return keystore.ReadCSR(string(k)) }

func (k KeystoreSock) Attest(nonce string) (*ui.Attestation, error) {
	return keystore.AttestFor(string(k), nonce)
}

func (k KeystoreSock) InstallCerts(c keystore.DeviceCerts) error {
	return keystore.InstallCerts(string(k), c)
}

// Plan is what the factory screen runs.
func (u *Unit) Plan() (PlanInfo, error) {
	ctx, cancel := timeout(10 * time.Second)
	defer cancel()
	var p PlanInfo
	_, err := u.do(ctx, "GET", PathPlan, nil, &p)
	return p, err
}

// Record hands the runner an interactive check's result.
func (u *Unit) Record(r CheckResult) error {
	ctx, cancel := timeout(10 * time.Second)
	defer cancel()
	_, err := u.do(ctx, "POST", PathResult, r, nil)
	return err
}

// Microphone has the runner record and measure the microphone.
func (u *Unit) Microphone(loopback bool) (CheckResult, error) {
	ctx, cancel := timeout(30 * time.Second)
	defer cancel()
	var r CheckResult
	_, err := u.do(ctx, "POST", PathMicrophone, MicRequest{Loopback: loopback}, &r)
	return r, err
}

// Provision certifies a unit with a CA at hand, the way the station does
// it with the store's CA (docs/22-developer-api.md §11): csr, an
// attestation checked against it, the certificates installed.
func Provision(k Keystore, ca *keystore.CA, nonce string) (string, error) {
	csr, err := k.CSR()
	if err != nil {
		return "", err
	}
	att, err := k.Attest(nonce)
	if err != nil {
		return "", err
	}
	if err := keystore.VerifyPossession(csr, att, nonce); err != nil {
		return "", err
	}
	certs, err := ca.Certify(csr)
	if err != nil {
		return "", err
	}
	return csr.DeviceID, k.InstallCerts(certs)
}
