package pushd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.PushRegister, svc.PushUnregister, svc.PushRadioActive, svc.PushPending, svc.PushAck, svc.PushState}

// FuzzRPC is what the Shell, or what took its place, writes on pushd.sock,
// pushd connected to a gateway that hands out tokens and takes the rest.
func FuzzRPC(f *testing.F) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device/token" {
			var a struct{ App string }
			_ = json.NewDecoder(r.Body).Decode(&a)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t-" + a.App})
		}
	}))
	defer gw.Close()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	pick := func(method string, params any) string { return rpcfuzz.Pick(rpcMethods, method, params) }
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.PushRegister, map[string]string{"app": "com.example.chat"}),
		rpcfuzz.Line(2, svc.PushRegister, map[string]string{"app": "com.example.chat"}),
		rpcfuzz.Line(3, svc.PushPending, nil),
		rpcfuzz.Line(4, svc.PushAck, map[string]string{"id": "m1"}),
		rpcfuzz.Line(5, svc.PushState, nil))
	rpcfuzz.Seed(f,
		pick(svc.PushRadioActive, nil),
		pick(svc.PushRadioActive, nil),
		pick(svc.PushUnregister, map[string]string{"app": "com.example.chat"}),
		pick(svc.PushRegister, map[string]string{}),
		pick(svc.PushAck, map[string]string{"id": "m2"}))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"register","params":{"app":["x"]}}`,
		`{"id":2,"method":"ack","params":"m1"}`,
		`{"id":3,"method":"unregister","params":{"app":"../x"}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		d := &daemon{gwFlag: gw.URL, stateDir: t.TempDir(), tokens: map[string]string{}, http: client, stream: client,
			hb: push.NewHeartbeat(), gw: gw.URL, connected: true,
			pending: []push.Message{{ID: "m1", App: "com.example.chat"}, {ID: "m2", App: "com.example.mail"}}}
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: d.handle}, data)
	})
}
