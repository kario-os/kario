package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/sensor"
	"github.com/kario-os/kario/ui"
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
	// A job the boot left due in two hours is due in two in a part as it
	// starts; the part the script returns to gets its own back, one hour
	// left after a's hour.
	c.jobs = []*job{{App: "com.example.none", ID: "sync", Every: 120, Next: c.now().Add(2 * time.Hour)}}
	for _, s := range []struct {
		script string
		want   state
		wait   time.Duration
	}{
		{script, state{0, -1, sensor.SimLux}, 2 * time.Hour},
		{script + "#part a\n== a2\nsleep 10\n", state{time.Hour, 40, 5}, time.Hour},
		{"sleep 10\n#part a\n== a1\nclock-at 06:59:50\n", state{}, 0},
	} {
		var out strings.Builder
		r := &Runner{C: c, Out: &out}
		if !r.Run(s.script) {
			t.Fatalf("%q:\n%s", s.script, out.String())
		}
		if len(c.jobs) != 1 {
			t.Fatalf("%q: jobs %+v", s.script, c.jobs)
		}
		if wait := c.jobs[0].Next.Sub(c.now()); s.wait != 0 && (wait < s.wait-5*time.Minute || wait > s.wait) {
			t.Errorf("%q: the job is due in %v, want %v", s.script, wait, s.wait)
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

// A job an app scheduled in a part is that part's: one already due when
// the script goes on to another part (QEMU: Weather's, from location,
// started Weather in the background in the middle of another part's case)
// does not run there, and runs back in its own part, on the wait it had
// left.
func TestPartHoldsItsJobs(t *testing.T) {
	if !alone(t) {
		return
	}
	script := "sleep 10\n#part a\n== schedules\njob-add test.still sync 15\ntimeshift 960\n" +
		"#part b\n== another part\ntimeshift 600\nsleep 1500\njob test.still sync runs 0 100\nclock-reset\n" +
		"#part a\n== back\njob test.still sync runs 0 100\ntimeshift 600\njob test.still sync runs 1 5000\n"
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	r.Run(script)
	if line := caseLine(r.Cases); line != "schedules PASS, another part PASS, back PASS" {
		t.Errorf("cases %s\n%s", line, out.String())
	}
}

// An app running since another part started it schedules no jobs in the
// part that opens it next, as starting there would have (the store opened
// in the data part, its automatic update in the store part): that part
// takes the jobs the other left it, each due a period on, and the other
// gets its own back. An app in front as the part began, or started in it,
// takes none.
func TestPartTakesTheJobsOfAnAppItOpens(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	a := c.AppByID("test.still")
	r := &Runner{C: c, Out: io.Discard}
	r.bootState = r.partState()
	r.enterPart("a")
	if err := c.scheduleJob(a, ui.JobArgs{ID: "sync", EveryMin: 60, Charging: true}); err != nil {
		t.Fatal(err)
	}
	c.jobs[0].Runs = 2
	a.running, a.started = true, time.Now()
	defer func() { a.running, c.fg = false, nil }()
	c.fg = a
	r.enterPart("b")
	r.takeJobs()
	if len(c.jobs) != 0 {
		t.Fatalf("b has %+v, in front as it began", *c.jobs[0])
	}
	c.fg = nil
	r.takeJobs()
	c.fg = a
	r.takeJobs()
	if len(c.jobs) != 1 || c.jobs[0].Runs != 0 || !c.jobs[0].Charging || c.jobs[0].Next.Sub(c.now()) < 59*time.Minute {
		t.Fatalf("b took %+v", c.jobs)
	}
	raw, _ := json.Marshal(r.parts)
	var carried map[string]partState
	if err := json.Unmarshal(raw, &carried); err != nil {
		t.Fatal(err)
	}
	if j := carried["a"].Jobs; len(j) != 1 || j[0].Job.Runs != 2 || j[0].Wait < 59*time.Minute {
		t.Errorf("a's jobs through a restart in place: %+v", j)
	}
	r.enterPart("a")
	if len(c.jobs) != 1 || c.jobs[0].Runs != 2 || c.jobs[0].Next.Sub(c.now()) < 59*time.Minute {
		t.Fatalf("a got back %+v", c.jobs)
	}
	c.fg = nil
	r.enterPart("c")
	a.started = time.Now()
	c.fg = a
	r.takeJobs()
	if len(c.jobs) != 0 {
		t.Errorf("c took %+v, started in it", *c.jobs[0])
	}
}

// The script's switches the runner does not keep for each part
// (partstate.go) are back where they rest wherever the script goes from one
// part to another, so the next part in the same Shell does not find them
// set; a case that moves the clock (clockMoves) gives it back at its end,
// or names the next case of its part that goes on with it (clockKept).
// Not checked: scan-code, which the developer machine's camera starts with
// as the chat part leaves it.
func TestPartsLeaveSwitchesAtRest(t *testing.T) {
	for _, e := range clockLint(SelftestScript + "\n" + CTSScript) {
		t.Error(e)
	}
	rest := map[string]string{
		"keypad": "keypad qwerty", "fn-layer": "fn-layer", "buttons-only": "buttons-only off", "power-key": "power-key sku",
		"no-light": "no-light off", "sound-switch": "sound-switch restore", "fingerprint-at": "fingerprint-at own",
		"usb-cable": "usb-cable off", "cell-veth": "cell-veth off", "carddav-net": "carddav-net real",
		"carddav-server": "carddav-server off", "parent-server": "parent-server off", "theft-server": "theft-server off",
		"keep-server": "keep-server off", "vpn-peer": "vpn-peer off", "nfc picker": "nfc picker off",
		"nfc holidays": "nfc holidays none", "tel slow": "tel slow 0", "tel roaming": "tel roaming off",
	}
	set := map[string]string{}
	part := ""
	end := func(n int, next string) {
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
		}
		for name := range rest {
			if l == name || strings.HasPrefix(l, name+" ") {
				set[name] = l
			}
		}
	}
	end(len(lines), "(end)")
}

// clockKept begins a line of a case that leaves the clock moved for the
// next case of its part, which it names (the start of its name): that case
// reads what this one's clock made (an alarm or a job it set comes due
// there, a record it made is counted as recent) and gives the clock back,
// or keeps it for the next one in turn.
const clockKept = "# clock kept for: "

// clockMoves: the steps that move the Shell's clock off the machine's (the
// simulated sleep moves it on to the wake-up); clock-reset gives it back.
var clockMoves = []string{"timeshift ", "clock-at ", "nfc clock ", "ota clock ", "system-sleep on"}

// clockLint is where the cases of script leave the clock moved for the
// cases after them: a case ending with it moved and naming no next case,
// or naming one that is not the next of its part.
func clockLint(script string) []string {
	type kept struct {
		to, by string
		line   int
	}
	var errs []string
	carried := map[string]kept{}
	part, kase, moved, keep, keepLine := "", "", "", "", 0
	end := func(n int) {
		switch {
		case kase == "":
		case keep != "" && moved == "":
			errs = append(errs, fmt.Sprintf("line %d: %q keeps the clock for %q, but gave it back", keepLine, kase, keep))
		case keep != "":
			carried[part] = kept{keep, moved, keepLine}
		case moved != "":
			errs = append(errs, fmt.Sprintf("line %d: %q leaves the clock moved (%s): end it with clock-reset, or name the next case of the part that needs it (%s<its name>)",
				n, kase, moved, clockKept))
		}
		kase, moved, keep = "", "", ""
	}
	lines := strings.Split(script, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, partMark):
			end(i + 1)
			part = strings.TrimSpace(strings.TrimPrefix(l, partMark))
		case strings.HasPrefix(l, "== "):
			end(i + 1)
			kase = strings.TrimPrefix(l, "== ")
			if k, ok := carried[part]; ok {
				delete(carried, part)
				moved = k.by
				if !strings.HasPrefix(kase, k.to) {
					errs = append(errs, fmt.Sprintf("line %d: the clock is kept for %q, but the next case of part %s is %q", k.line, k.to, part, kase))
				}
			}
		case kase == "":
		case l == "clock-reset":
			moved = ""
		case strings.HasPrefix(l, clockKept):
			keep, keepLine = strings.TrimSpace(strings.TrimPrefix(l, clockKept)), i+1
		default:
			for _, m := range clockMoves {
				if strings.HasPrefix(l, m) {
					moved = l
				}
			}
		}
	}
	end(len(lines))
	for _, p := range slices.Sorted(maps.Keys(carried)) {
		errs = append(errs, fmt.Sprintf("line %d: the clock is kept for %q, but part %s has no case after it", carried[p].line, carried[p].to, p))
	}
	return errs
}

