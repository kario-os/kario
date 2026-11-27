package mediad

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.MediaCallStart, svc.MediaCallControl, svc.MediaCallEnd, svc.MediaCameraOpen,
	svc.MediaCameraCapture, svc.MediaCameraClose, svc.MediaInfo, svc.MediaScanOpen, svc.MediaScanWait,
	svc.MediaScanClose, svc.MediaSimCode, svc.MediaPlayerProbe, svc.MediaPlayerOpen, svc.MediaPlayerControl,
	svc.MediaPlayerClose}

// FuzzRPC is what the Shell writes on mediad.sock: calls, the camera and
// its photos, the scanner and the code held up to it, on the synthetic
// camera. player.* comes without the video the Shell passes along.
func FuzzRPC(f *testing.F) {
	pick := func(method string, params any) string { return rpcfuzz.Pick(rpcMethods, method, params) }
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.MediaCallStart, map[string]any{"app": "com.phone.dialer", "peer": "+8613800138000", "video": true}),
		rpcfuzz.Line(2, svc.MediaCallControl, map[string]any{"call_id": "call1", "mic": false}),
		rpcfuzz.Line(3, svc.MediaCallControl, map[string]any{"call_id": "call1", "cam": false}),
		rpcfuzz.Line(4, svc.MediaInfo, nil),
		rpcfuzz.Line(5, svc.MediaCallEnd, map[string]string{"call_id": "call1"}))
	rpcfuzz.Seed(f,
		pick(svc.MediaCameraOpen, map[string]string{"app": "com.phone.camera"}),
		pick(svc.MediaCameraCapture, map[string]string{"cam_id": "cam1"}),
		pick(svc.MediaCameraOpen, map[string]string{"app": "com.phone.camera"}),
		pick(svc.MediaCameraClose, map[string]string{"cam_id": "cam2"}))
	rpcfuzz.Seed(f,
		pick(svc.MediaSimCode, map[string]string{"text": "https://example.com/pay?id=1"}),
		pick(svc.MediaScanOpen, map[string]string{"app": "com.phone.wallet"}),
		pick(svc.MediaScanWait, map[string]string{"scan_id": "scan1"}),
		pick(svc.MediaScanOpen, map[string]string{"app": "com.phone.wallet"}),
		pick(svc.MediaScanClose, map[string]string{"scan_id": "scan2"}),
		pick(svc.MediaSimCode, map[string]string{"text": ""}))
	rpcfuzz.Seed(f,
		pick(svc.MediaPlayerProbe, map[string]any{"app": "com.phone.gallery"}),
		pick(svc.MediaPlayerOpen, map[string]any{"app": "com.phone.gallery", "title": "clip", "max_w": 320, "max_h": 240}),
		pick(svc.MediaPlayerControl, map[string]any{"player": "play1", "op": ui.OpSeek, "at": 1.5}),
		pick(svc.MediaPlayerClose, map[string]any{"player": "play1"}))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"call.control","params":{"call_id":"call1","mic":null,"cam":"yes"}}`,
		`{"id":2,"method":"sim.code","params":{"text":"\u0000\uffff"}}`,
		`{"id":3,"method":"scan.wait","params":{"scan_id":""}}`)
	card, err := media.NewCard(demoMerchantCode)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s := &server{started: time.Now(), streams: map[string]media.Source{}, calls: map[string]*call{}, scans: map[string]*scan{}, players: map[string]*player{}, card: card}
		closeScans := func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for id := range s.scans {
				s.closeScan(id)
			}
		}
		h := func(method string, params json.RawMessage) (any, error) { return s.serve(method, params, nil) }
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: h, Cancel: closeScans, Close: closeScans}, data)
	})
}
