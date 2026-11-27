package switchrecv

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kario-os/kario/switchrecv/oldphone"
)

// fuzzTargets are the requests a fuzzed plaintext goes to.
var fuzzTargets = []struct{ method, target string }{
	{"POST", "/v1/hello"},
	{"POST", "/v1/items/contacts"},
	{"POST", "/v1/items/call_log"},
	{"POST", "/v1/items/sms"},
	{"POST", "/v1/items/calendar"},
	{"POST", "/v1/files"},
	{"PUT", "/v1/files/f1?offset=0"},
	{"PUT", "/v1/files/f1?offset=4"},
	{"POST", "/v1/done"},
	{"GET", "/"},
}

// FuzzEnvelope is what reaches the receiver from the network: a body that
// is not an envelope under the session's keys gets the plain 401; a
// plaintext the paired app seals, whatever its JSON, gets a sealed answer
// at the next downstream counter, a JSON object with a code unless it is
// 200; and a sealed plaintext opens to itself.
func FuzzEnvelope(f *testing.F) {
	v := loadVectors(f)
	for _, e := range v.Envelopes {
		f.Add(uint8(0), unhex(f, e.Envelope), unhex(f, e.Plain))
	}
	f.Add(uint8(1), []byte("x"), []byte(`{"items":[{"vcard":"BEGIN:VCARD\r\nFN:a\r\nEND:VCARD"}],"last":true}`))
	f.Add(uint8(2), []byte{}, []byte(`{"items":[{"number":"1","type":"missed","at":5,"duration":0,"id":"a"}]}`))
	f.Add(uint8(3), []byte{}, []byte(`{"items":[{"address":"1","body":"x","type":"in","at":1,"read":true}]}`))
	f.Add(uint8(4), []byte{}, []byte(`{"items":[{"ics":"BEGIN:VEVENT\nBEGIN:VALARM\nEND:VALARM\nEND:VEVENT"}]}`))
	f.Add(uint8(5), []byte{}, []byte(`{"id":"f1","kind":"photo","name":"a.jpg","size":8,"mime":"image/jpeg","sha256":"9f64a747e1b97f131fabb6b447296c9b6f0201e79fb3c5356e6c77e89b6a806a"}`))
	f.Add(uint8(6), []byte{}, []byte("1234"))
	f.Add(uint8(8), []byte{}, []byte("{}"))
	helloVector := unhex(f, v.Envelopes[0].Envelope)
	f.Fuzz(func(t *testing.T, op uint8, raw, plain []byte) {
		r := NewReceiver(nil)
		s, err := r.Open(Config{Host: "192.168.43.1", Port: Port, Accept: Kinds, Sink: &vectorSink{}, Dir: t.TempDir(), MaxTotal: 1 << 20, Rand: seededRand(t, v)})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		p := s.Pairing()
		app, err := oldphone.New(oldphone.Pairing{Host: p.Host, Port: p.Port, Token: p.Token, Key: p.Key}, unhex(t, v.App.Priv))
		if err != nil {
			t.Fatal(err)
		}
		tg := fuzzTargets[int(op)%len(fuzzTargets)]
		do := func(method, target string, body []byte, hello bool) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, target, bytes.NewReader(body))
			if hello {
				req.Header.Set("X-Kario-Key", app.PublicKey())
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			return w
		}
		// Raw bytes, as anyone on the network sends them: only the
		// vectors' hello opens (the fuzzer cannot seal), on its own path.
		w := do(tg.method, tg.target, raw, true)
		if w.Code != http.StatusUnauthorized || w.Body.String() != `{"error":"the envelope does not open","code":"bad_key"}` {
			if !bytes.Equal(raw, helloVector) || tg.target != "/v1/hello" {
				t.Fatalf("raw %q to %s: %d %q", raw, tg.target, w.Code, w.Body)
			}
		}
		// The paired app: hello, then the fuzzed plaintext.
		app.SetCounter(100)
		w = do("POST", "/v1/hello", app.Seal("POST", "/v1/hello", []byte(`{"app":"kario-switch","version":"1.0.0","counts":{"contacts":1}}`)), true)
		if w.Code != http.StatusOK || w.Body.Len() < 28 {
			t.Fatalf("hello: %d", w.Code)
		}
		helloCounter := binary.BigEndian.Uint64(w.Body.Bytes()[4:12])
		pub, _ := b64.DecodeString(app.PublicKey())
		ks, err := deriveKeys(s.priv, pub, s.token)
		if err != nil {
			t.Fatal(err)
		}
		w = do(tg.method, tg.target, app.Seal(tg.method, tg.target, plain), tg.target == "/v1/hello")
		if tg.target == "/" {
			if w.Code != http.StatusNotFound {
				t.Fatalf("GET /: %d", w.Code)
			}
			return
		}
		n, got, err := open(newAEAD(ks.down), dirDown, w.Body.Bytes(), tg.method+" "+tg.target)
		if err != nil || n != helloCounter+1 {
			t.Fatalf("%s %q: %d, the reply does not open at %d: %d %v", tg.target, plain, w.Code, helloCounter+1, n, err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(got, &obj); err != nil {
			t.Fatalf("%s %q: the reply %q is not a JSON object", tg.target, plain, got)
		}
		if _, ok := obj["code"]; w.Code != http.StatusOK && !ok {
			t.Fatalf("%s %q: %d without a code: %s", tg.target, plain, w.Code, got)
		}
		switch w.Code {
		case 200, 400, 403, 404, 409, 422, 500, 503, 507:
		default:
			t.Fatalf("%s %q: status %d", tg.target, plain, w.Code)
		}
		aead := newAEAD(ks.up)
		if c, back, err := open(aead, dirUp, seal(aead, dirUp, uint64(len(raw)), plain, string(raw)), string(raw)); err != nil || c != uint64(len(raw)) || !bytes.Equal(back, plain) {
			t.Fatalf("round trip %q: %q %v", plain, back, err)
		}
	})
}
