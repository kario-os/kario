package smsd

import (
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.SMSSend, svc.SMSList, svc.SMSThreads, svc.SMSRead, svc.SMSDelete, svc.SMSBlock, svc.SMSBlocked,
	svc.SMSAlerts, svc.SMSUnnotified, svc.SMSNotified, svc.SMSState, svc.SMSImport, svc.SMSDeleteImported, svc.SMSFlashSave,
	svc.SMSDownload, svc.SMSPart, svc.SMSPartMedia, svc.SMSRegion, svc.SMSKnown, svc.SMSBlockList, svc.SMSBlockSet, svc.SMSOnly,
	svc.SMSMMSSim, svc.SMSDraft, svc.SMSScheduleSim}

// FuzzRPC is what the Shell, or what took its place, writes on smsd.sock:
// requests of every method with any params, on a store of their own, no
// telephonyd behind it.
func FuzzRPC(f *testing.F) {
	m := rpcMethods
	pick := func(method string, params any) string { return rpcfuzz.Pick(m, method, params) }
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.SMSSend, svc.SMSSendReq{To: "10010", Text: "balance?"}),
		rpcfuzz.Line(2, svc.SMSList, ui.SMSListArgs{Peer: "10010"}),
		rpcfuzz.Line(3, svc.SMSThreads, ui.SMSListArgs{}),
		rpcfuzz.Line(4, svc.SMSRead, ui.SMSListArgs{Peer: "10010"}),
		rpcfuzz.Line(5, svc.SMSDelete, ui.SMSDeleteArgs{Peer: "10010"}),
		rpcfuzz.Line(6, svc.SMSState, nil))
	rpcfuzz.Seed(f,
		pick(svc.SMSImport, svc.SMSImportArgs{Messages: []ui.SMS{
			{Peer: "+86 138 0000 0001", Dir: "in", Text: "from the old phone", At: 1700000000, Read: true},
			{Peer: "+86 138 0000 0001", Dir: "out", Text: "ok", At: 1700000060},
			{Peer: "10086", Dir: "in", Kind: ui.SMSKindMMS, Subject: "group", Members: []string{"10086", "10010"}, At: 1700000100}}}),
		pick(svc.SMSThreads, ui.SMSListArgs{Folder: ui.SMSFolderBlocked}),
		pick(svc.SMSList, ui.SMSListArgs{Query: "old"}),
		pick(svc.SMSDeleteImported, ui.SMSDeleteArgs{IDs: []string{"s1", "s2", ""}}))
	rpcfuzz.Seed(f,
		pick(svc.SMSBlockSet, ui.BlockSetArgs{Number: "+86 400 *", On: true}),
		pick(svc.SMSBlockSet, ui.BlockSetArgs{Word: "loan", On: true}),
		pick(svc.SMSBlock, ui.SMSBlockArgs{Number: "10690000", On: true}),
		pick(svc.SMSBlocked, nil),
		pick(svc.SMSBlockList, nil),
		pick(svc.SMSKnown, map[string][]string{"numbers": {"+86 138 0000 0001", "10086"}}),
		pick(svc.SMSOnly, map[string]any{"on": true, "numbers": []string{"110", "+86 138 0000 0001"}}),
		pick(svc.SMSRegion, map[string]string{"region": "CN"}),
		pick(svc.SMSOnly, map[string]any{"on": false}))
	rpcfuzz.Seed(f,
		pick(svc.SMSDraft, ui.SMSDraftArgs{Peer: "+86 138 0000 0001", Text: "see you at"}),
		pick(svc.SMSSend, svc.SMSSendReq{To: "+86 138 0000 0001", Text: "later", At: 4102444800, Zone: "Asia/Shanghai"}),
		pick(svc.SMSSend, svc.SMSSendReq{To: "10010, 10086", Text: "to both", Subject: "hi",
			Parts: []svc.SMSPartIn{{Type: "image/png", Name: "a.png", Data: []byte("\x89PNG\r\n\x1a\n")}}}),
		pick(svc.SMSSend, svc.SMSSendReq{ID: "s1"}),
		pick(svc.SMSPart, svc.SMSPartArgs{ID: "w1", Index: 0}),
		pick(svc.SMSPartMedia, svc.SMSPartArgs{ID: "w1", Index: 0, Media: "media:p7"}),
		pick(svc.SMSFlashSave, ui.SMSIDArgs{ID: "f1"}),
		pick(svc.SMSDownload, ui.SMSIDArgs{ID: "s1"}),
		pick(svc.SMSUnnotified, nil),
		pick(svc.SMSNotified, map[string][]string{"ids": {"s1", "f1"}}),
		pick(svc.SMSAlerts, nil),
		pick(svc.SMSMMSSim, map[string]string{"mmsc": "http://127.0.0.1:1/mms"}),
		pick(svc.SMSScheduleSim, nil))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"send","params":{"to":7,"text":["x"]}}`,
		`{"id":2,"method":"list","params":[1,2]}`,
		`{"id":"3","method":"threads"}`,
		`{"id":4,"method":"import","params":{"messages":[{"peer":"","dir":"sideways","at":-1}]}}`,
		`{"id":5,"method":"part","params":{"id":"../../x","index":-1}}`,
		`{"id":6,"method":"subscribe","fds":3}`,
		`{"id":7,"method":"send","params":{"to":"1","text":"x","at":-9223372036854775808,"zone":"../../etc/passwd"}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := openStore(filepath.Join(t.TempDir(), "smsd"))
		if err != nil {
			t.Fatal(err)
		}
		s := newService(st, func(svc.SMSEvent) {})
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: s.handle, Close: func() { _ = st.close() }}, data)
	})
}
