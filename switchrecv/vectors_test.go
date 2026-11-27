package switchrecv

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kario-os/kario/switchrecv/oldphone"
)

// vectors is testdata/switch-vectors.json, made by the app's tests with
// Node's OpenSSL.
type vectors struct {
	App struct {
		Priv string `json:"private_key_hex"`
		Pub  string `json:"public_key_hex"`
		B64  string `json:"public_key_b64url"`
	} `json:"app"`
	Receiver struct {
		Priv string `json:"private_key_hex"`
		Pub  string `json:"public_key_hex"`
		B64  string `json:"public_key_b64url"`
	} `json:"receiver"`
	Token      string `json:"token_hex"`
	TokenB64   string `json:"token_b64url"`
	PairingURL string `json:"pairing_url"`
	Pairing    struct {
		Host string `json:"host"`
		Port int    `json:"port"`
		SSID string `json:"ssid"`
		PSK  string `json:"psk"`
		Name string `json:"name"`
	} `json:"pairing"`
	Shared string `json:"shared_secret_hex"`
	HKDF   struct {
		OKM string `json:"okm_hex"`
	} `json:"hkdf"`
	Up        string     `json:"upstream_key_hex"`
	Down      string     `json:"downstream_key_hex"`
	Envelopes []envelope `json:"envelopes"`
	Invalid   []struct {
		Name     string `json:"name"`
		Key      string `json:"key"`
		AAD      string `json:"aad"`
		Envelope string `json:"envelope_hex"`
	} `json:"invalid"`
	Unauthorized struct {
		Status      int    `json:"status"`
		ContentType string `json:"content_type"`
		Body        string `json:"body"`
	} `json:"unauthorized"`
}

type envelope struct {
	Name      string            `json:"name"`
	Key       string            `json:"key"`
	Direction uint32            `json:"direction"`
	Counter   uint64            `json:"counter"`
	Method    string            `json:"method"`
	Target    string            `json:"target"`
	AAD       string            `json:"aad"`
	Headers   map[string]string `json:"headers"`
	Status    int               `json:"status"`
	Plain     string            `json:"plaintext_hex"`
	Nonce     string            `json:"nonce_hex"`
	Envelope  string            `json:"envelope_hex"`
}

func loadVectors(t testing.TB) vectors {
	raw, err := os.ReadFile("testdata/switch-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (v vectors) key(t testing.TB, which string) []byte {
	if which == "upstream" {
		return unhex(t, v.Up)
	}
	return unhex(t, v.Down)
}

// The keys both sides derive, from either side.
func TestVectorKeys(t *testing.T) {
	v := loadVectors(t)
	app, err := ecdh.X25519().NewPrivateKey(unhex(t, v.App.Priv))
	if err != nil {
		t.Fatal(err)
	}
	rcv, err := ecdh.X25519().NewPrivateKey(unhex(t, v.Receiver.Priv))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(app.PublicKey().Bytes()) != v.App.Pub || b64.EncodeToString(app.PublicKey().Bytes()) != v.App.B64 {
		t.Fatal("app public key")
	}
	if hex.EncodeToString(rcv.PublicKey().Bytes()) != v.Receiver.Pub || b64.EncodeToString(rcv.PublicKey().Bytes()) != v.Receiver.B64 {
		t.Fatal("receiver public key")
	}
	if shared, _ := rcv.ECDH(app.PublicKey()); hex.EncodeToString(shared) != v.Shared {
		t.Fatal("shared secret")
	}
	token := unhex(t, v.Token)
	if b64.EncodeToString(token) != v.TokenB64 {
		t.Fatal("token base64url")
	}
	for _, k := range []struct {
		priv *ecdh.PrivateKey
		peer []byte
	}{{rcv, app.PublicKey().Bytes()}, {app, rcv.PublicKey().Bytes()}} {
		got, err := deriveKeys(k.priv, k.peer, token)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got.up)+hex.EncodeToString(got.down) != v.HKDF.OKM || hex.EncodeToString(got.up) != v.Up || hex.EncodeToString(got.down) != v.Down {
			t.Fatalf("keys %x %x", got.up, got.down)
		}
	}
	// The salt is the token's bytes, not its base64url text.
	if got, _ := deriveKeys(rcv, app.PublicKey().Bytes(), []byte(v.TokenB64)); hex.EncodeToString(got.up) == v.Up {
		t.Fatal("the base64url text derived the same keys")
	}
}

