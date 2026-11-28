package shell

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// How a fake service answers.
const (
	svcOK      = "ok"
	svcHang    = "hang"
	svcGarbage = "garbage"
	svcWrongID = "wrong-id"
	svcHuge    = "huge"
	svcCrash   = "crash"
)

// fakeService is a system service's socket served the way a broken service
// would serve it.
type fakeService struct {
	t    *testing.T
	path string
	mu   sync.Mutex
	mode string
	// over spreads each answer over this long, in pieces (0: one write).
	over  time.Duration
	ln    net.Listener
	conns []net.Conn
	dials int
}

func (s *fakeService) setMode(m string, over time.Duration) {
	s.mu.Lock()
	s.mode, s.over = m, over
	s.mu.Unlock()
}

func (s *fakeService) start() {
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		s.t.Fatal(err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns, s.dials = append(s.conns, nc), s.dials+1
			s.mu.Unlock()
			go s.serve(nc)
		}
	}()
}

// stop is the service gone: no socket, connections closed.
func (s *fakeService) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ln.Close()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
	_ = os.Remove(s.path)
}

func (s *fakeService) serve(nc net.Conn) {
	rd := bufio.NewReader(nc)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req wire.RPC
		_ = json.Unmarshal(line, &req)
		s.mu.Lock()
		mode, over := s.mode, s.over
		s.mu.Unlock()
		var out []byte
		switch mode {
		case svcOK:
			out, _ = json.Marshal(wire.RPC{ID: req.ID, OK: true, Result: json.RawMessage(`{}`)})
		case svcHang:
			continue
		case svcGarbage:
			out = []byte("\x00not json{")
		case svcWrongID:
			out, _ = json.Marshal(wire.RPC{ID: req.ID + 1000, OK: true})
		case svcHuge:
			// A whole, valid answer, only longer than wire.MaxLine: nothing
			// but the limit fails it.
			out = []byte(`{"id":` + strconv.Itoa(req.ID) + `,"ok":true,"result":"` + strings.Repeat("z", wire.MaxLine) + `"}`)
		case svcCrash:
			nc.Close()
			return
		}
		if err := s.write(nc, append(out, '\n'), over); err != nil {
			return
		}
	}
}

