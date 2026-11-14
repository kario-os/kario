package fingerprint

import (
	"context"
	"errors"
	"testing"
	"time"
)

func enrol(t *testing.T, s *Sim, finger string) Template {
	t.Helper()
	e := s.Enroll()
	var p Progress
	for i := 0; i < s.Steps; i++ {
		p = e.Add(simRead(finger))
		if p.Rejected != "" || p.Done != i+1 {
			t.Fatalf("touch %d of %s: %+v", i+1, finger, p)
		}
	}
	if p.Done != p.Steps {
		t.Fatalf("enrolment not done: %+v", p)
	}
	return e.Template()
}

func TestSimMatchesTheEnrolledFingerOnly(t *testing.T) {
	s := NewSim()
	thumb := enrol(t, s, "thumb")
	for i := 0; i < 200; i++ {
		if !s.Match(thumb, simRead("thumb")) {
			t.Fatalf("reading %d of the enrolled finger did not match", i)
		}
		if s.Match(thumb, simRead("index")) {
			t.Fatalf("reading %d of another finger matched", i)
		}
	}
}

func TestEnrolmentRejectsAnotherFinger(t *testing.T) {
	s := NewSim()
	e := s.Enroll()
	if p := e.Add(simRead("thumb")); p.Done != 1 {
		t.Fatalf("first touch: %+v", p)
	}
	if p := e.Add(simRead("index")); p.Rejected != RejectOtherFinger || p.Done != 1 {
		t.Fatalf("other finger: %+v", p)
	}
	if e.Template() != nil {
		t.Fatal("a template before the enrolment is done")
	}
}

func TestCaptureWaitsForATouch(t *testing.T) {
	s := NewSim()
	got := make(chan Sample, 1)
	go func() {
		sample, err := s.Capture(context.Background())
		if err != nil {
			t.Error(err)
		}
		got <- sample
	}()
	for {
		s.mu.Lock()
		n := len(s.waiters)
		s.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.Touch("thumb")
	select {
	case sample := <-got:
		if len(sample.Features) != simFeatures {
			t.Fatalf("features %d", len(sample.Features))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Capture did not return")
	}
}

func TestATouchJustBeforeCaptureCounts(t *testing.T) {
	s := NewSim()
	s.Touch("thumb")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Capture(ctx); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if _, err := s.Capture(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a touch counted twice: %v", err)
	}
}

func TestAStaleTouchIsDropped(t *testing.T) {
	s := NewSim()
	s.Touch("thumb")
	s.pendingAt = time.Now().Add(-simKeep)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Capture(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a stale touch counted: %v", err)
	}
}

func TestOpen(t *testing.T) {
	if s, err := Open("sim"); err != nil || s == nil || s.Name() != "simulated" {
		t.Fatalf("sim: %v %v", s, err)
	}
	if s, err := Open("none"); err != nil || s != nil {
		t.Fatalf("none: %v %v", s, err)
	}
	if _, err := Open("fpc1020"); !errors.Is(err, ErrNoDriver) {
		t.Fatalf("fpc1020: %v", err)
	}
}