func TestClockLint(t *testing.T) {
	for _, c := range []struct{ script, want string }{
		{"#part a\n== a1\ntimeshift 60\nclock-reset\n== a2\nclock-at 12:00\nnfc clock 09:00\nclock-reset\n", ""},
		{"#part a\n== a1\ntimeshift 60\n== a2\n", `line 4: "a1" leaves the clock moved (timeshift 60)`},
		{"#part a\n== a1\nclock-at 12:00\n#part b\n== b1\n", `line 4: "a1" leaves the clock moved (clock-at 12:00)`},
		{"#part a\n== a1\nsystem-sleep on\nsystem-sleep off\n", `line 5: "a1" leaves the clock moved (system-sleep on)`},
		// Kept for the next case of the part, past another part's.
		{"#part a\n== a1\nalarm-in 120 x\ntimeshift 60\n# clock kept for: a2\n#part b\n== b1\n#part a\n== a2 rings\ntimeshift 60\nclock-reset\n", ""},
		{"#part a\n== a1\ntimeshift 60\n# clock kept for: a2\n== a2\n", `line 6: "a2" leaves the clock moved (timeshift 60)`},
		{"#part a\n== a1\ntimeshift 60\n# clock kept for: a3\n== a2\nclock-reset\n", `line 4: the clock is kept for "a3", but the next case of part a is "a2"`},
		{"#part a\n== a1\ntimeshift 60\nclock-reset\n# clock kept for: a2\n== a2\n", `line 5: "a1" keeps the clock for "a2", but gave it back`},
		{"#part a\n== a1\ntimeshift 60\n# clock kept for: a2\n#part b\n== b1\n", `line 4: the clock is kept for "a2", but part a has no case after it`},
	} {
		got := strings.Join(clockLint(c.script), "\n")
		if c.want == "" && got != "" || !strings.HasPrefix(got, c.want) {
			t.Errorf("%q:\n%s\nwant %s", c.script, got, firstNonEmpty(c.want, "nothing"))
		}
	}
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
