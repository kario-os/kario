package imed

import (
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.IMEOpen, svc.IMEKey, svc.IMETick, svc.IMECancel, svc.IMEState, svc.IMEStats}

// FuzzRPC is what the Shell, or what took its place, writes on imed.sock:
// sessions opened on any keypad and mode, and keys of any kind.
func FuzzRPC(f *testing.F) {
	// The dictionary, built once per process (not counted against an input).
	ime.New(ime.QWERTY).Handle(ime.Key{Name: "rune", Rune: 'n'})
	pick := func(method string, params any) string { return rpcfuzz.Pick(rpcMethods, method, params) }
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.IMEOpen, map[string]any{"keypad": ime.QWERTY, "mode": ime.Zh, "fuzzy": true}),
		rpcfuzz.Line(2, svc.IMEKey, ime.Key{Name: "rune", Rune: 'n'}),
		rpcfuzz.Line(3, svc.IMEKey, ime.Key{Name: "rune", Rune: 'i'}),
		rpcfuzz.Line(4, svc.IMEKey, ime.Key{Name: "enter"}),
		rpcfuzz.Line(5, svc.IMEState, nil),
		rpcfuzz.Line(6, svc.IMEStats, nil))
	rpcfuzz.Seed(f,
		pick(svc.IMEOpen, map[string]any{"keypad": ime.T9Pad, "mode": ime.Stroke}),
		pick(svc.IMEKey, ime.Key{Name: "rune", Rune: '1'}),
		pick(svc.IMEKey, ime.Key{Name: "rune", Rune: '5', Long: true}),
		pick(svc.IMETick, nil),
		pick(svc.IMEOpen, map[string]any{"mode": ime.En}),
		pick(svc.IMEKey, ime.Key{Name: "rune", Rune: '4', Before: "6."}),
		pick(svc.IMEKey, ime.Key{Name: "sym"}),
		pick(svc.IMEKey, ime.Key{Name: "backspace"}),
		pick(svc.IMEOpen, map[string]any{"mode": ime.Abc}),
		pick(svc.IMEKey, ime.Key{Name: "rune", Rune: '*', Long: true}),
		pick(svc.IMEKey, ime.Key{Name: "ime"}),
		pick(svc.IMECancel, nil))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"open","params":{"keypad":"dvorak","mode":"klingon"}}`,
		`{"id":2,"method":"key","params":{"name":"rune","rune":-1,"before":"\udfff"}}`,
		`{"id":3,"method":"key","params":{"name":"rune","rune":1114112}}`,
		`{"id":4,"method":"key","params":"enter"}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		e := ime.New(ime.QWERTY)
		e.SetUserDict(filepath.Join(t.TempDir(), "user.json"))
		s := &server{e: e}
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: s.handle}, data)
	})
}
