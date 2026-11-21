// Package calendar_test fuzzes the calendar's iCalendar parser
// (src/ics.ts), TypeScript, from Go's fuzzer as tools/cfuzz does the C
// parsers: esbuild bundles the parser with a small harness, one Node.js
// process per fuzzing worker reads the inputs as JSON lines and answers
// each one. A throw, a hang (5 s), events past the bounds or a file that
// does not read back as itself after being written fails the input. The
// target is skipped without Node.js ($KARIO_NODE or node on PATH).
package calendar_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

const harness = `
import { parseICS, serialiseICS } from "./src/ics.ts";
import { vzoneSpans } from "./src/vtz.ts";
import { createInterface } from "node:readline";
const strip = (events, first) => events.map((e, i) => ({ ...e, id: "", uid: first && !first[i]?.uid ? "" : e.uid }));
for await (const line of createInterface({ input: process.stdin })) {
  let out;
  try {
    const text = JSON.parse(line);
    let n = 0;
    const a = parseICS(text, () => "f" + ++n);
    const bad = a.events.find((e) => [...e.title].length > 200 || [...e.notes].length > 2000 || [...e.location].length > 200 || e.alarms.length > 4 || e.exdates.length > 200 || (e.attendees?.length ?? 0) > 50 || (e.attendees ?? []).some((x) => [...x.name].length > 100 || [...x.addr].length > 200));
    const zones = Object.values(a.zones);
    const badZone = zones.find((z) => z.obs.length > 32 || z.obs.some((o) => o.rdates.length > 64) || vzoneSpans(z).length > 1001 || vzoneSpans(z).some((s, i, all) => !Number.isFinite(s.start) || (i > 0 && s.start <= all[i - 1].start)));
    if (a.events.length > 2000 || bad || zones.length > 16 || badZone) out = { err: "past the bounds: " + JSON.stringify(bad ?? badZone ?? a.events.length).slice(0, 300) };
    else {
      const b = parseICS(serialiseICS(a.events, "20261117T000000Z", a.zones), () => "g" + ++n);
      const x = JSON.stringify(strip(a.events)), y = JSON.stringify(strip(b.events, a.events));
      out = b.problems.length ? { err: "the written file does not read: " + JSON.stringify(b.problems.slice(0, 3)) } : x === y ? { ok: a.events.length } : { err: "a round trip changed it:\n" + x.slice(0, 600) + "\n" + y.slice(0, 600) };
    }
  } catch (e) {
    out = { err: "threw: " + (e && e.stack || e) };
  }
  process.stdout.write(JSON.stringify(out) + "\n");
}
`

type runner struct {
	mu  sync.Mutex
	in  io.WriteCloser
	out *bufio.Reader
	err error
}

var (
	once sync.Once
	run  runner
)

func start(t *testing.T) *runner {
	once.Do(func() {
		node := os.Getenv("KARIO_NODE")
		if node == "" {
			node, run.err = exec.LookPath("node")
			if run.err != nil {
				return
			}
		}
		dir, err := filepath.Abs(".")
		if err != nil {
			run.err = err
			return
		}
		res := api.Build(api.BuildOptions{
			Stdin:    &api.StdinOptions{Contents: harness, ResolveDir: dir, Sourcefile: "harness.mjs", Loader: api.LoaderJS},
			Bundle:   true,
			Format:   api.FormatESModule,
			Target:   api.ES2023,
			Platform: api.PlatformNode,
			External: []string{"node:*"},
		})
		if len(res.Errors) > 0 {
			run.err = errors.New(res.Errors[0].Text)
			return
		}
		f, err := os.CreateTemp("", "ics-harness-*.mjs")
		if err != nil {
			run.err = err
			return
		}
		_, _ = f.Write(res.OutputFiles[0].Contents)
		f.Close()
		cmd := exec.Command(node, f.Name())
		cmd.Stderr = os.Stderr
		run.in, _ = cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		run.out = bufio.NewReaderSize(stdout, 1<<20)
		run.err = cmd.Start()
	})
	if run.err != nil {
		t.Skipf("no harness: %v", run.err)
	}
	return &run
}

