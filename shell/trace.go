package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/ui"
)

// Profiling the developer's app (kario run -profile, 29-app-development.md
// §3, as Instruments' timeline): phone-shell --profile writes what happens
// between the Shell and that app as Chrome's Trace Event format (a JSON
// array, one event a line), which Perfetto (ui.perfetto.dev) and
// chrome://tracing load: CPU and memory samples of its process (the
// counters the battery alerts and the low-memory killer read), the pages
// it sends with the Shell's time on each, its capability calls from request
// to answer, the time from an action the Shell sends to the next page the
// app sends, and the responsiveness pings (anr.go); with a renderer that
// times its frames (phone-paint headless, the "timing" feature), each frame
// from the Shell sending it to the renderer's report, the renderer's own
// steps inside. Each event is written as it happens, so a Shell that is
// killed leaves a usable file; kario adds the closing bracket.

// The trace's tracks: one process for the app, a thread for each kind;
// another for the renderer's frames.
const (
	tracePid       = 1
	traceTidPages  = 1
	traceTidCalls  = 2
	traceTidInput  = 3
	traceTidPing   = 4
	traceTidSystem = 5

	tracePidRenderer = 2
	traceTidFrames   = 1
)

// traceEvery is how often the app's CPU and memory are sampled; an action
// that brings no page within traceInputWait is dropped from the latencies.
const (
	traceEvery     = 250 * time.Millisecond
	traceInputWait = 5 * time.Second
)

// TraceEvent is one event of Chrome's Trace Event format.
type TraceEvent struct {
	Name string         `json:"name"`
	Cat  string         `json:"cat,omitempty"`
	Ph   string         `json:"ph"`
	Ts   float64        `json:"ts"`
	Dur  *float64       `json:"dur,omitempty"`
	Pid  int            `json:"pid"`
	Tid  int            `json:"tid"`
	ID   string         `json:"id,omitempty"`
	S    string         `json:"s,omitempty"`
	Args map[string]any `json:"args,omitempty"`
}

// Tracer writes the developer's app's timeline.
type Tracer struct {
	mu    sync.Mutex
	f     *os.File
	start time.Time
	app   string
	anr   time.Duration
	input []traceInput
	calls map[int]traceCall
	// answered: answers sent while the Shell was still handling the
	// request, which traceIn only sees afterwards.
	answered map[int]traceAnswer
	pings    map[string]time.Time
	seq      int
	n        int
	cpuLast  time.Duration
	cpuAt    time.Time
	cpuPid   int
	// rendered: the renderer's track has its names.
	rendered bool
	stop     chan struct{}
	err      error
}

type traceInput struct {
	id   int
	what string
	at   time.Time
}

type traceCall struct {
	cap string
	at  time.Time
}

type traceAnswer struct {
	ok  bool
	err string
	at  time.Time
}

// active is the tracer the app's messages go to (one per Shell process).
var active atomic.Pointer[Tracer]

func newTracer(f *os.File, app string, anr time.Duration, start time.Time) *Tracer {
	t := &Tracer{f: f, start: start, app: app, anr: anr, calls: map[int]traceCall{}, answered: map[int]traceAnswer{}, pings: map[string]time.Time{}, stop: make(chan struct{})}
	t.write("[\n")
	for _, m := range []TraceEvent{
		{Name: "process_name", Ph: "M", Pid: tracePid, Args: map[string]any{"name": app}},
		{Name: "thread_name", Ph: "M", Pid: tracePid, Tid: traceTidPages, Args: map[string]any{"name": "pages"}},
		{Name: "thread_name", Ph: "M", Pid: tracePid, Tid: traceTidCalls, Args: map[string]any{"name": "capability calls"}},
		{Name: "thread_name", Ph: "M", Pid: tracePid, Tid: traceTidInput, Args: map[string]any{"name": "input to update"}},
		{Name: "thread_name", Ph: "M", Pid: tracePid, Tid: traceTidPing, Args: map[string]any{"name": "ANR pings"}},
		{Name: "thread_name", Ph: "M", Pid: tracePid, Tid: traceTidSystem, Args: map[string]any{"name": "lifecycle"}},
	} {
		t.emit(m)
	}
	return t
}

