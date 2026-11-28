package audiod

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
	"github.com/kario-os/kario/audio/tts"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
	"github.com/kario-os/kario/wire/rpcfuzz"
)

var rpcMethods = []string{svc.AudioPlay, svc.AudioOpen, svc.AudioControl, svc.AudioState, svc.AudioVolume,
	svc.AudioRoute, svc.AudioChannels, svc.SpeechSay, svc.SpeechStop, svc.SpeechState}

// FuzzRPC is what the Shell writes on audiod.sock: tones, demo tracks,
// files and music tracks, the apps' own rings, their control, volumes,
// routes and speech, on the null sink without its clock. A file outside
// the fuzz directory is refused before audiod sees it: the Shell names
// only the apps' and the user's files.
func FuzzRPC(f *testing.F) {
	pick := func(method string, params any) string { return rpcfuzz.Pick(rpcMethods, method, params) }
	dir := filepath.Dir(wav(f, 44100, 4410))
	song := filepath.Join(dir, "a.wav")
	rpcfuzz.Seed(f,
		rpcfuzz.Line(1, svc.AudioPlay, playArgs{App: "system", Name: "ring", Category: audio.Ring, Source: "tone:ringtone"}),
		rpcfuzz.Line(2, svc.AudioPlay, playArgs{App: "com.phone.music", Name: "dawn", Category: audio.Media, Source: "demo:dawn"}),
		rpcfuzz.Line(3, svc.AudioPlay, playArgs{App: "system", Name: "notify", Category: audio.Notify, Source: "tone:notify"}),
		rpcfuzz.Line(4, svc.AudioState, nil),
		rpcfuzz.Line(5, svc.AudioControl, controlArgs{ID: "s1", Op: ui.OpPause}),
		rpcfuzz.Line(6, svc.AudioControl, controlArgs{ID: "s1", Op: ui.OpResume}),
		rpcfuzz.Line(7, svc.AudioControl, controlArgs{ID: "s2", Op: ui.OpStop}))
	rpcfuzz.Seed(f,
		pick(svc.AudioPlay, playArgs{App: "com.phone.music", Name: "a", Category: audio.Media,
			Track: &hifi.Spec{Path: song, Media: "music:a.wav", Label: "WAV 16/44.1", GainDB: -3, Peak: 0.9},
			Next:  &hifi.Spec{Path: song, Media: "music:b.wav"}, StartMS: 20, DSD: hifi.DSDDoP}),
		pick(svc.AudioControl, controlArgs{ID: "s1", Op: ui.OpSeek, AtMS: 50}),
		pick(svc.AudioControl, controlArgs{ID: "s1", Op: ui.OpQueue, Next: &hifi.Spec{Path: song}}),
		pick(svc.AudioControl, controlArgs{ID: "s1", Op: ui.OpOptions, DSD: hifi.DSDPCM}),
		pick(svc.AudioPlay, playArgs{App: "com.example.app", Name: "clip", Category: audio.Media, Source: "file:" + song}),
		pick(svc.AudioState, nil))
	rpcfuzz.Seed(f,
		pick(svc.AudioOpen, map[string]string{"app": "com.example.game", "name": "fx", "category": audio.Media, "dir": svc.AudioDirPlay}),
		pick(svc.AudioOpen, map[string]string{"app": "com.example.recorder", "name": "memo", "dir": svc.AudioDirRecord}),
		pick(svc.AudioControl, controlArgs{ID: "r1", Op: ui.OpStop}),
		pick(svc.AudioVolume, map[string]any{"category": audio.Media, "level": 0.5}),
		pick(svc.AudioRoute, map[string]string{"route": ui.RouteSpeaker}),
		pick(svc.AudioChannels, map[string]any{"mono": true, "balance": -0.25}))
	rpcfuzz.Seed(f,
		pick(svc.SpeechSay, map[string]any{"text": "Battery low", "lang": "en"}),
		pick(svc.SpeechSay, map[string]any{"text": "Next", "lang": "en"}),
		pick(svc.SpeechState, nil),
		pick(svc.SpeechSay, map[string]any{"text": "Call", "lang": "en", "interrupt": true}),
		pick(svc.SpeechStop, nil))
	rpcfuzz.Seed(f,
		`{"id":1,"method":"volume","params":{"category":"media","level":1e308}}`,
		`{"id":2,"method":"channels","params":{"mono":false,"balance":-1e308}}`,
		`{"id":3,"method":"play","params":{"source":"tone:"}}`,
		`{"id":4,"method":"control","params":{"id":"s1","op":"seek","at_ms":-9223372036854775808}}`,
		`{"id":5,"method":"open","params":{"dir":"sideways"}}`)
	f.Fuzz(func(t *testing.T, data []byte) {
		m := audio.NewMixer()
		out := &nullSink{}
		rec := &recordings{}
		play, outs := newPlayer(m, out, nil, rec), newOutputs(m, out, nil)
		play.outs = outs
		d := &daemon{m: m, out: out, rec: rec, play: play, speech: tts.New(tts.Simulated{}), hub: &wire.Hub{}, outs: outs}
		h := func(method string, params json.RawMessage) (any, error) {
			var a struct {
				Source      string
				Track, Next *hifi.Spec
			}
			if (method == svc.AudioPlay || method == svc.AudioControl) && json.Unmarshal(params, &a) == nil {
				for _, p := range []string{strings.TrimPrefix(a.Source, "file:"), path(a.Track), path(a.Next)} {
					if p != "" && p != a.Source && !strings.HasPrefix(filepath.Clean(p), dir+string(filepath.Separator)) {
						return nil, errors.New("not in the fuzz directory")
					}
				}
			}
			return d.handle(method, params)
		}
		closeAll := func() {
			for _, s := range m.State() {
				_, _ = m.Control(s.ID, ui.OpStop)
			}
			for _, s := range rec.streams() {
				rec.stop(s.ID)
			}
			d.speech.Stop()
		}
		rpcfuzz.Run(t, rpcfuzz.Service{Methods: rpcMethods, Handler: h, Close: closeAll}, data)
	})
}

func path(s *hifi.Spec) string {
	if s == nil {
		return ""
	}
	return s.Path
}
