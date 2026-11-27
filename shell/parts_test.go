package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/sensor"
)

const partsScript = "sleep 10\nsnap 00-boot\n#part a\n== a1\nrequire-env KARIO_TEST_NEVER_SET\nsleep 10\n" +
	"#part b\n== b1\nsleep 10\n#part a\n== a2\nrequire-previous\nsleep 10\n#part c\n== c1\nrequire-previous\nsleep 10\n"

// The device's parallel run (phone-shell --parts) runs some parts of the
// script in one Shell: the boot, then the parts named, each case at its own
// line; a part left for another keeps its "#part" line, so require-previous
// looks back in the case's own part only, as in the whole script.
func TestSelectParts(t *testing.T) {
	if !alone(t) {
		return
	}
	if got, err := ScriptParts(partsScript); err != nil || strings.Join(got, " ") != "a b c" {
		t.Fatalf("parts %q, %v", got, err)
	}
	got, err := SelectParts(partsScript, []string{"c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	want := "sleep 10\nsnap 00-boot\n#part a\n== a1\nrequire-env KARIO_TEST_NEVER_SET\nsleep 10\n" +
		"\n\n\n\n== a2\nrequire-previous\nsleep 10\n#part c\n== c1\nrequire-previous\nsleep 10\n"
	if got != want {
		t.Fatalf("selected:\n%s\nwant:\n%s", got, want)
	}
	// A part that is not the script's first waits for the idle screen
	// where the first began, and keeps its own name.
	b, err := SelectParts(partsScript, []string{"b"})
	if want := "sleep 10\nsnap 00-boot\nat sys.idle 5000\n\n\n\n#part b\n== b1\nsleep 10" + strings.Repeat("\n", 9); err != nil || b != want {
		t.Fatalf("selected b (%v):\n%q\nwant:\n%q", err, b, want)
	}
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	r.Run(got)
	if line := caseLine(r.Cases); line != "a1 SKIP, a2 SKIP, c1 PASS" {
		t.Errorf("cases %s\n%s", line, out.String())
	}
	if _, err := SelectParts(partsScript, []string{"a", "d"}); err == nil || !strings.Contains(err.Error(), `no part "d"`) {
		t.Errorf("an unknown part: %v", err)
	}
	if _, err := ScriptParts("sleep 1\n== early\n#part a\n== a1\n"); err == nil {
		t.Error("a case before the first part line was taken")
	}
}

// A part starts from the machine's clock and the boot's battery and light,
// whatever the part before it in the same Shell left (the emulator runs
// several parts in one Shell: family came after power's 995 s of timeshift
// and its downtime case ended early), and gets its own back where the
// script returns to it.
func TestPartState(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	type state struct {
		skew  time.Duration
		power int
		lux   float64
	}
	now := func() state {
		st := state{skew: c.clockSkew, power: -1}
		if c.powerOverride != nil {
			st.power = c.powerOverride.Level
		}
		st.lux, _ = c.screen.als.Lux()
		return st
	}
	script := "sleep 10\n#part a\n== a1\ntimeshift 3600\npower 40\nlux 5\n#part b\n== b1\nsleep 10\n"
	// A job due in two hours has one left after a's hour, in b too.
	c.jobs = []*job{{App: "com.example.none", ID: "sync", Every: 120, Next: c.now().Add(2 * time.Hour)}}
	for i, s := range []struct {
		script string
		want   state
	}{
		{script, state{0, -1, sensor.SimLux}},
		{script + "#part a\n== a2\nsleep 10\n", state{time.Hour, 40, 5}},
		{"sleep 10\n#part a\n== a1\nclock-at 06:59:50\n", state{}},
	} {
		var out strings.Builder
		r := &Runner{C: c, Out: &out}
		if !r.Run(s.script) {
			t.Fatalf("%q:\n%s", s.script, out.String())
		}
		if wait := c.jobs[0].Next.Sub(c.now()); i == 0 && (wait < 55*time.Minute || wait > time.Hour) {
			t.Errorf("%q: the job is due in %v, want an hour", s.script, wait)
		}
		got := now()
		if s.want == (state{}) {
			if at := c.now().Format("15:04:05"); at < "06:59:50" || at > "06:59:59" {
				t.Errorf("clock-at 06:59:50: the shell's clock reads %s", at)
			}
			continue
		}
		if got != s.want {
			t.Errorf("%q ends with %+v, want %+v", s.script, got, s.want)
		}
	}
}

// The script's switches the runner does not keep for each part
// (partstate.go) are back where they rest wherever the script goes from one
// part to another, so the next part in the same Shell does not find them
// set; a case that pins the time of day (clock-at) gives the clock back at
// its end. Not checked: scan-code, which the developer machine's camera
// starts with as the chat part leaves it.
func TestPartsLeaveSwitchesAtRest(t *testing.T) {
	rest := map[string]string{
		"keypad": "keypad qwerty", "fn-layer": "fn-layer", "buttons-only": "buttons-only off", "power-key": "power-key sku",
		"no-light": "no-light off", "sound-switch": "sound-switch restore", "fingerprint-at": "fingerprint-at own",
		"usb-cable": "usb-cable off", "cell-veth": "cell-veth off", "carddav-net": "carddav-net real",
		"carddav-server": "carddav-server off", "parent-server": "parent-server off", "theft-server": "theft-server off",
		"keep-server": "keep-server off", "vpn-peer": "vpn-peer off", "nfc picker": "nfc picker off",
		"nfc holidays": "nfc holidays none", "tel slow": "tel slow 0", "tel roaming": "tel roaming off",
	}
	set := map[string]string{}
	part, kase, clock := "", "", ""
	end := func(n int, next string) {
		if clock != "" && clock != "clock-reset" {
			t.Errorf("line %d: %q leaves the clock at %q", n, kase, clock)
		}
		clock = ""
		if next == part {
			return
		}
		for name, line := range set {
			if line != rest[name] {
				t.Errorf("line %d: part %s goes on to %s with %q (at rest: %q)", n, part, next, line, rest[name])
			}
		}
		clear(set)
	}
	lines := strings.Split(SelftestScript+"\n"+CTSScript, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, partMark):
			next := strings.TrimSpace(strings.TrimPrefix(l, partMark))
			end(i+1, next)
			part = next
		case strings.HasPrefix(l, "== "):
			end(i+1, part)
			kase = strings.TrimPrefix(l, "== ")
		case strings.HasPrefix(l, "clock-at ") || l == "clock-reset":
			clock = l
		}
		for name := range rest {
			if l == name || strings.HasPrefix(l, name+" ") {
				set[name] = l
			}
		}
	}
	end(len(lines), "(end)")
}

// The compatibility cases are a part of their own after the selftest's.
func TestCTSScriptIsAPart(t *testing.T) {
	parts, err := ScriptParts(SelftestScript + "\n" + CTSScript)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range parts {
		if p == "cts" {
			n++
		}
	}
	if parts[len(parts)-1] != "cts" || n != 1 {
		t.Errorf("parts %v", parts)
	}
}