// The pairing URL, byte for byte, and read back by the old phone's side.
func TestVectorPairing(t *testing.T) {
	v := loadVectors(t)
	p := Pairing{Host: v.Pairing.Host, Port: v.Pairing.Port, SSID: v.Pairing.SSID, PSK: v.Pairing.PSK,
		Token: unhex(t, v.Token), Key: unhex(t, v.Receiver.Pub), Name: v.Pairing.Name}
	if got := p.URL(); got != v.PairingURL {
		t.Fatalf("URL\n got %s\nwant %s", got, v.PairingURL)
	}
	back, err := oldphone.ParsePairing(v.PairingURL)
	if err != nil {
		t.Fatal(err)
	}
	if back.Host != p.Host || back.Port != p.Port || back.SSID != p.SSID || back.PSK != p.PSK || back.Name != p.Name ||
		!bytes.Equal(back.Token, p.Token) || !bytes.Equal(back.Key, p.Key) {
		t.Fatalf("parsed %+v", back)
	}
	p.SSID, p.PSK = "", ""
	if back, err := oldphone.ParsePairing(p.URL()); err != nil || back.SSID != "" || back.PSK != "" || back.Wifi != "" {
		t.Fatalf("without a hotspot: %+v %v", back, err)
	}
	if strings.Contains(v.PairingURL, "wifi=") {
		t.Fatal("the vectors' pairing names a Wi-Fi network")
	}
}

// wifiPairingURL is the vectors' pairing on the Wi-Fi path, byte for byte:
// the app's tests (switch/test/pairing.test.ts) hold the same text.
const wifiPairingURL = "kario-switch://pair?v=1&host=192.168.1.23&port=47800&wifi=%E5%AE%B6%E9%87%8C+Wi-Fi%2B5G" +
	"&token=AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8&key=3p7bfXt9wbTTW2HC7OQ1Nz-DQ8hbeGdNrfx-FG-IK08&name=%E6%88%91%E7%9A%84+Kario"

// The Wi-Fi path: the network's name where the hotspot's would be, without
// a password; never beside a hotspot, and left out when an SSID cannot be
// text in the code.
func TestPairingWifi(t *testing.T) {
	v := loadVectors(t)
	home := "\u5bb6\u91cc Wi-Fi+5G"
	p := Pairing{Host: "192.168.1.23", Port: 47800, Wifi: home, Token: unhex(t, v.Token), Key: unhex(t, v.Receiver.Pub), Name: v.Pairing.Name}
	if got := p.URL(); got != wifiPairingURL {
		t.Fatalf("URL\n got %s\nwant %s", got, wifiPairingURL)
	}
	back, err := oldphone.ParsePairing(wifiPairingURL)
	if err != nil || back.Wifi != home || back.SSID != "" || back.PSK != "" || back.Host != p.Host || back.Name != p.Name {
		t.Fatalf("parsed %+v %v", back, err)
	}
	p.SSID, p.PSK = v.Pairing.SSID, v.Pairing.PSK
	if u := p.URL(); strings.Contains(u, "wifi=") || !strings.Contains(u, "&ssid=Kario-3F2A&psk=") {
		t.Fatalf("beside a hotspot: %s", u)
	}
	p.SSID, p.PSK = "", ""
	for _, bad := range []string{"\xff\xfe", strings.Repeat("a", 33)} {
		p.Wifi = bad
		if u := p.URL(); strings.Contains(u, "wifi=") || !strings.Contains(u, "&port=47800&token=") {
			t.Fatalf("%q: %s", bad, u)
		}
	}
	p.Wifi = strings.Repeat("\u5bb6", 10) + "ab"
	if back, err := oldphone.ParsePairing(p.URL()); err != nil || back.Wifi != p.Wifi {
		t.Fatalf("32 bytes: %+v %v", back, err)
	}
	if _, err := oldphone.ParsePairing(v.PairingURL + "&wifi=Home"); err == nil {
		t.Fatal("the old phone took a Wi-Fi network beside a hotspot")
	}
	if _, err := oldphone.ParsePairing(strings.Replace(wifiPairingURL, "wifi=", "wifi="+strings.Repeat("x", 33), 1)); err == nil {
		t.Fatal("the old phone took a name over 32 bytes")
	}
}

