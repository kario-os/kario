// Package rpcfuzz runs a system service's handler on the requests a fuzz
// input holds, through wire's server loop as the service runs them, and
// checks what comes back (17-app-sandbox.md §9): an answer to every line,
// each a result or an error with a message, all within Answer once what
// they wait on is cancelled, the results encodable, the input's
// allocation bounded, and no goroutine of it left behind.
//
// The input is lines. A line that starts with '{' is written as it is: a
// request as a client writes it, or a line that is not one (which gets an
// error). Any other line is a request made from it: its first byte picks
// the method, the rest is the params, as they are when they are JSON,
// else as a JSON string. A line is written once the one before it is
// answered, or has waited a moment (a request that waits: a scan, a
// reader session), so that an input runs the same way each time.
package rpcfuzz

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/wire"
)

const (
	// MaxInput and MaxLines bound an input: longer ones are skipped, lines
	// past MaxLines dropped.
	MaxInput = 256 << 10
	MaxLines = 64
	// Answer: the time the requests of one input have, Cancel ending what
	// they wait on.
	Answer = 10 * time.Second
	// The allocation an input may make: AllocBase, and AllocPerLine (or
	// the service's own) for each request.
	AllocBase    = 16 << 20
	AllocPerLine = 4 << 20
	// settle: how long a request may take before the next is written, and
	// answers may pause before Cancel is called.
	settle = 20 * time.Millisecond
)

// answerWait is Answer (tests of this package shorten it).
var answerWait = Answer

// Service is a service under test, made for one input.
type Service struct {
	// Methods: what a line that is not an envelope picks from.
	Methods []string
	Handler wire.Handler
	// Cancel ends what requests wait on (a scan, a reader session, a
	// sensor): called whenever answers pause, and once they are all in.
	Cancel func()
	// Close lets go of the service once every request has its answer;
	// what it started must be over when it returns.
	Close func()
	// AllocPerLine, when not 0, replaces AllocPerLine.
	AllocPerLine uint64
}

// T is what Run reports to: a *testing.T.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

type request struct {
	line   []byte
	id     int
	method string
}

func requests(methods []string, data []byte) []request {
	var out []request
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, MaxInput+1)
	for sc.Scan() && len(out) < MaxLines {
		l := sc.Bytes()
		if len(l) == 0 {
			continue
		}
		if l[0] == '{' {
			// The ID the service reads, which it answers with.
			var m wire.RPC
			_ = json.Unmarshal(l, &m)
			out = append(out, request{line: slices.Clone(l), id: m.ID, method: m.Method})
			continue
		}
		m := wire.RPC{ID: 1<<30 + len(out), Method: methods[int(l[0])%len(methods)]}
		if p := l[1:]; json.Valid(p) {
			m.Params = slices.Clone(p)
		} else if len(p) > 0 {
			m.Params, _ = json.Marshal(string(p))
		}
		line, _ := json.Marshal(m)
		out = append(out, request{line: line, id: m.ID, method: m.Method})
	}
	return out
}

// Line is a request as a client writes it, for seeds.
func Line(id int, method string, params any) string {
	m := wire.RPC{ID: id, Method: method}
	if params != nil {
		m.Params, _ = json.Marshal(params)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// Pick is a line that is not an envelope, method (one of methods) with
// params, for seeds.
func Pick(methods []string, method string, params any) string {
	i := slices.Index(methods, method)
	if i < 0 {
		panic("rpcfuzz: no method " + method)
	}
	for i == '\n' || i == '\r' || i == '{' {
		i += len(methods)
	}
	b := []byte{byte(i)}
	if params != nil {
		p, _ := json.Marshal(params)
		b = append(b, p...)
	}
	return string(b)
}

// Seed adds a conversation of lines.
func Seed(f *testing.F, lines ...string) {
	f.Add([]byte(strings.Join(lines, "\n") + "\n"))
}

// conn is the service's end of the connection: it gives the lines one at
// a time, paced by the answers, which it collects.
type conn struct {
	reqs []request
	next int
	rest []byte

	mu      sync.Mutex
	answers []wire.RPC
	bad     []string
	// Wake-ups for an answer: Read's and Run's.
	paced, changed chan struct{}
}

func (c *conn) answered() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.answers)
}