// StartTrace profiles the developer's app into path until Close.
func (c *Core) StartTrace(path string) (*Tracer, error) {
	app := ""
	for _, a := range c.apps {
		if c.isDev(a) {
			app = a.M.ID
		}
	}
	if app == "" {
		return nil, fmt.Errorf("--profile: no developer's app (--dev-app) to profile")
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	t := newTracer(f, app, c.anrAfter(), time.Now())
	active.Store(t)
	go func() {
		tick := time.NewTicker(traceEvery)
		defer tick.Stop()
		for {
			select {
			case <-t.stop:
				return
			case <-tick.C:
				c.Post(func() { c.traceSample(t) })
			}
		}
	}()
	return t, nil
}

// Close ends the trace: the array closed, the file too.
func (t *Tracer) Close() error {
	active.CompareAndSwap(t, nil)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return t.err
	}
	close(t.stop)
	t.write("\n]\n")
	err := t.f.Close()
	t.f = nil
	if t.err != nil {
		return t.err
	}
	return err
}

func (t *Tracer) write(s string) {
	if t.f == nil || t.err != nil {
		return
	}
	if _, err := t.f.WriteString(s); err != nil {
		t.err = err
	}
}

func (t *Tracer) us(at time.Time) float64 { return float64(at.Sub(t.start).Microseconds()) }

