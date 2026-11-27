package nfcd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.NFCState, svc.NFCPolicy, svc.NFCSet, svc.NFCPresent, svc.NFCReply, svc.NFCReaderOpen,
	svc.NFCReaderWait, svc.NFCReaderExchange, svc.NFCReaderClose, svc.NFCSimCard}

// FuzzRPC is what the Shell, or what took its place, writes on nfcd.sock:
// the policy and the apps' cards, settings, reader sessions and the
// simulated cards, on the simulated controller. sim.tap is answered
// without running: the simulated reader polls in real time (0.6 s).
func FuzzRPC(f *testing.F) {
	pick := func(method string, params any) string { return rpcfuzz.Pick(rpcMethods, method, params) }
	policy := svc.NFCPolicyArgs{ScreenOn: true, Foreground: "com.example.wallet", ZoneOffset: 8 * 3600, Cards: []svc.NFCAppCard{
		{App: "com.example.wallet", AppName: "Wallet", Card: "transit", Name: "Metro", Category: "transit", AIDs: []string{transitAID}, Express: true},
		{App: "com.example.wallet", AppName: "Wallet", Card: "pay", Name: "Pay", Category: "payment", AIDs: []string{payAID}, RequiresUnlock: true}},
		DaysOff: []string{"2026-10-01"}, Workdays: []string{"2026-09-28"}}
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.NFCPolicy, policy),
		rpcfuzz.Line(2, svc.NFCState, nil),
		rpcfuzz.Line(3, svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetPayment, App: "com.example.wallet"}),
		rpcfuzz.Line(4, svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetExpress, Card: "com.example.wallet/transit"}),
		rpcfuzz.Line(5, svc.NFCPresent, map[string]any{"card": "com.example.wallet/pay", "seconds": 30}),
		rpcfuzz.Line(6, svc.NFCReply, map[string]string{"session": "f1", "app": "com.example.wallet", "apdu": "9000"}))
	rpcfuzz.Seed(f,
		pick(svc.NFCSimCard, map[string]any{"uid": "04A1B2C3", "encrypted": false}),
		pick(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetRead, Seconds: 2}),
		pick(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetAddDoor, Name: "Office"}),
		pick(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetRename, Card: "door1", Name: "Home"}),
		pick(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetMove, Card: "door1", By: -1}),
		pick(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetLocked, Card: "door1", On: true}),
		pick(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetDelete, Card: "door1"}),
		pick(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetCancelRead}))
	rpcfuzz.Seed(f,
		pick(svc.NFCSimCard, map[string]any{"uid": "04A1B2C3D4E5F6", "aid": transitAID, "balance": 1500}),
		pick(svc.NFCReaderOpen, svc.NFCReaderArgs{App: "com.example.reader"}),
		pick(svc.NFCReaderWait, svc.NFCReaderArgs{Session: "r1", App: "com.example.reader", Seconds: 5}),
		pick(svc.NFCReaderExchange, svc.NFCReaderArgs{Session: "r1", App: "com.example.reader", APDU: "00A4040006" + transitAID}),
		pick(svc.NFCReaderExchange, svc.NFCReaderArgs{Session: "r1", App: "com.example.reader", APDU: "805C000204"}),
		pick(svc.NFCReaderClose, svc.NFCReaderArgs{Session: "r1", App: "com.example.reader"}),
		pick(svc.NFCSimCard, map[string]any{}))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"reader.wait","params":{"session":"r1","seconds":9223372036854775807}}`,
		`{"id":2,"method":"present","params":{"card":"x","seconds":9223372036854775807}}`,
		`{"id":3,"method":"policy","params":{"clock_skew_ms":-9223372036854775808,"zone_offset":2147483647,"days_off":["2026-13-45"]}}`,
		`{"id":4,"method":"set","params":{"key":"move","card":"","by":-9223372036854775808}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		sim := nfc.NewSim()
		e := newEngine(sim, t.TempDir(), func(svc.NFCEvent) {})
		e.mu.Lock()
		e.idle()
		e.mu.Unlock()
		stop, done := make(chan struct{}), make(chan struct{})
		// The engine's loop (run) without its timer of the time of use.
		go func() {
			defer close(done)
			for {
				select {
				case ev := <-sim.Events():
					e.mu.Lock()
					e.event(ev)
					e.mu.Unlock()
				case <-stop:
					return
				}
			}
		}()
		cancel := func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.reader != nil {
				e.readerEnd(e.reader, "closed")
			}
		}
		h := func(method string, params json.RawMessage) (any, error) {
			if method == svc.NFCSimTap {
				return nil, errors.New("not fuzzed")
			}
			return e.handle(method, params)
		}
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: h, Cancel: cancel, Close: func() { close(stop); <-done }}, data)
	})
}
