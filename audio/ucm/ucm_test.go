package ucm

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// line is a run's commands as a shell would show them.
func line(cmds []string) string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		if c == "" || strings.Contains(c, " ") {
			c = strconv.Quote(c)
		}
		out[i] = c
	}
	return strings.Join(out, " ")
}

// fakeTool records each alsaucm run; a run naming fail fails, as
// alsaucm does on a device the profile lacks.
type fakeTool struct {
	runs []string
	fail string
}

func (f *fakeTool) Run(cmds []string) error {
	l := line(cmds)
	f.runs = append(f.runs, l)
	if f.fail != "" && strings.Contains(l, f.fail) {
		return errors.New("alsaucm: error failed to set _enadev=" + f.fail + ": No such file or directory")
	}
	return nil
}

func (f *fakeTool) expect(t *testing.T, step string, want ...string) {
	t.Helper()
	if !slices.Equal(f.runs, want) {
		t.Fatalf("%s: alsaucm ran\n  %s\nwant\n  %s", step, strings.Join(f.runs, "\n  "), strings.Join(want, "\n  "))
	}
	f.runs = nil
}

// The OnePlus 6T's set-ups (oneplus-fajita.conf).
func phone(tool Tool, state string) *Router {
	return &Router{
		Tool:     tool,
		Media:    Setup{Verb: "HiFi", Devices: []string{"Speaker"}},
		Earpiece: Setup{Verb: "Voice Call", Devices: []string{"Earpiece", "Mic"}, PCM: "plughw:0,0"},
		Speaker:  Setup{Verb: "Voice Call", Devices: []string{"Speaker1", "Mic"}, PCM: "plughw:0,2"},
		Mic:      "Mic",
		State:    state,
	}
}

const (
	hifi     = `set _verb HiFi set _enadev Speaker`
	call     = `set _verb "Voice Call" set _enadev Earpiece set _enadev Mic`
	callSpk  = `set _verb "Voice Call" set _enadev Speaker1 set _enadev Mic`
	toCall   = hifi + ` ` + call
	fromCall = call + ` ` + hifi
)

