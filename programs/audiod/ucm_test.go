package audiod

import (
	"encoding/json"
	"flag"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/audio/ucm"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// The OnePlus 6T's drop-in (phone-audiod-ucm.conf.in with
// oneplus-fajita.conf's values).
var fajitaArgs = []string{"--ucm-card", "hw:0", "--ucm-verb", "HiFi", "--ucm-device", "Speaker",
	"--ucm-call-verb", "Voice Call", "--ucm-earpiece", "Earpiece", "--ucm-earpiece-pcm", "plughw:0,0",
	"--ucm-speaker", "Speaker1", "--ucm-speaker-pcm", "plughw:0,2", "--ucm-mic", "Mic"}

func parseUCM(t *testing.T, args ...string) (*ucm.Router, error) {
	t.Helper()
	old := flag.CommandLine
	defer func() { flag.CommandLine = old }()
	flag.CommandLine = flag.NewFlagSet("phone-audiod", flag.ContinueOnError)
	var uc ucmConfig
	uc.flags()
	if err := flag.CommandLine.Parse(args); err != nil {
		t.Fatal(err)
	}
	return uc.router("/run/phone")
}

func TestUCMFlags(t *testing.T) {
	if r, err := parseUCM(t); r != nil || err != nil {
		t.Fatalf("no flags: %v %v", r, err)
	}
	r, err := parseUCM(t, fajitaArgs...)
	if err != nil {
		t.Fatal(err)
	}
	want := ucm.Router{Tool: ucm.Alsaucm{Card: "hw:0"}, Media: ucm.Setup{Verb: "HiFi", Devices: []string{"Speaker"}},
		Earpiece: ucm.Setup{Verb: "Voice Call", Devices: []string{"Earpiece", "Mic"}, PCM: "plughw:0,0"},
		Speaker:  ucm.Setup{Verb: "Voice Call", Devices: []string{"Speaker1", "Mic"}, PCM: "plughw:0,2"},
		Mic:      "Mic", State: "/run/phone/audiod-ucm.json"}
	if r.Tool != want.Tool || !reflect.DeepEqual(r.Media, want.Media) || !reflect.DeepEqual(r.Earpiece, want.Earpiece) ||
		!reflect.DeepEqual(r.Speaker, want.Speaker) || r.Mic != want.Mic || r.State != want.State {
		t.Fatalf("router %+v", r)
	}
	r, err = parseUCM(t, "--ucm-card", "hw:0", "--ucm-verb", "HiFi", "--ucm-device", "Speaker", "--ucm-call-verb", "")
	if err != nil || r.Earpiece.Verb != "" {
		t.Fatalf("media only: %+v %v", r, err)
	}
	if _, err := parseUCM(t, "--ucm-card", "hw:0"); err == nil {
		t.Fatal("a card without a media verb accepted")
	}
	if _, err := parseUCM(t, "--ucm-card", "hw:0", "--ucm-verb", "HiFi", "--ucm-call-verb", "Voice Call"); err == nil {
		t.Fatal("a call verb without the earpiece accepted")
	}
}

type recTool struct {
	mu   sync.Mutex
	runs []string
}

func (f *recTool) Run(cmds []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, strings.Join(cmds, "|"))
	return nil
}

func (f *recTool) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.runs
	f.runs = nil
	return r
}

// audiod restarted mid-call follows telephonyd: its list of calls (the
// call goes on: the card is left as it is), then the call's end (media
// again).
func TestFollowCalls(t *testing.T) {
	dir := t.TempDir()
	r, err := parseUCM(t, fajitaArgs...)
	if err != nil {
		t.Fatal(err)
	}
	tool := &recTool{}
	r.Tool, r.State = tool, filepath.Join(dir, "audiod-ucm.json")
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	if err := r.SetCalls([]ui.TelCall{{ID: "c1", State: "active"}}); err != nil {
		t.Fatal(err)
	}
	tool.take()
	r, _ = parseUCM(t, fajitaArgs...)
	r.Tool, r.State = tool, filepath.Join(dir, "audiod-ucm.json")
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}

	listed := make(chan struct{}, 1)
	hub, err := wire.ServeHub(filepath.Join(dir, svc.TelephonydSock), func(method string, params json.RawMessage) (any, error) {
		if method == svc.TelCalls {
			listed <- struct{}{}
			return []ui.TelCall{{ID: "c1", State: "active"}}, nil
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	nc, err := net.Dial("unix", filepath.Join(dir, svc.TelephonydSock))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { followSession(nc, r); close(done) }()
	<-listed
	deadline := time.Now().Add(5 * time.Second)
	for r.Setup().Verb != "HiFi" && time.Now().Before(deadline) {
		hub.Broadcast(map[string]any{"event": svc.TelEventCall, "call": ui.TelCall{ID: "c1", State: "ended"}})
		time.Sleep(20 * time.Millisecond)
	}
	nc.Close()
	<-done
	want := []string{"set|_verb|Voice Call|set|_enadev|Earpiece|set|_enadev|Mic|set|_verb|HiFi|set|_enadev|Speaker"}
	if got := tool.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("alsaucm ran %q, want %q", got, want)
	}
}

// The ALSA sink plays to the PCM of the card's set-up and back to its
// own; in auto mode it keeps picking its card.
func TestRouteSink(t *testing.T) {
	s, _ := openSink("alsa:plughw:0,2")
	routeSink(s, "plughw:0,0")
	if s.Name() != "alsa:plughw:0,0" {
		t.Fatal(s.Name())
	}
	routeSink(s, "")
	if s.Name() != "alsa:plughw:0,2" {
		t.Fatal(s.Name())
	}
	auto := &alsaSink{auto: true, device: "plughw:0,0"}
	routeSink(auto, "plughw:1,0")
	if auto.Name() != "alsa:plughw:0,0" {
		t.Fatal(auto.Name())
	}
	routeSink(&nullSink{}, "plughw:0,0")
}

func TestSilence(t *testing.T) {
	s := &silence{}
	buf := []float64{1, 2, 3}
	if !s.Fill(buf) || buf[0] != 0 || buf[2] != 0 || s.Pos() != 3 || s.Len() != 0 {
		t.Fatalf("%v %d", buf, s.Pos())
	}
}
