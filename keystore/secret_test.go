package keystore

import (
	"bytes"
	"strings"
	"testing"
)

func TestSecretSealOpen(t *testing.T) {
	s, dir := open(t)
	sealed, err := s.SealSecret("carddav:a1", []byte("abcd-efgh-ijkl-mnop"))
	if err != nil || !strings.HasPrefix(sealed, "s1.") || strings.Contains(sealed, "abcd") {
		t.Fatalf("%q %v", sealed, err)
	}
	if again, _ := s.SealSecret("carddav:a1", []byte("abcd-efgh-ijkl-mnop")); again == sealed {
		t.Error("the same secret sealed twice reads the same")
	}
	if got, err := s.OpenSecret("carddav:a1", sealed); err != nil || string(got) != "abcd-efgh-ijkl-mnop" {
		t.Errorf("%q %v", got, err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.OpenSecret("carddav:a1", sealed); err != nil || string(got) != "abcd-efgh-ijkl-mnop" {
		t.Errorf("after a restart: %q %v", got, err)
	}
	other, _ := open(t)
	flipped := []byte(sealed)
	flipped[len(flipped)-3] ^= 1
	for name, f := range map[string]func() ([]byte, error){
		"another purpose": func() ([]byte, error) { return s.OpenSecret("carddav:a2", sealed) },
		"another phone":   func() ([]byte, error) { return other.OpenSecret("carddav:a1", sealed) },
		"changed":         func() ([]byte, error) { return s.OpenSecret("carddav:a1", string(flipped)) },
		"not sealed":      func() ([]byte, error) { return s.OpenSecret("carddav:a1", "abcd-efgh-ijkl-mnop") },
		"short":           func() ([]byte, error) { return s.OpenSecret("carddav:a1", "s1.AAAA") },
	} {
		if got, err := f(); err != ErrNotSealed || got != nil {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	if _, err := s.SealSecret("carddav:a1", bytes.Repeat([]byte("x"), maxSecret+1)); err != ErrSecretTooBig {
		t.Errorf("too big: %v", err)
	}
	if _, err := s.SealSecret("", []byte("x")); err == nil {
		t.Error("no purpose")
	}
}
