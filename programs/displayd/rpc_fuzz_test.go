package displayd

import (
	"testing"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.DisplayState, svc.DisplaySetting, svc.DisplayHint, svc.DisplayCap, svc.DisplayPanel, svc.DisplaySimThermal}

// FuzzRPC is what the Shell, or what took its place, writes on
// displayd.sock: settings, hints of any kind and length, caps, the
// renderer's reports, on the simulated panel and thermal zones.
func FuzzRPC(f *testing.F) {
	pick := func(method string, params any) string { return rpcfuzz.Pick(rpcMethods, method, params) }
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.DisplayState, nil),
		rpcfuzz.Line(2, svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction, MS: 1500}),
		rpcfuzz.Line(3, svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceApp + "com.example.game", Kind: svc.DisplayContent, FPS: 120}),
		rpcfuzz.Line(4, svc.DisplayPanel, svc.DisplayPanelArgs{Applied: 120}),
		rpcfuzz.Line(5, svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceApp + "*", Kind: svc.DisplayClear}))
	rpcfuzz.Seed(f,
		pick(svc.DisplaySetting, map[string]string{"setting": ui.RefreshHigh}),
		pick(svc.DisplayCap, svc.DisplayCapArgs{Source: svc.DisplayCapBattery, Hz: 60}),
		pick(svc.DisplaySimThermal, map[string]bool{"hot": true}),
		pick(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceRenderer, Kind: svc.DisplayAnimating, MS: 300}),
		pick(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceVideo + "play1", Kind: svc.DisplayContent, FPS: 24}),
		pick(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceShell, Kind: svc.DisplayEnd}),
		pick(svc.DisplayPanel, svc.DisplayPanelArgs{KMS: true, Err: "EINVAL"}),
		pick(svc.DisplaySimThermal, map[string]bool{"hot": false}),
		pick(svc.DisplayCap, svc.DisplayCapArgs{Source: svc.DisplayCapBattery}),
		pick(svc.DisplaySetting, map[string]string{"setting": ui.RefreshAuto}))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"hint","params":{"source":"input","kind":"interaction","ms":9223372036854775807}}`,
		`{"id":2,"method":"hint","params":{"source":"app:x","kind":"content","fps":-2147483648}}`,
		`{"id":3,"method":"cap","params":{"source":"thermal","hz":1}}`,
		`{"id":4,"method":"panel","params":{"applied":-1}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		d := newDaemon(newSimPanel([]int{60, 120}), newThermal(t.TempDir(), "", 45), false)
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: d.handle}, data)
	})
}
