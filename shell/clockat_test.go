package shell

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// e2eClockEnv names the time of day the child of TestScreenTimeCaseBeforeSeven
// runs at.
const e2eClockEnv = "KARIO_E2E_CLOCK"

// The screen-time case starts downtime now, which lasts until the
// schedule's end at 07:00: run at 06:59:50 (the emulator ran it there after
// the power part's 995 s of timeshift), the downtime ended before the case
// looked again and the snake opened. The case pins its clock, so its
// downtime steps hold whenever it runs.
func TestScreenTimeCaseBeforeSeven(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end stack skipped in -short mode")
	}
	if at := os.Getenv(e2eClockEnv); at != "" {
		screenTimeAt(t, at)
		return
	}
	go stagedPrefix()
	t.Parallel()
	prefix, err := stagedPrefix()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(filepath.Dir(prefix), "run-"+t.Name())
	r := runChild(t, nil, e2eClockEnv+"=06:59:50", e2ePrefixEnv+"="+prefix, e2eRunFileEnv+"="+file)
	reapChild(file)
	r.report(t)
}

// screenTimeAt is TestScreenTimeCaseBeforeSeven in its child: the boot, then
// the case's downtime steps with the shell's clock at that time of day.
func screenTimeAt(t *testing.T, at string) {
	c, _ := e2eStack(t)
	d, err := clockAt(c.now(), at)
	if err != nil {
		t.Fatal(err)
	}
	c.setClockSkew(d)
	boot, _, _ := strings.Cut(SelftestScript, "\n"+partMark)
	script := boot + "\nat sys.idle 5000\n== downtime\n" + downtimeSteps(t)
	var out strings.Builder
	if !(&Runner{C: c, Out: &out, Scale: raceScale}).Run(script) {
		t.Fatalf("at %s:\n%s\n%s", at, script, out.String())
	}
	t.Log("\n" + out.String())
}

// downtimeSteps are the screen-time case's steps from the idle screen to
// downtime started, the snake held, the dialer open, the snake held again
// and the downtime ended, with 20 s passing while the dialer is open (the
// emulator is that much slower); the clock steps before and after them in
// the case go with them.
func downtimeSteps(t *testing.T) string {
	lines := strings.Split(SelftestScript, "\n")
	page := slices.Index(lines, "at downtime")
	if page < 0 {
		t.Fatal("the selftest has no downtime page")
	}
	head := page
	for head > 0 && !strings.HasPrefix(lines[head], "== ") {
		head--
	}
	end := len(lines)
	if i := slices.IndexFunc(lines[page:], func(l string) bool { return strings.HasPrefix(l, "== ") }); i >= 0 {
		end = page + i
	}
	from := page
	for from > head && lines[from] != "at sys.idle" {
		from--
	}
	to := page + slices.Index(lines[page:end], "at com.phone.snake/board")
	if from == head || to < page {
		t.Fatalf("lines %d-%d: no idle screen before the downtime page or no snake after it", head+1, end)
	}
	var steps []string
	clock := func(ls []string) {
		for _, l := range ls {
			if strings.HasPrefix(l, "clock-") {
				steps = append(steps, l)
			}
		}
	}
	clock(lines[head:from])
	for _, l := range lines[from+1 : to+1] {
		steps = append(steps, l)
		if l == "at com.phone.dialer/main" {
			steps = append(steps, "timeshift 20")
		}
	}
	steps = append(steps, "key f4", "at sys.idle")
	clock(lines[to:end])
	return strings.Join(steps, "\n") + "\n"
}
