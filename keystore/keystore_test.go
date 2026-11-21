package keystore

import (
	"errors"
	"github.com/kario-os/kario/push"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestConfirmChainVerifies(t *testing.T) {
	s, dir := open(t)
	root, err := LoadRootPub(RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.Confirm("com.phone.wallet", "pay", "", "Pay Morning Light Cafe ¥18.00", `{"amount":1800}`, DefaultPIN)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyConfirm(root, p, "com.phone.wallet", "pay", "Pay Morning Light Cafe ¥18.00", `{"amount":1800}`); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	cases := map[string]func() error{
		"different prompt shown": func() error {
			return VerifyConfirm(root, p, "com.phone.wallet", "pay", "Pay Morning Light Cafe ¥1,800.00", `{"amount":1800}`)
		},
		"different payload": func() error {
			return VerifyConfirm(root, p, "com.phone.wallet", "pay", "Pay Morning Light Cafe ¥18.00", `{"amount":180000}`)
		},
		"other app": func() error {
			return VerifyConfirm(root, p, "com.evil.app", "pay", "Pay Morning Light Cafe ¥18.00", `{"amount":1800}`)
		},
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s: tampered proof accepted", name)
		}
	}
	other, _ := open(t)
	q, _, _ := other.Confirm("com.phone.wallet", "pay", "", "x", "y", DefaultPIN)
	if VerifyConfirm(root, q, "com.phone.wallet", "pay", "x", "y") == nil {
		t.Error("proof from a device not issued by this root accepted")
	}
}

func TestSignKeyIsNotUserAuth(t *testing.T) {
	s, dir := open(t)
	root, _ := LoadRootPub(RootPubPath(dir))
	p, err := s.Sign("com.phone.wallet", "paycode", "", "PAYCODE|1")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySign(root, p, "com.phone.wallet", "paycode", "PAYCODE|1"); err != nil {
		t.Fatal(err)
	}
	if VerifyConfirm(root, p, "com.phone.wallet", "paycode", "", "PAYCODE|1") == nil {
		t.Error("a key without user auth passed as user-confirmed")
	}
	if _, _, err := s.Confirm("com.phone.wallet", "paycode", "", "p", "x", DefaultPIN); err == nil {
		t.Error("same alias reused with a different user-auth policy")
	}
}

func TestPINLockout(t *testing.T) {
	s, _ := open(t)
	for i := 1; i <= maxFailures; i++ {
		_, left, err := s.Confirm("a.b", "pay", "", "p", "x", "000000")
		if !errors.Is(err, ErrWrongPIN) || left != maxFailures-i {
			t.Fatalf("attempt %d: err=%v left=%d", i, err, left)
		}
	}
	if _, _, err := s.Confirm("a.b", "pay", "", "p", "x", DefaultPIN); !errors.Is(err, ErrLocked) {
		t.Fatalf("correct PIN after lockout: %v", err)
	}
}

func TestAttestation(t *testing.T) {
	s, dir := open(t)
	root, _ := LoadRootPub(RootPubPath(dir))
	a, err := s.Attest("com.phone.wallet", "n1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAttestation(root, a, "com.phone.wallet", "n1"); err != nil {
		t.Fatal(err)
	}
	if VerifyAttestation(root, a, "com.phone.wallet", "n2") == nil {
		t.Error("replayed attestation accepted for a new nonce")
	}
	if _, err := s.Sign("../etc", "k", "", "x"); err == nil {
		t.Error("path traversal in app id accepted")
	}
}

func TestPushKeys(t *testing.T) {
	dir := t.TempDir()
	ks, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ks.PushKey("com.example.a")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := ks.PushKey("com.example.a"); again != a {
		t.Fatal("push key not kept")
	}
	if b, _ := ks.PushKey("com.example.b"); b == a {
		t.Fatal("two apps share a push key")
	}
	sealed, err := push.Seal(a, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := ks.OpenPush("com.example.a", sealed); err != nil || string(plain) != "secret" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := ks.OpenPush("com.example.b", sealed); err == nil {
		t.Fatal("another app opened the data")
	}
	if _, err := ks.PushKey("../evil"); err == nil {
		t.Fatal("path in app id accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "apps")); err == nil {
		t.Fatal("key material under an app directory")
	}
}

// cafeOrder is a mini-program named in Chinese (Morning Light Cafe ordering):
// identities keep any UTF-8 and limit characters, not bytes.
const cafeOrder = "com.phone.miniapp/\u6668\u5149\u5496\u5561\u70b9\u9910"

func TestMiniProgramKeys(t *testing.T) {
	s, dir := open(t)
	root, _ := LoadRootPub(RootPubPath(dir))
	host, err := s.Sign("com.phone.miniapp", "pay", "", "x")
	if err != nil {
		t.Fatal(err)
	}
	mp, err := s.Sign(cafeOrder, "pay", "", "x")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.Sign("com.phone.miniapp/Another mini-program", "pay", "", "x")
	if mp.AppPub == host.AppPub || mp.AppPub == other.AppPub {
		t.Fatal("a mini-program shares a key with its host or another mini-program")
	}
	if err := VerifySign(root, mp, cafeOrder, "pay", "x"); err != nil {
		t.Fatal(err)
	}
	if VerifySign(root, mp, "com.phone.miniapp", "pay", "x") == nil {
		t.Fatal("a mini-program's signature passed as the host's")
	}
	if again, _ := s.Sign(cafeOrder, "pay", "", "y"); again.AppPub != mp.AppPub {
		t.Fatal("mini-program key not kept")
	}
	for _, bad := range []string{"../cafe", "./x", "com.phone.miniapp/", "com.phone.miniapp/a/b", "com.phone.miniapp/\x00", "../x/cafe", "com.phone.miniapp/" + strings.Repeat("\u957f", MaxOrigin+1)} {
		if _, err := s.Sign(bad, "pay", "", "x"); err == nil {
			t.Errorf("identity %q accepted", bad)
		}
	}
	a, err := s.Attest(cafeOrder, "n", "")
	if err != nil || VerifyAttestation(root, a, cafeOrder, "n") != nil {
		t.Fatalf("mini-program attestation: %v", err)
	}
}

func TestForget(t *testing.T) {
	s, _ := open(t)
	k1, _ := s.Sign("com.example.a", "k", "", "x")
	mp1, _ := s.Sign("com.example.a/mini", "k", "", "x")
	push1, _ := s.PushKey("com.example.a")
	keep, _ := s.Sign("com.example.b", "k", "", "x")
	if err := s.Forget("com.example.a"); err != nil {
		t.Fatal(err)
	}
	if k2, _ := s.Sign("com.example.a", "k", "", "x"); k2.AppPub == k1.AppPub {
		t.Error("app key survived Forget")
	}
	if mp2, _ := s.Sign("com.example.a/mini", "k", "", "x"); mp2.AppPub == mp1.AppPub {
		t.Error("mini-program key survived Forget")
	}
	if push2, _ := s.PushKey("com.example.a"); push2 == push1 {
		t.Error("push key survived Forget")
	}
	if again, _ := s.Sign("com.example.b", "k", "", "x"); again.AppPub != keep.AppPub {
		t.Error("Forget touched another app")
	}
	if s.Forget("../x") == nil || s.Forget("com.example.a/mini") == nil {
		t.Error("Forget accepted a path or a mini-program identity")
	}
}

func TestPushKeyRotation(t *testing.T) {
	s, _ := open(t)
	k1, _ := s.PushKey("com.example.a")
	old, _ := push.Seal(k1, []byte("before"))
	k2, err := s.RotatePush("com.example.a")
	if err != nil || k2 == k1 {
		t.Fatalf("rotate: %v", err)
	}
	if again, _ := s.PushKey("com.example.a"); again != k2 {
		t.Fatal("the new key is not current")
	}
	fresh, _ := push.Seal(k2, []byte("after"))
	for sealed, want := range map[string]string{old: "before", fresh: "after"} {
		if plain, err := s.OpenPush("com.example.a", sealed); err != nil || string(plain) != want {
			t.Fatalf("open %q: %q %v", want, plain, err)
		}
	}
	if _, err := s.RotatePush("com.example.a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenPush("com.example.a", old); err == nil {
		t.Fatal("data sealed two keys ago still opens")
	}
	if plain, err := s.OpenPush("com.example.a", fresh); err != nil || string(plain) != "after" {
		t.Fatal("the previous key no longer opens")
	}
	if err := s.Forget("com.example.a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenPush("com.example.a", fresh); err == nil {
		t.Fatal("previous push key survived Forget")
	}
}

func TestUnlock(t *testing.T) {
	s, dir := open(t)
	root, _ := LoadRootPub(RootPubPath(dir))
	k1, _ := s.Sign("com.example.a", "k", "", "x")
	p1, _ := s.PushKey("com.example.a")
	if err := s.SetUnlocked(true); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Attest("com.example.a", "n", "")
	if a.Boot != BootUnlocked || VerifyAttestation(root, a, "com.example.a", "n") != nil {
		t.Fatalf("unlocked attestation: boot %q", a.Boot)
	}
	if k2, _ := s.Sign("com.example.a", "k", "", "x"); k2.AppPub == k1.AppPub {
		t.Fatal("app key survived unlocking")
	}
	if p2, _ := s.PushKey("com.example.a"); p2 == p1 {
		t.Fatal("push key survived unlocking")
	}
	k3, _ := s.Sign("com.example.a", "k", "", "x")
	if err := s.SetUnlocked(false); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Attest("com.example.a", "n", ""); a.Boot != BootState {
		t.Fatalf("locked again: boot %q", a.Boot)
	}
	if k4, _ := s.Sign("com.example.a", "k", "", "x"); k4.AppPub == k3.AppPub {
		t.Fatal("a key made while unlocked survived locking")
	}
	// An unlocked device cannot pass itself off as locked.
	_ = s.SetUnlocked(true)
	forged, _ := s.Attest("com.example.a", "n3", "")
	forged.Boot = BootState
	if VerifyAttestation(root, forged, "com.example.a", "n3") == nil {
		t.Fatal("an attestation edited to say verified boot passed")
	}
}

func TestSM2AppKey(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := LoadRootPub(RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	prompt, payload := "Pay Morning Light Cafe ¥18.00", `{"amount":1800}`
	p, _, err := s.Confirm("com.phone.wallet", "pay-gm", AlgSM2, prompt, payload, DefaultPIN)
	if err != nil {
		t.Fatal(err)
	}
	if p.Alg != AlgSM2 || p.Digest != digestSM3("phone-confirm-v0", "com.phone.wallet", "pay-gm", prompt, payload) {
		t.Fatalf("not an SM2/SM3 proof: %+v", p)
	}
	if err := VerifyConfirm(root, p, "com.phone.wallet", "pay-gm", prompt, payload); err != nil {
		t.Fatal(err)
	}
	if err := VerifyConfirm(root, p, "com.phone.wallet", "pay-gm", prompt, `{"amount":18000}`); err == nil {
		t.Fatal("other amount verified")
	}
	forged := *p
	forged.Alg = ""
	if err := VerifyConfirm(root, &forged, "com.phone.wallet", "pay-gm", prompt, payload); err == nil {
		t.Fatal("SM2 proof accepted as Ed25519")
	}
	if _, _, err := s.Confirm("com.phone.wallet", "pay-gm", "", prompt, payload, DefaultPIN); err == nil {
		t.Fatal("SM2 key used as Ed25519")
	}
	q, err := s.Sign("com.phone.wallet", "code-gm", AlgSM2, "PAYCODE|1")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySign(root, q, "com.phone.wallet", "code-gm", "PAYCODE|1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign("com.phone.wallet", "x", "rsa", "p"); err == nil {
		t.Fatal("unknown algorithm accepted")
	}
}
