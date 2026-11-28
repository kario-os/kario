package push

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestToken(t *testing.T) {
	key := []byte("gateway key")
	tok := Token(key, "dev-1", "com.phone.wallet")
	d, a, err := ParseToken(key, tok)
	if err != nil || d != "dev-1" || a != "com.phone.wallet" {
		t.Fatalf("parse: %q %q %v", d, a, err)
	}
	if _, _, err := ParseToken([]byte("other key"), tok); err == nil {
		t.Fatal("token accepted under another gateway key")
	}
	forged := Token([]byte("guess"), "dev-1", "com.phone.chat")
	if _, _, err := ParseToken(key, forged); err == nil {
		t.Fatal("forged token accepted")
	}
	if _, _, err := ParseToken(key, "garbage"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestSealOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k", "app.key")
	k, err := KeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := KeyFile(path)
	if PublicKey(again) != PublicKey(k) {
		t.Fatal("app key not kept")
	}
	sealed, err := Seal(PublicKey(k), []byte(`{"text":"hello"}`))
	if err != nil || !IsSealed(sealed) {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(sealed, "hello") {
		t.Fatal("plaintext visible in sealed data")
	}
	plain, err := Open(k, sealed)
	if err != nil || string(plain) != `{"text":"hello"}` {
		t.Fatalf("open: %q %v", plain, err)
	}
	other, _ := KeyFile(filepath.Join(t.TempDir(), "other.key"))
	if _, err := Open(other, sealed); err == nil {
		t.Fatal("another app's key opened the data")
	}
	raw := []byte(sealed)
	raw[len(raw)-2] ^= 1
	if _, err := Open(k, string(raw)); err == nil {
		t.Fatal("tampered data opened")
	}
}

func TestTokenGenerations(t *testing.T) {
	key := []byte("k")
	if _, _, gen, err := ParseTokenGen(key, Token(key, "d", "a")); err != nil || gen != 0 {
		t.Fatalf("first install: gen %d %v", gen, err)
	}
	tok := TokenAt(key, "d", "a", 2)
	if d, a, gen, err := ParseTokenGen(key, tok); err != nil || d != "d" || a != "a" || gen != 2 {
		t.Fatalf("gen 2: %s %s %d %v", d, a, gen, err)
	}
	if tok == Token(key, "d", "a") {
		t.Fatal("generations share a token")
	}
}
