package shell

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	t     *testing.T
	path  string
	mu    sync.Mutex
	mode  string
	ln    net.Listener
	conns []net.Conn
	dials int
}

func (s *fakeService) setMode(m string) {
	s.mu.Lock()
	s.mode = m
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
		mode := s.mode
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
			out = []byte(`{"id":1,"result":"` + strings.Repeat("z", wire.MaxLine+10))
		case svcCrash:
			nc.Close()
			return
		}
		if _, err := nc.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// A system service that hangs, answers garbage, crashes or is gone fails
// the Shell's calls within their deadline, never holds the event loop or
// leaks a goroutine per call, and works again once it is back.
func TestServiceFaults(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newHostileRig(t, map[string]string{})
	fake := &fakeService{t: t, path: filepath.Join(r.runDir, svc.KeystoredSock), mode: svcOK}
	fake.start()
	t.Cleanup(fake.stop)
	const deadline = 300 * time.Millisecond
	key := svc.Keystored + "/" + svc.KeySign
	old, had := svcDeadlines[key]
	svcDeadlines[key] = deadline
	t.Cleanup(func() {
		if had {
			svcDeadlines[key] = old
		} else {
			delete(svcDeadlines, key)
		}
	})
	b := r.c.broker
	// calls makes n calls at once and returns their errors once all are in.
	calls := func(n int, within time.Duration) []error {
		var errs []error
		for range n {
			b.call(svc.Keystored, svc.KeySign, map[string]string{"app": "test"}, nil, func(err error) { errs = append(errs, err) })
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
	// The connection broke: the call fails at once (sending, or waiting).
	lost := func(err error) bool { return err != nil && !errors.Is(err, wire.ErrTimeout) }

	if errs := calls(5, 2*time.Second); !all(errs, isNil) {
		t.Fatal("a healthy service failed")
	}
	gor := runtime.NumGoroutine()
	for _, tc := range []struct {
		mode string
		want func(error) bool
	}{
		{svcHang, func(err error) bool { return errors.Is(err, wire.ErrTimeout) }},
		{svcWrongID, func(err error) bool { return errors.Is(err, wire.ErrTimeout) }},
		{svcGarbage, lost},
		{svcHuge, lost},
		{svcCrash, lost},
	} {
		fake.setMode(tc.mode)
		r.loop = nil
		t0 := time.Now()
		errs := calls(50, deadline+3*time.Second)
		took := time.Since(t0)
		if !all(errs, tc.want) {
			t.Errorf("%s: calls did not fail as expected", tc.mode)
		}
		if tc.mode != svcHang && tc.mode != svcWrongID && took > deadline {
			t.Errorf("%s: failing took %v, as long as a deadline", tc.mode, took)
		}
		t.Logf("%s: 50 calls failed in %v; loop latency p99 %v", tc.mode, took.Round(time.Millisecond), quantile(r.loop, .99))
		if p := quantile(r.loop, .99); p > maxLoopLatency {
			t.Errorf("%s: the event loop's p99 latency %v is over %v", tc.mode, p, maxLoopLatency)
		}
		fake.setMode(svcOK)
		if errs := calls(3, 3*time.Second); !all(errs, isNil) {
			t.Errorf("after %s the service is not used again", tc.mode)
		}
	}
	r.pump(200 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > gor+10 {
		t.Errorf("%d goroutines more than before the faults", n-gor)
	}

	// Gone (the socket too), then started again as systemd would.
	fake.stop()
	errs := calls(5, 5*time.Second)
	if !all(errs, func(err error) bool { return err != nil }) {
		t.Fatal("calls to a service that is gone did not fail")
	}
	fake.start()
	if errs := calls(3, 3*time.Second); !all(errs, isNil) {
		t.Fatal("the restarted service is not used")
	}

	// An app's request to a hung service gets an answer, and does not count
	// as waiting on the system any more (the not-responding check resumes).
	fake.setMode(svcHang)
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