func (r *runner) parse(t *testing.T, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line, _ := json.Marshal(text)
	if _, err := r.in.Write(append(line, '\n')); err != nil {
		t.Fatalf("the harness is gone: %v", err)
	}
	type answer struct {
		OK  *int   `json:"ok"`
		Err string `json:"err"`
	}
	got := make(chan answer, 1)
	go func() {
		var a answer
		l, err := r.out.ReadBytes('\n')
		if err != nil || json.Unmarshal(l, &a) != nil {
			a.Err = "the harness answered " + string(l)
		}
		got <- a
	}()
	select {
	case a := <-got:
		if a.OK == nil {
			t.Fatalf("%s\ninput: %q", a.Err, text)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the parser hung on %q", text)
	}
}

func FuzzICS(f *testing.F) {
	f.Add("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:a@b\r\nDTSTART;TZID=America/New_York:20261124T090000\r\nDTEND;TZID=America/New_York:20261124T100000\r\n" +
		"RRULE:FREQ=WEEKLY;BYDAY=TU;UNTIL=20261222T140000Z\r\nEXDATE;TZID=America/New_York:20261208T090000\r\nSUMMARY:Stand-up\\, A\r\nDESCRIPTION:one\\ntwo\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	f.Add("BEGIN:VEVENT\nDTSTART;VALUE=DATE:20261001\nDTEND;VALUE=DATE:20261008\nSUMMARY:\u56fd\u5e86\nRRULE:FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=1;COUNT=99999\nEND:VEVENT\n")
	f.Add("BEGIN:VEVENT\nDTSTART:20261117T013000Z\nDURATION:P1DT2H\nRECURRENCE-ID;TZID=\"Asia/Shanghai\":20261117T090000\nBEGIN:VALARM\nACTION:AUDIO\nTRIGGER;VALUE=DATE-TIME:20261117T000000Z\nEND:VALARM\nEND:VEVENT")
	f.Add("BEGIN:VEVENT\nDTSTART:2026\nEND:VTODO\nBEGIN:VEVENT\nX-;:\n \n\tEND:VEVENT")
	f.Add("BEGIN:VCALENDAR\r\nBEGIN:VTIMEZONE\r\nTZID:Customized Time Zone\r\nBEGIN:STANDARD\r\nDTSTART:16010101T020000\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\n" +
		"RRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=11\r\nEND:STANDARD\r\nBEGIN:DAYLIGHT\r\nDTSTART:16010101T020000\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n" +
		"RRULE:FREQ=YEARLY;BYDAY=2SU;BYMONTH=3;UNTIL=20370308T070000Z\r\nRDATE:20260601T020000,20270601T020000Z\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\nBEGIN:VEVENT\r\nUID:o@x\r\n" +
		"DTSTART;TZID=\"Customized Time Zone\":20261117T090000\r\nDTEND;TZID=Customized Time Zone:20261117T100000\r\nRRULE:FREQ=DAILY;BYHOUR=9,17;BYMINUTE=0,30;COUNT=9\r\n" +
		"EXDATE:20261118T140000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	f.Add("BEGIN:VTIMEZONE\nTZID:a,b\nBEGIN:DAYLIGHT\nDTSTART:19700101\nTZOFFSETFROM:+1800\nTZOFFSETTO:-1859\nRRULE:FREQ=DAILY;BYHOUR=0,12\nEND:DAYLIGHT\nEND:VTIMEZONE\n" +
		"BEGIN:VEVENT\nDTSTART;TZID=a,b:20261117T000000\nRRULE:FREQ=YEARLY;BYWEEKNO=-1,53;BYYEARDAY=-366,1;BYDAY=MO;BYSETPOS=-1\nEND:VEVENT\n")
	f.Add("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:p@x\r\nSEQUENCE:1\r\nDTSTART:20301104T190000Z\r\nSUMMARY:party\r\n" +
		"ORGANIZER;CN=\"Wang, Fang\":mailto:fang@x\r\nATTENDEE;CN=Lei;ROLE=CHAIR;PARTSTAT=x-odd;RSVP=TRUE:mailto:lei@x\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	f.Add("BEGIN:VCALENDAR\nMETHOD:REPLY\nBEGIN:VEVENT\nUID:p@x\nRECURRENCE-ID;TZID=Nowhere:20301111T100000\nATTENDEE;PARTSTAT=DECLINED:mailto:lei@x\nEND:VEVENT\nEND:VCALENDAR\n")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 64<<10 {
			return
		}
		start(t).parse(t, text)
	})
}
