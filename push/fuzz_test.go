package push

import (
	"bytes"
	"crypto/ecdh"
	"strings"
	"testing"
)

// FuzzSealed is push data as it reaches the phone: what opens is what was
// sealed, and nothing else opens.
func FuzzSealed(f *testing.F) {
	key, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{7}, 32))
	sealed, _ := Seal(PublicKey(key), []byte(`{"title":"hi"}`))
	f.Add(sealed, []byte("hello"))
	f.Add("e1.AAAA", []byte{})
	f.Fuzz(func(t *testing.T, s string, plain []byte) {
		Open(key, s)
		sealed, err := Seal(PublicKey(key), plain)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := Open(key, sealed); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%q sealed opens to %q, %v", plain, got, err)
		}
	})
}

// FuzzToken is a push token an app server hands the gateway.
func FuzzToken(f *testing.F) {
	key := []byte("gateway key")
	f.Add(TokenAt(key, "dev1", "com.example.chat", 2), "dev1", "com.example.chat", 2)
	f.Add("ZGV2.AAAA", "a\x00b", "c", 0)
	f.Fuzz(func(t *testing.T, tok, device, app string, gen int) {
		ParseTokenGen(key, tok)
		if strings.ContainsRune(device+app, 0) {
			return
		}
		d, a, g, err := ParseTokenGen(key, TokenAt(key, device, app, gen))
		if err == nil && (d != device || a != app || g != max(gen, 0)) {
			t.Fatalf("%q %q %d reads back as %q %q %d", device, app, gen, d, a, g)
		}
	})
}