func (t *Tracer) emit(e TraceEvent) {
	if e.Pid == 0 {
		e.Pid = tracePid
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	if t.n++; t.n > 1 {
		t.write(",\n")
	}
	t.write(string(raw))
}

func dur(d time.Duration) *float64 {
	v := float64(d.Microseconds())
	return &v
}

// traceIn is a message from the app, handled from at for took.
func traceIn(a *App, m ui.Msg, at time.Time, took time.Duration) {
	if t := active.Load(); t != nil && t.app == a.M.ID {
		t.in(m, at, took)
	}
}

// traceOut is a message to the app.
func traceOut(a *App, m ui.Msg) {
	if t := active.Load(); t != nil && t.app == a.M.ID {
		t.out(m, time.Now())
	}
}

func (t *Tracer) in(m ui.Msg, at time.Time, took time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch m.T {
	case ui.MsgPush, ui.MsgReplace, ui.MsgUpdate, ui.MsgReset, ui.MsgPop:
		args := map[string]any{}
		name := m.T
		if m.Screen != nil {
			name += " " + m.Screen.ID
			raw, _ := json.Marshal(m.Screen)
			args["screen"], args["nodes"], args["bytes"] = m.Screen.ID, len(m.Screen.Body), len(raw)
		}
		t.emit(TraceEvent{Name: name, Cat: "page", Ph: "X", Ts: t.us(at), Dur: dur(took), Tid: traceTidPages, Args: args})
		for _, in := range t.input {
			if at.Sub(in.at) <= traceInputWait {
				t.emit(TraceEvent{Name: in.what, Cat: "input", Ph: "b", Ts: t.us(in.at), Tid: traceTidInput, ID: strconv.Itoa(in.id)})
				t.emit(TraceEvent{Name: in.what, Cat: "input", Ph: "e", Ts: t.us(at), Tid: traceTidInput, ID: strconv.Itoa(in.id),
					Args: map[string]any{"latency_ms": float64(at.Sub(in.at).Microseconds()) / 1000, "page": name}})
			}
		}
		t.input = t.input[:0]
	case ui.MsgReq:
		c := traceCall{cap: m.Cap, at: at}
		if a, ok := t.answered[m.Req]; ok {
			delete(t.answered, m.Req)
			t.call(m.Req, c, a)
		} else {
			t.calls[m.Req] = c
		}
	case ui.MsgPong:
		if sent, ok := t.pings[m.Value]; ok {
			delete(t.pings, m.Value)
			t.emit(TraceEvent{Name: "ping", Cat: "anr", Ph: "X", Ts: t.us(sent), Dur: dur(at.Sub(sent)), Tid: traceTidPing})
			if at.Sub(sent) >= t.anr {
				t.emit(TraceEvent{Name: "ANR", Cat: "anr", Ph: "i", S: "t", Ts: t.us(sent.Add(t.anr)), Tid: traceTidPing,
					Args: map[string]any{"silent_ms": at.Sub(sent).Milliseconds()}})
			}
		}
	case ui.MsgToast, ui.MsgNotify, ui.MsgState:
		t.emit(TraceEvent{Name: m.T, Cat: "app", Ph: "i", S: "t", Ts: t.us(at), Tid: traceTidSystem})
	}
}

func (t *Tracer) out(m ui.Msg, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch m.T {
	case ui.MsgAction, ui.MsgPopped:
		t.seq++
		what := m.T
		if m.Action != "" {
			what += " " + m.Action
		}
		if m.Action == ui.ActionKey && m.Value != "" {
			what += " " + m.Value
		}
		t.input = append(t.input, traceInput{id: t.seq, what: what, at: at})
	case ui.MsgRes:
		a := traceAnswer{ok: m.OK, err: m.Err, at: at}
		c, ok := t.calls[m.Req]
		if !ok {
			t.answered[m.Req] = a
			return
		}
		delete(t.calls, m.Req)
		t.call(m.Req, c, a)
	case ui.MsgPing:
		t.pings[m.Value] = at
	case ui.MsgStart, ui.MsgLifecycle, ui.MsgResync:
		name := m.T
		if m.State != "" {
			name += " " + m.State
		}
		t.emit(TraceEvent{Name: name, Cat: "lifecycle", Ph: "i", S: "t", Ts: t.us(at), Tid: traceTidSystem})
	}
}

func (t *Tracer) call(req int, c traceCall, a traceAnswer) {
	args := map[string]any{"ok": a.ok}
	if a.err != "" {
		args["err"] = a.err
	}
	id := "call" + strconv.Itoa(req)
	t.emit(TraceEvent{Name: c.cap, Cat: "capability", Ph: "b", Ts: t.us(c.at), Tid: traceTidCalls, ID: id})
	t.emit(TraceEvent{Name: c.cap, Cat: "capability", Ph: "e", Ts: t.us(a.at), Tid: traceTidCalls, ID: id, Args: args})
}

// rendererFrame is a frame the renderer drew: from the Shell sending it to
// the renderer's report (arrived), with what it took the renderer in
// milliseconds (reading, layout, each step drawn as the panel would, each
// step of the transition it starts), those as slices inside, back to back
// up to the report. The steps are the developer machine's times, not a
// phone's.
func (t *Tracer) rendererFrame(f sentFrame, arrived time.Time, parse, layout float64, draws, steps []float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.rendered {
		t.rendered = true
		t.emit(TraceEvent{Name: "process_name", Ph: "M", Pid: tracePidRenderer, Args: map[string]any{"name": "renderer"}})
		t.emit(TraceEvent{Name: "thread_name", Ph: "M", Pid: tracePidRenderer, Tid: traceTidFrames, Args: map[string]any{"name": "frames"}})
	}
	total := func(ms []float64) float64 {
		sum := 0.0
		for _, v := range ms {
			sum += v
		}
		return sum
	}
	args := map[string]any{"bytes": f.bytes, "patch": f.patch, "screen": f.screen, "parse_ms": parse, "layout_ms": layout,
		"draw_ms": total(draws), "draw_steps": len(draws), "transition_ms": total(steps), "transition_steps": len(steps)}
	t.emit(TraceEvent{Name: "frame " + strconv.Itoa(f.n), Cat: "frame", Ph: "X", Ts: t.us(f.at), Dur: dur(arrived.Sub(f.at)),
		Pid: tracePidRenderer, Tid: traceTidFrames, Args: args})
	ms := func(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }
	at := arrived.Add(-ms(parse + layout + total(draws) + total(steps)))
	if at.Before(f.at) {
		at = f.at
	}
	step := func(name string, v float64) {
		t.emit(TraceEvent{Name: name, Cat: "frame", Ph: "X", Ts: t.us(at), Dur: dur(ms(v)), Pid: tracePidRenderer, Tid: traceTidFrames})
		at = at.Add(ms(v))
	}
	step("parse", parse)
	step("layout", layout)
	for _, v := range draws {
		step("draw", v)
	}
	for _, v := range steps {
		step("transition step", v)
	}
}

// sample is a CPU and memory reading of the app's process (pid), at.
func (t *Tracer) sample(pid int, cpu time.Duration, mem int64, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if pid != t.cpuPid {
		t.cpuPid, t.cpuLast, t.cpuAt = pid, cpu, at
	} else if wall := at.Sub(t.cpuAt); wall > 0 {
		pct := float64(max(cpu-t.cpuLast, 0)) / float64(wall) * 100
		t.cpuLast, t.cpuAt = cpu, at
		t.emit(TraceEvent{Name: "CPU", Cat: "sample", Ph: "C", Ts: t.us(at), Args: map[string]any{"% of a core": pct}})
	}
	if mem > 0 {
		t.emit(TraceEvent{Name: "memory", Cat: "sample", Ph: "C", Ts: t.us(at), Args: map[string]any{"MB": float64(mem) / (1 << 20)}})
	}
}

func (c *Core) traceSample(t *Tracer) {
	for _, a := range c.apps {
		if a.M.ID != t.app || !a.running || a.proc == nil {
			continue
		}
		cpu, ok := appCPUTime(c, a)
		if !ok {
			return
		}
		t.sample(a.proc.Pid, cpu, c.appMemory(a), time.Now())
		return
	}
}