func (c *conn) Read(p []byte) (int, error) {
	if len(c.rest) == 0 {
		if c.next == len(c.reqs) {
			return 0, io.EOF
		}
		timeout := time.After(settle)
	wait:
		for c.answered() < c.next {
			select {
			case <-c.paced:
			case <-timeout:
				break wait
			}
		}
		c.rest = append(c.reqs[c.next].line, '\n')
		c.next++
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

// Write is an answer: wire writes each in one call.
func (c *conn) Write(p []byte) (int, error) {
	var r wire.RPC
	err := json.Unmarshal(p, &r)
	c.mu.Lock()
	if err != nil {
		c.bad = append(c.bad, string(p))
	} else {
		c.answers = append(c.answers, r)
	}
	c.mu.Unlock()
	for _, ch := range []chan struct{}{c.paced, c.changed} {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func (c *conn) Close() error { return nil }

// Run serves s on a connection, writes the requests of data and checks
// the answers.
func Run(t T, s Service, data []byte) {
	t.Helper()
	defer func() {
		if s.Close != nil {
			s.Close()
		}
	}()
	if len(data) > MaxInput || len(s.Methods) == 0 {
		return
	}
	reqs := requests(s.Methods, data)
	logs := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(logs)
	goroutines := runtime.NumGoroutine()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	h := func(method string, params json.RawMessage) (any, error) {
		res, err := s.Handler(method, params)
		if err != nil {
			if err.Error() == "" {
				t.Errorf("%s: an error without a message", method)
			}
			return res, err
		}
		// This connection carries no files: they are closed, the result
		// sent alone.
		if f, ok := res.(wire.Files); ok {
			for _, file := range f.Files {
				if file == nil {
					t.Errorf("%s: a nil file in the answer", method)
				} else if err := file.Close(); err != nil {
					t.Errorf("%s: a file of the answer: %v", method, err)
				}
			}
			res = f.Result
		}
		if _, merr := json.Marshal(res); merr != nil {
			t.Errorf("%s: the result cannot be sent: %v", method, merr)
		}
		return res, err
	}
	c := &conn{reqs: reqs, paced: make(chan struct{}, 1), changed: make(chan struct{}, 1)}
	served := make(chan struct{})
	go func() {
		defer close(served)
		wire.ServeConn(wire.New(c), h)
	}()

	start := time.Now()
	for c.answered() < len(reqs) {
		select {
		case <-c.changed:
		case <-time.After(settle):
			if s.Cancel != nil {
				s.Cancel()
			}
			if time.Since(start) > answerWait {
				t.Fatalf("%d of %d requests unanswered after %s: %q", len(reqs)-c.answered(), len(reqs), answerWait, unanswered(c))
			}
		}
	}
	if s.Cancel != nil {
		s.Cancel()
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	per := uint64(AllocPerLine)
	if s.AllocPerLine != 0 {
		per = s.AllocPerLine
	}
	if alloc, limit := after.TotalAlloc-before.TotalAlloc, AllocBase+per*uint64(len(reqs)); alloc > limit {
		t.Errorf("%d requests allocated %d MB, more than %d MB", len(reqs), alloc>>20, limit>>20)
	}
	select {
	case <-served:
	case <-time.After(answerWait):
		t.Fatalf("the service did not end the connection at its end")
	}
	check(t, c)
	if s.Close != nil {
		s.Close()
		s.Close = nil
	}
	for end, nap := time.Now().Add(2*time.Second), 50*time.Microsecond; runtime.NumGoroutine() > goroutines; nap = min(2*nap, 5*time.Millisecond) {
		time.Sleep(nap)
		if time.Now().After(end) {
			buf := make([]byte, 256<<10)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("%d goroutines left behind:\n%s", runtime.NumGoroutine()-goroutines, buf)
		}
	}
}

// check: every answer is a result or an error, to a request asked once.
func check(t T, c *conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.bad {
		t.Errorf("an answer that is not one: %q", b)
	}
	pending := map[int]int{}
	for _, r := range c.reqs {
		pending[r.id]++
	}
	for _, a := range c.answers {
		if !a.OK && a.Err == "" {
			t.Errorf("answer %d is neither a result nor an error: %+v", a.ID, a)
		}
		if a.FDs != 0 {
			t.Errorf("answer %d says it carries %d files", a.ID, a.FDs)
		}
		if pending[a.ID] == 0 {
			t.Errorf("an answer to %d, which was not asked or is answered already", a.ID)
		}
		pending[a.ID]--
	}
}

// unanswered: the methods of the requests without an answer yet.
func unanswered(c *conn) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	left := map[int]int{}
	for _, a := range c.answers {
		left[a.ID]++
	}
	var out []string
	for _, r := range c.reqs {
		if left[r.id] > 0 {
			left[r.id]--
			continue
		}
		out = append(out, r.method)
	}
	return out
}