func (s *fakeService) write(nc net.Conn, b []byte, over time.Duration) error {
	const pieces = 8
	if over == 0 {
		_, err := nc.Write(b)
		return err
	}
	for step := len(b)/pieces + 1; len(b) > 0; {
		time.Sleep(over / pieces)
		n := min(step, len(b))
		if _, err := nc.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// A system service that hangs, answers garbage, crashes or is gone fails
// the Shell's calls (a hung one by their deadline, a broken connection with
// its error however slowly its last answer came), never holds the event
// loop or leaks a goroutine per call, and works again once it is back.
func TestServiceFaults(t *testing.T) {
	if !alone(t) {
		return
	}
	// Only the calls that must time out (sign) have a short deadline; the
	// others (attest) have the default, which neither a healthy answer nor
	// a broken connection comes near on a slow machine, so that they pass
	// or fail by how they end. The Shell's own calls (the lock's status at
	// start) read the table, so it is set before the Shell starts.
	const deadline = 300 * time.Millisecond
	timed, untimed := svc.KeySign, svc.KeyAttest
	key := svc.Keystored + "/" + timed
	old, had := svcDeadlines[key]
	svcDeadlines[key] = deadline
	t.Cleanup(func() {
		if had {
			svcDeadlines[key] = old
		} else {
			delete(svcDeadlines, key)
		}
	})
	r := newHostileRig(t, map[string]string{})
	fake := &fakeService{t: t, path: filepath.Join(r.runDir, svc.KeystoredSock), mode: svcOK}
	fake.start()
	t.Cleanup(fake.stop)
	b := r.c.broker
	// calls makes n calls of method at once and returns their errors once
	// all are in.
	calls := func(method string, n int) []error {
		within := svcDeadline(svc.Keystored, method) + 3*time.Second
		var errs []error
		for range n {
			b.call(svc.Keystored, method, map[string]string{"app": "test"}, nil, func(err error) { errs = append(errs, err) })
		}
		if !r.until(within, func() bool { return len(errs) == n }) {
			t.Fatalf("%d of %d calls answered within %v", len(errs), n, within)
		}
		return errs
	}
	all := func(errs []error, want func(error) bool) bool {
		for _, err := range errs {
			if !want(err) {
				t.Logf("unexpected: %v", err)
				return false
			}
		}
		return true
	}
	isNil := func(err error) bool { return err == nil }
	timedOut := func(err error) bool { return errors.Is(err, wire.ErrTimeout) }
	// The connection broke: the call fails with its error (sending, or
	// waiting), not by the deadline.
	lost := func(err error) bool { return err != nil && !errors.Is(err, wire.ErrTimeout) }

	if errs := calls(untimed, 5); !all(errs, isNil) {
		t.Fatal("a healthy service failed")
	}
	gor := runtime.NumGoroutine()
	for _, tc := range []struct {
		mode   string
		over   time.Duration
		method string
		want   func(error) bool
	}{
		{svcHang, 0, timed, timedOut},
		{svcWrongID, 0, timed, timedOut},
		{svcGarbage, 0, untimed, lost},
		{svcHuge, 0, untimed, lost},
		// As on a slow machine: the Shell has read wire.MaxLine of the
		// answer only well after a timed call's deadline.
		{svcHuge, 2 * deadline, untimed, lost},
		{svcCrash, 0, untimed, lost},
	} {
		name := tc.mode
		if tc.over > 0 {
			name += " over " + tc.over.String()
		}
		fake.setMode(tc.mode, tc.over)
		r.loop = nil
		t0 := time.Now()
		errs := calls(tc.method, 50)
		took := time.Since(t0)
		if !all(errs, tc.want) {
			t.Errorf("%s: calls did not fail as expected", name)
		}
		t.Logf("%s: 50 calls failed in %v; loop latency p99 %v", name, took.Round(time.Millisecond), quantile(r.loop, .99))
		if p := quantile(r.loop, .99); p > maxLoopLatency {
			t.Errorf("%s: the event loop's p99 latency %v is over %v", name, p, maxLoopLatency)
		}
		fake.setMode(svcOK, 0)
		if errs := calls(untimed, 3); !all(errs, isNil) {
			t.Errorf("after %s the service is not used again", name)
		}
	}
	r.pump(200 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > gor+10 {
		t.Errorf("%d goroutines more than before the faults", n-gor)
	}

	// Gone (the socket too), then started again as systemd would.
	fake.stop()
	errs := calls(untimed, 5)
	if !all(errs, func(err error) bool { return err != nil }) {
		t.Fatal("calls to a service that is gone did not fail")
	}
	fake.start()
	if errs := calls(untimed, 3); !all(errs, isNil) {
		t.Fatal("the restarted service is not used")
	}

	// An app's request to a hung service gets an answer, and does not count
	// as waiting on the system any more (the not-responding check resumes).
	fake.setMode(svcHang, 0)
	a := &App{M: &Manifest{ID: "test.signer", Permissions: []string{ui.PermKeystore}}, running: true, ready: true,
		out: make(chan outbound, appOutQueue)}
	b.handle(a, ui.Msg{T: ui.MsgReq, Req: 7, Cap: ui.CapKeySign})
	if a.waiting != 1 {
		t.Fatalf("waiting %d", a.waiting)
	}
	var res ui.Msg
	r.until(deadline+2*time.Second, func() bool {
		select {
		case o := <-a.out:
			res = o.m
			return true
		default:
			return false
		}
	})
	if res.T != ui.MsgRes || res.Req != 7 || res.OK || res.Err != wire.ErrTimeout.Error() || a.waiting != 0 {
		t.Fatalf("answer %+v, still waiting %d", res, a.waiting)
	}
	fake.mu.Lock()
	dials := fake.dials
	fake.mu.Unlock()
	t.Logf("the Shell connected %d times", dials)
}
