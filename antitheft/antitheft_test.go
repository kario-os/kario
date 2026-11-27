package antitheft_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/antitheft"
	"github.com/kario-os/kario/antitheft/antitheftest"
	"github.com/kario-os/kario/keyset"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

type phone struct {
	t  *testing.T
	ks *keystore.Store
	c  *antitheft.Client
}

func newPhone(t *testing.T, server string) *phone {
	ks, err := keystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &phone{t: t, ks: ks, c: &antitheft.Client{Server: server}}
}

// req is a request for purpose: a new challenge, the attestation over the
// nonce of it.
func (p *phone) req(purpose, account, own string) antitheft.Request {
	p.t.Helper()
	ch, err := p.c.Challenge(context.Background())
	if err != nil {
		p.t.Fatal(err)
	}
	att, err := p.ks.Attest(antitheft.App, antitheft.Nonce(purpose, account, ch, own), "")
	if err != nil {
		p.t.Fatal(err)
	}
	return antitheft.Request{Challenge: ch, Account: account, Nonce: own, Attestation: att}
}

func code(err error) int {
	var se *antitheft.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// A phone binds to the account its login and app password sign in to, and
// no other account can take it then. After a reset the server tells the
// phone it is bound, sends a code to the account's address, and signs a
// release for this phone, account and attempt for the right code only; the
// binding ends with it. A challenge holds once.
func TestBindAndRelease(t *testing.T) {
	srv := antitheftest.New()
	ts := httptest.NewServer(srv)
	defer ts.Close()
	acct := srv.AddAccount("Lei@Kario.test", "app-pass")
	other := srv.AddAccount("thief@kario.test", "thief-pass")
	p := newPhone(t, ts.URL)
	ctx := context.Background()

	if _, err := p.c.Bind(ctx, "lei@kario.test", "wrong", p.req(antitheft.PurposeBind, "lei@kario.test", "")); code(err) != 401 {
		t.Fatalf("a wrong password: %v", err)
	}
	if _, err := p.c.Bind(ctx, "lei@kario.test", "app-pass", p.req(antitheft.PurposeBind, "thief@kario.test", "")); code(err) != 403 {
		t.Fatalf("an attestation for another login: %v", err)
	}
	b, err := p.c.Bind(ctx, "lei@kario.test", "app-pass", p.req(antitheft.PurposeBind, "lei@kario.test", ""))
	if err != nil || b.Account != acct || b.Email != "l***i@kario.test" {
		t.Fatalf("bind: %+v %v", b, err)
	}
	if _, err := p.c.Bind(ctx, "thief@kario.test", "thief-pass", p.req(antitheft.PurposeBind, "thief@kario.test", "")); code(err) != 409 {
		t.Fatalf("another account took the phone: %v", err)
	}
	r := p.req(antitheft.PurposeStatus, acct, "n1")
	if st, err := p.c.Status(ctx, r); err != nil || !st.Bound || st.Release != nil {
		t.Fatalf("status: %+v %v", st, err)
	}
	if _, err := p.c.Status(ctx, r); code(err) != 403 {
		t.Fatalf("a challenge used twice: %v", err)
	}
	if st, err := p.c.Status(ctx, p.req(antitheft.PurposeStatus, other, "n1")); err != nil || st.Bound || st.Release != nil {
		t.Fatalf("status for an account it is not bound to: %+v %v", st, err)
	}
	if to, err := p.c.SendCode(ctx, p.req(antitheft.PurposeCode, acct, "n2")); err != nil || to != "l***i@kario.test" {
		t.Fatalf("code: %q %v", to, err)
	}
	c := srv.LastCode("lei@kario.test")
	wrong := p.req(antitheft.PurposeRelease, acct, "n3")
	wrong.Code = "000000"
	if c == wrong.Code {
		wrong.Code = "111111"
	}
	if _, err := p.c.Release(ctx, wrong); code(err) != 401 {
		t.Fatalf("a wrong code: %v", err)
	}
	right := p.req(antitheft.PurposeRelease, acct, "n3")
	right.Code = c
	s, err := p.c.Release(ctx, right)
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := p.ks.CSR()
	if _, err := antitheft.Verify(keyset.Set{srv.Pub}, s, id, acct, "n3"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][3]string{{"PH-000000000000", acct, "n3"}, {id, other, "n3"}, {id, acct, "n4"}, {id, acct, ""}} {
		if _, err := antitheft.Verify(keyset.Set{srv.Pub}, s, bad[0], bad[1], bad[2]); !errors.Is(err, antitheft.ErrRelease) {
			t.Fatalf("a release for %v: %v", bad, err)
		}
	}
	other2, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := antitheft.Verify(keyset.Set{other2}, s, id, acct, "n3"); !errors.Is(err, antitheft.ErrSignature) {
		t.Fatalf("another key: %v", err)
	}
	if _, ok := srv.Bound(id); ok {
		t.Fatal("the binding outlived the release")
	}
}

// The owner removing the phone on the account's page releases it without a
// code; a phone the account never had gets nothing that way.
func TestRemovedOnThePage(t *testing.T) {
	srv, err := antitheftest.Open(filepath.Join(t.TempDir(), "srv.json"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	acct := srv.AddAccount("lei@kario.test", "app-pass")
	p, stranger := newPhone(t, ts.URL), newPhone(t, ts.URL)
	ctx := context.Background()
	if _, err := p.c.Bind(ctx, "lei@kario.test", "app-pass", p.req(antitheft.PurposeBind, "lei@kario.test", "")); err != nil {
		t.Fatal(err)
	}
	id, _, _ := p.ks.CSR()
	if err := srv.Remove(id); err != nil {
		t.Fatal(err)
	}
	st, err := p.c.Status(ctx, p.req(antitheft.PurposeStatus, acct, "n1"))
	if err != nil || st.Bound || st.Release == nil {
		t.Fatalf("status after the removal: %+v %v", st, err)
	}
	if _, err := antitheft.Verify(keyset.Set{srv.Pub}, *st.Release, id, acct, "n1"); err != nil {
		t.Fatal(err)
	}
	if st, err := stranger.c.Status(ctx, stranger.req(antitheft.PurposeStatus, acct, "n1")); err != nil || st.Release != nil {
		t.Fatalf("a phone the account never had: %+v %v", st, err)
	}
}

// A previous owner's binding left on the server (BindTo) keeps the phone
// from another account, as kario-accountsrv answers it, until the owner
// removes it.
func TestBoundToAPreviousOwner(t *testing.T) {
	srv := antitheftest.New()
	ts := httptest.NewServer(srv)
	defer ts.Close()
	srv.AddAccount("lei@kario.test", "app-pass")
	p := newPhone(t, ts.URL)
	id, pub, _ := p.ks.CSR()
	if err := srv.BindTo(id, pub, "Previous@kario.test"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var se *antitheft.StatusError
	_, err := p.c.Bind(ctx, "lei@kario.test", "app-pass", p.req(antitheft.PurposeBind, "lei@kario.test", ""))
	if !errors.As(err, &se) || se.Code != 409 || se.Msg != "the phone is bound to another account: its owner removes it first" {
		t.Fatalf("bound to a previous owner: %v", err)
	}
	if err := srv.Remove(id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.Bind(ctx, "lei@kario.test", "app-pass", p.req(antitheft.PurposeBind, "lei@kario.test", "")); err != nil {
		t.Fatalf("after the previous owner removed it: %v", err)
	}
}

// The server keeps its state in its file: opened again (the selftest's
// simulated reboot) it has the binding and signs with the same key.
func TestServerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "srv.json")
	srv, err := antitheftest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	srv.AddAccount("lei@kario.test", "app-pass")
	p := newPhone(t, ts.URL)
	if _, err := p.c.Bind(context.Background(), "lei@kario.test", "app-pass", p.req(antitheft.PurposeBind, "lei@kario.test", "")); err != nil {
		t.Fatal(err)
	}
	ts.Close()
	again, err := antitheftest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := p.ks.CSR()
	if _, ok := again.Bound(id); !ok || !again.Pub.Equal(srv.Pub) {
		t.Fatalf("bound %v, same key %v", ok, again.Pub.Equal(srv.Pub))
	}
}

// A request names its phone only with that phone's key: another key
// claiming the phone's ID, another app or nonce do not pass; under the
// certifier's roots only a certificate from them does.
func TestCheckDevice(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	att, err := ks.Attest(antitheft.App, "n", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := antitheft.CheckDevice(nil, att, "n"); err != nil {
		t.Fatal(err)
	}
	if err := antitheft.CheckDevice(nil, att, "m"); err == nil {
		t.Fatal("another nonce passed")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	forged := *att
	forged.DevicePub = base64.StdEncoding.EncodeToString(pub)
	forged.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, keystore.AttestMsg(att.DeviceID, att.AppID, att.Nonce, att.Boot)))
	if err := antitheft.CheckDevice(nil, &forged, "n"); err == nil {
		t.Fatal("another key passed for the phone's ID")
	}
	other, _ := ks.Attest("com.example.app", "n", "")
	if err := antitheft.CheckDevice(nil, other, "n"); err == nil {
		t.Fatal("an app's attestation passed")
	}
	roots, err := keystore.LoadRootPub(keystore.RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := antitheft.CheckDevice(roots, att, "n"); err != nil {
		t.Fatal(err)
	}
	rpub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := antitheft.CheckDevice(&keystore.Roots{Ed25519: rpub}, att, "n"); err == nil {
		t.Fatal("a certificate from other roots passed")
	}
	if err := antitheft.CheckDevice(nil, &ui.Attestation{Alg: keystore.AlgSM2}, "n"); err == nil {
		t.Fatal("an SM2 attestation passed")
	}
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{"lei@kario.test": "l***i@kario.test", "ab@x.cn": "a***@x.cn", "a@x.cn": "a***@x.cn", "nope": "***"} {
		if got := antitheft.MaskEmail(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