func tel(id, state string, muted bool) ui.TelCall {
	return ui.TelCall{ID: id, Number: "10086", Dir: "out", State: state, Muted: muted}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMoves(t *testing.T) {
	for _, c := range []struct {
		from, to Setup
		want     string
	}{
		{Setup{}, Setup{Verb: "HiFi", Devices: []string{"Speaker"}}, hifi},
		{Setup{Verb: "HiFi", Devices: []string{"Speaker"}}, Setup{Verb: "Voice Call", Devices: []string{"Earpiece", "Mic"}}, toCall},
		{Setup{Verb: "Voice Call", Devices: []string{"Earpiece", "Mic"}}, Setup{Verb: "Voice Call", Devices: []string{"Speaker1", "Mic"}},
			call + ` set _enadev Speaker1 set _disdev Earpiece`},
		{Setup{Verb: "Voice Call", Devices: []string{"Earpiece", "Mic"}}, Setup{Verb: "Voice Call", Devices: []string{"Earpiece"}},
			call + ` set _disdev Mic`},
	} {
		if got := line(Moves(c.from, c.to)); got != c.want {
			t.Errorf("%v -> %v:\n  %s\nwant\n  %s", c.from, c.to, got, c.want)
		}
	}
}

// The first start since boot runs the profile's boot sequence and sets
// media up, as the drop-in's ExecStartPre did; a restart without a call
// changes nothing.
func TestFirstStart(t *testing.T) {
	f := &fakeTool{}
	state := filepath.Join(t.TempDir(), "audiod-ucm.json")
	r := phone(f, state)
	must(t, r.Start())
	f.expect(t, "start", `set _boot ""`, hifi)
	must(t, r.SetCalls(nil))
	f.expect(t, "no calls")
	if r.PCM() != "" || r.Setup().Verb != "HiFi" {
		t.Fatalf("pcm %q setup %v", r.PCM(), r.Setup())
	}
	r = phone(f, state)
	must(t, r.Start())
	must(t, r.SetCalls(nil))
	f.expect(t, "restart")
}

// A call: the earpiece from dialing on, the speaker and back, mute
// keeping the microphone out whatever the output, media again at the
// end; the next call starts on the earpiece though nobody said so.
func TestCall(t *testing.T) {
	f := &fakeTool{}
	r := phone(f, filepath.Join(t.TempDir(), "audiod-ucm.json"))
	must(t, r.Start())
	must(t, r.SetCalls(nil))
	f.runs = nil

	must(t, r.Call(tel("c1", "dialing", false)))
	f.expect(t, "dialing", toCall)
	if r.PCM() != "plughw:0,0" {
		t.Fatalf("pcm %q", r.PCM())
	}
	must(t, r.Call(tel("c1", "alerting", false)))
	must(t, r.Call(tel("c1", "active", false)))
	f.expect(t, "connected")

	must(t, r.SetRoute(ui.RouteSpeaker))
	f.expect(t, "speaker", call+` set _enadev Speaker1 set _disdev Earpiece`)
	if r.PCM() != "plughw:0,2" {
		t.Fatalf("pcm %q", r.PCM())
	}
	must(t, r.Call(tel("c1", "active", true)))
	f.expect(t, "mute", callSpk+` set _disdev Mic`)
	must(t, r.SetRoute(ui.RouteEarpiece))
	f.expect(t, "earpiece, muted", `set _verb "Voice Call" set _enadev Speaker1 set _enadev Earpiece set _disdev Speaker1`)
	must(t, r.Call(tel("c1", "active", false)))
	f.expect(t, "unmute", `set _verb "Voice Call" set _enadev Earpiece set _enadev Mic`)
	must(t, r.SetRoute(ui.RouteSpeaker))
	f.runs = nil

	must(t, r.Call(tel("c1", "ended", false)))
	f.expect(t, "ended", callSpk+` `+hifi)
	if r.PCM() != "" {
		t.Fatalf("pcm %q", r.PCM())
	}
	must(t, r.SetRoute(ui.RouteEarpiece))
	f.expect(t, "the shell's reset")
	must(t, r.Call(tel("c2", "dialing", false)))
	f.expect(t, "next call", toCall)
}

// Two calls: a second one ringing in and answered (the first held)
// changes nothing; the mute of the call that talks counts; a held call
// alone keeps the call set-up.
func TestTwoCalls(t *testing.T) {
	f := &fakeTool{}
	r := phone(f, "")
	must(t, r.Start())
	must(t, r.SetCalls([]ui.TelCall{tel("c1", "active", true)}))
	f.expect(t, "first call, muted", `set _boot ""`, hifi, hifi+` set _verb "Voice Call" set _enadev Earpiece`)

	in := tel("c2", "incoming", false)
	in.Dir = "in"
	must(t, r.Call(in))
	f.expect(t, "waiting")
	must(t, r.Call(tel("c1", "held", true)))
	in.State = "active"
	must(t, r.Call(in))
	f.expect(t, "answered", `set _verb "Voice Call" set _enadev Earpiece set _enadev Mic`)
	must(t, r.Call(tel("c2", "ended", false)))
	f.expect(t, "held alone")
	must(t, r.Call(tel("c1", "active", true)))
	f.expect(t, "resumed, muted", call+` set _disdev Mic`)
	must(t, r.Call(tel("c1", "ended", false)))
	f.expect(t, "last call ended", `set _verb "Voice Call" set _enadev Earpiece `+hifi)
}

// audiod restarting mid-call: the new run takes the card as it is (the
// speaker, its PCM) and touches it only once telephonyd has said what
// calls there are; a call that ended meanwhile then takes it back to
// media.
func TestRestartMidCall(t *testing.T) {
	state := filepath.Join(t.TempDir(), "audiod-ucm.json")
	f := &fakeTool{}
	r := phone(f, state)
	must(t, r.Start())
	must(t, r.SetCalls([]ui.TelCall{tel("c1", "active", false)}))
	must(t, r.SetRoute(ui.RouteSpeaker))
	f.runs = nil

	r = phone(f, state)
	must(t, r.Start())
	if r.PCM() != "plughw:0,2" {
		t.Fatalf("pcm %q before telephonyd", r.PCM())
	}
	must(t, r.Call(tel("c1", "active", true)))
	f.expect(t, "before telephonyd's list")
	must(t, r.SetCalls([]ui.TelCall{tel("c1", "active", false)}))
	f.expect(t, "the call goes on")
	must(t, r.Call(tel("c1", "ended", false)))
	f.expect(t, "ended", callSpk+` `+hifi)

	must(t, r.SetCalls([]ui.TelCall{tel("c2", "active", false)}))
	must(t, r.SetRoute(ui.RouteSpeaker))
	f.runs = nil
	r = phone(f, state)
	must(t, r.Start())
	must(t, r.SetCalls(nil))
	f.expect(t, "ended while audiod was down", callSpk+` `+hifi)
	must(t, r.Call(tel("c3", "dialing", false)))
	f.expect(t, "next call", toCall)
}

// A profile without the speaker device: the call stays on the earpiece,
// and mute still works.
func TestSpeakerMissing(t *testing.T) {
	f := &fakeTool{fail: "Speaker1"}
	r := phone(f, "")
	must(t, r.Start())
	must(t, r.SetCalls([]ui.TelCall{tel("c1", "active", false)}))
	f.runs = nil
	if err := r.SetRoute(ui.RouteSpeaker); err == nil || !strings.Contains(err.Error(), "Speaker1") {
		t.Fatalf("speaker: %v", err)
	}
	f.expect(t, "speaker", call+` set _enadev Speaker1 set _disdev Earpiece`)
	if r.PCM() != "plughw:0,0" || r.Setup().Devices[0] != "Earpiece" {
		t.Fatalf("pcm %q setup %v", r.PCM(), r.Setup())
	}
	must(t, r.Call(tel("c1", "active", true)))
	f.expect(t, "mute", call+` set _disdev Mic`)
}

// Without a call verb the router only sets media up: calls change
// nothing.
func TestMediaOnly(t *testing.T) {
	f := &fakeTool{}
	r := &Router{Tool: f, Media: Setup{Verb: "HiFi", Devices: []string{"Speaker"}}}
	must(t, r.Start())
	must(t, r.Call(tel("c1", "active", false)))
	must(t, r.SetRoute(ui.RouteSpeaker))
	f.expect(t, "media only", `set _boot ""`, hifi)
}

// The real Alsaucm passes each command as its own argument (a verb with
// a space, the boot sequence's empty value) and reports alsaucm's error.
func TestAlsaucm(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]\\n' \"$a\"; done >>" + log + "\n" +
		"case \"$*\" in *Broken*) echo 'alsaucm: error failed to set _enadev=Broken: No such file or directory' >&2; exit 1;; esac\n"
	bin := filepath.Join(dir, "alsaucm")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a := Alsaucm{Card: "hw:0", Path: bin}
	must(t, a.Run([]string{"set", "_boot", ""}))
	must(t, a.Run(Moves(Setup{Verb: "HiFi", Devices: []string{"Speaker"}}, Setup{Verb: "Voice Call", Devices: []string{"Earpiece"}})))
	raw, _ := os.ReadFile(log)
	want := "[-c]\n[hw:0]\n[set]\n[_boot]\n[]\n" +
		"[-c]\n[hw:0]\n[set]\n[_verb]\n[HiFi]\n[set]\n[_enadev]\n[Speaker]\n[set]\n[_verb]\n[Voice Call]\n[set]\n[_enadev]\n[Earpiece]\n"
	if string(raw) != want {
		t.Fatalf("args:\n%s\nwant:\n%s", raw, want)
	}
	err := a.Run([]string{"set", "_enadev", "Broken"})
	if err == nil || !strings.Contains(err.Error(), "No such file or directory") || !strings.Contains(err.Error(), `"_enadev" "Broken"`) {
		t.Fatalf("error %v", err)
	}
}