// Every envelope seals to the vector's bytes and opens to its plaintext.
func TestVectorEnvelopes(t *testing.T) {
	v := loadVectors(t)
	for _, e := range v.Envelopes {
		aead := newAEAD(v.key(t, e.Key))
		if e.AAD != e.Method+" "+e.Target {
			t.Fatalf("%s: aad %q", e.Name, e.AAD)
		}
		plain := unhex(t, e.Plain)
		env := seal(aead, e.Direction, e.Counter, plain, e.AAD)
		if hex.EncodeToString(env) != e.Envelope || hex.EncodeToString(env[:nonceSize]) != e.Nonce {
			t.Errorf("%s: sealed\n %x\nwant %s", e.Name, env, e.Envelope)
		}
		n, got, err := open(aead, e.Direction, unhex(t, e.Envelope), e.AAD)
		if err != nil || n != e.Counter || !bytes.Equal(got, plain) {
			t.Errorf("%s: opened %d %q %v", e.Name, n, got, err)
		}
		if _, _, err := open(aead, 3-e.Direction, unhex(t, e.Envelope), e.AAD); err == nil {
			t.Errorf("%s: opened as the other direction", e.Name)
		}
	}
	for _, e := range v.Invalid {
		if _, _, err := open(newAEAD(v.key(t, e.Key)), dirUp, unhex(t, e.Envelope), e.AAD); err == nil {
			t.Errorf("%s: opened", e.Name)
		}
	}
}

// vectorSink stores whatever comes.
type vectorSink struct{ vcards, files int }

func all(n int) []Result { return make([]Result, n) }

func (s *vectorSink) Contacts(v []string) ([]Result, error) {
	s.vcards += len(v)
	return all(len(v)), nil
}
func (s *vectorSink) CallLog(v []CallEntry) ([]Result, error) {
	return all(len(v)), nil
}
func (s *vectorSink) SMS(v []Message) ([]Result, error)     { return all(len(v)), nil }
func (s *vectorSink) Calendar(v []string) ([]Result, error) { return all(len(v)), nil }
func (s *vectorSink) File(File) error                       { s.files++; return nil }

// seededRand gives the receiver the vector's private key and token.
func seededRand(t testing.TB, v vectors) *bytes.Reader {
	return bytes.NewReader(append(append(unhex(t, v.Receiver.Priv), unhex(t, v.Token)...), make([]byte, 8)...))
}

// The vectors' session played against the receiver: every reply it seals
// is the vector's, byte for byte (the random source gives the vector's
// session id).
func TestVectorSession(t *testing.T) {
	v := loadVectors(t)
	r := NewReceiver(t.Logf)
	sink := &vectorSink{}
	s, err := r.Open(Config{Name: v.Pairing.Name, Host: v.Pairing.Host, Port: v.Pairing.Port, SSID: v.Pairing.SSID, PSK: v.Pairing.PSK,
		Accept: Kinds, Sink: sink, Dir: t.TempDir(), MaxTotal: 1 << 20, Rand: seededRand(t, v)})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Pairing().URL(); got != v.PairingURL {
		t.Fatalf("pairing %s", got)
	}
	byName := map[string]envelope{}
	for _, e := range v.Envelopes {
		byName[e.Name] = e
	}
	send := func(name string) *httptest.ResponseRecorder {
		e := byName[name]
		req := httptest.NewRequest(e.Method, e.Target, bytes.NewReader(unhex(t, e.Envelope)))
		for k, h := range e.Headers {
			req.Header.Set(k, h)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	down := newAEAD(unhex(t, v.Down))
	w := send("hello")
	n, plain, err := open(down, dirDown, w.Body.Bytes(), "POST /v1/hello")
	var hello helloReply
	if w.Code != http.StatusOK || err != nil || n != 0 || json.Unmarshal(plain, &hello) != nil || hello.MaxBody != MaxBody || hello.Chunk != Chunk || len(hello.Accept) != 6 {
		t.Fatalf("hello: %d %d %s %v", w.Code, n, plain, err)
	}
	if want := byName["hello reply"]; w.Code != want.Status || hex.EncodeToString(w.Body.Bytes()) != want.Envelope {
		t.Fatalf("hello: %s, want %s", plain, unhex(t, want.Plain))
	}
	for _, step := range []struct{ send, want string }{
		{"items batch", "items reply"},
		{"file offer", "file offer reply"},
		{"file chunk", "file chunk reply"},
		{"items batch", "replay error reply"},
		{"done", "done reply"},
	} {
		w := send(step.send)
		want := byName[step.want]
		if w.Code != want.Status || hex.EncodeToString(w.Body.Bytes()) != want.Envelope {
			_, got, _ := open(down, dirDown, w.Body.Bytes(), want.AAD)
			t.Fatalf("%s: %d %s, want %s", step.send, w.Code, got, step.want)
		}
	}
	if sink.vcards != 1 || sink.files != 1 {
		t.Fatalf("stored %+v", sink)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("done did not end the session")
	}
	// The token is void: hello again does not open.
	w = send("hello")
	if w.Code != v.Unauthorized.Status || w.Header().Get("Content-Type") != v.Unauthorized.ContentType || w.Body.String() != v.Unauthorized.Body {
		t.Fatalf("after done: %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}
