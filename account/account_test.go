package account_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/kario-os/kario/account"
	"github.com/kario-os/kario/account/accounttest"
)

func newServer(t *testing.T) (*accounttest.Server, *account.Client) {
	t.Helper()
	srv := accounttest.New()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, &account.Client{Server: ts.URL}
}

func wrong(code string) string {
	if code == "000000" {
		return "111111"
	}
	return "000000"
}

// A code to the address, the code back: the phone's own password, named
// after it, which signs in until the phone signs out with it.
func TestSignInAndOut(t *testing.T) {
	srv, c := newServer(t)
	srv.AddAccount("lucy@example.com", 0)
	ctx := context.Background()
	sent, err := c.RequestCode(ctx, " Lucy@Example.com ")
	if err != nil || sent.Sent != "email" || sent.ExpiresIn != int(account.CodeTTL.Seconds()) {
		t.Fatalf("code: %+v %v", sent, err)
	}
	code := srv.LastCode("lucy@example.com")
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
		t.Fatalf("code %q", code)
	}
	dev, err := c.SignIn(ctx, account.SignIn{Email: "lucy@example.com", Code: code, Device: "Kario (qemu-t9)"})
	if err != nil || dev.Name != "Kario (qemu-t9)" || dev.Username != "lucy@example.com" || dev.NewAccount ||
		!regexp.MustCompile(`^[a-z]{4}(-[a-z]{4}){3}$`).MatchString(dev.Password) {
		t.Fatalf("sign in: %+v %v", dev, err)
	}
	if !srv.Check("Lucy@example.com", dev.Password) {
		t.Fatal("the phone's password does not sign in")
	}
	if _, err := c.SignIn(ctx, account.SignIn{Email: "lucy@example.com", Code: code, Device: "again"}); err == nil {
		t.Fatal("a code twice")
	}
	if err := c.SignOut(ctx, "lucy@example.com", dev.Password); err != nil {
		t.Fatal(err)
	}
	if srv.Check("lucy@example.com", dev.Password) {
		t.Fatal("signed out, the password still signs in")
	}
	var se *account.StatusError
	if err := c.SignOut(ctx, "lucy@example.com", dev.Password); !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("signing out twice: %v", err)
	}
}

// A wrong code says how many tries are left; the last wrong one ends the
// code. Codes are limited; a new account needs the user's consent and the
// code holds for it; a full account is refused.
func TestCodeRefusals(t *testing.T) {
	srv, c := newServer(t)
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return now }
	srv.AddAccount("lucy@example.com", accounttest.MaxPasswords)
	ctx := context.Background()
	if _, err := c.RequestCode(ctx, "lucy@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RequestCode(ctx, "lucy@example.com"); err == nil {
		t.Fatal("a second code at once")
	} else if wait, ok := account.TooMany(err); !ok || wait != time.Minute {
		t.Fatalf("a second code at once: %v %v", wait, err)
	}
	code := srv.LastCode("lucy@example.com")
	for i := 1; i <= account.CodeAttempts; i++ {
		_, err := c.SignIn(ctx, account.SignIn{Email: "lucy@example.com", Code: wrong(code), Device: "phone"})
		if left, ok := account.WrongCode(err); !ok || left != account.CodeAttempts-i {
			t.Fatalf("wrong code %d: %d %v", i, left, err)
		}
	}
	if _, err := c.SignIn(ctx, account.SignIn{Email: "lucy@example.com", Code: code, Device: "phone"}); err == nil {
		t.Fatal("the right code after too many wrong ones")
	}
	now = now.Add(time.Minute)
	if _, err := c.RequestCode(ctx, "lucy@example.com"); err != nil {
		t.Fatal(err)
	}
	var se *account.StatusError
	_, err := c.SignIn(ctx, account.SignIn{Email: "lucy@example.com", Code: srv.LastCode("lucy@example.com"), Device: "phone"})
	if !errors.As(err, &se) || se.Code != 409 {
		t.Fatalf("a full account: %v", err)
	}

	if _, err := c.RequestCode(ctx, "new@example.com"); err != nil {
		t.Fatal(err)
	}
	code = srv.LastCode("new@example.com")
	if _, err := c.SignIn(ctx, account.SignIn{Email: "new@example.com", Code: code, Device: "phone"}); !account.NeedsConsent(err) {
		t.Fatalf("a new account without consent: %v", err)
	}
	dev, err := c.SignIn(ctx, account.SignIn{Email: "new@example.com", Code: code, Agree: true, Device: "phone"})
	if err != nil || !dev.NewAccount || !srv.HasAccount("new@example.com") {
		t.Fatalf("a new account: %+v %v", dev, err)
	}
	if _, err := c.SignIn(ctx, account.SignIn{Email: "new@example.com", Code: code, Device: ""}); err == nil {
		t.Fatal("no device name")
	}
}

// Codes and passwords go over HTTPS, or plain HTTP to this machine and
// private addresses only; a server down is told apart.
func TestServers(t *testing.T) {
	for server, ok := range map[string]bool{"https://account.example.cn": true, "http://127.0.0.1:8796": true, "http://192.168.1.5": true,
		"http://localhost:1": true, "http://account.example.cn": false, "ftp://127.0.0.1": false, "": false} {
		if err := account.ServerOK(server); (err == nil) != ok {
			t.Errorf("%q: %v", server, err)
		}
	}
	c := &account.Client{Server: "http://account.example.cn"}
	if _, err := c.RequestCode(context.Background(), "lucy@example.com"); err == nil {
		t.Fatal("a code over plain HTTP")
	}
	ts := httptest.NewServer(accounttest.New())
	ts.Close()
	c = &account.Client{Server: ts.URL}
	var ne *account.NetError
	if _, err := c.RequestCode(context.Background(), "lucy@example.com"); !errors.As(err, &ne) {
		t.Fatalf("a server down: %v", err)
	}
}
