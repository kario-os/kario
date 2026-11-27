package push

import (
	"testing"
	"time"
)

// A network whose NAT drops connections idle for 5 minutes or more.
func TestHeartbeatFindsTheNATTimeout(t *testing.T) {
	const nat = 5 * time.Minute
	h := NewHeartbeat()
	for i := 0; i < 40; i++ {
		if h.Cur < nat {
			h.Survived()
		} else {
			h.Dropped()
		}
	}
	if h.Cur >= nat || h.Cur < nat*3/4 {
		t.Fatalf("settled on %s for a %s NAT timeout", h.Cur, nat)
	}
	if h.Good != h.Cur {
		t.Fatalf("current %s is not the last good %s", h.Cur, h.Good)
	}
}

func TestHeartbeatBounds(t *testing.T) {
	h := NewHeartbeat()
	for i := 0; i < 20; i++ {
		h.Survived()
	}
	if h.Cur > HeartbeatMax {
		t.Fatalf("grew past the maximum: %s", h.Cur)
	}
	h = NewHeartbeat()
	for i := 0; i < 10; i++ {
		h.Dropped()
	}
	if h.Cur != HeartbeatMin {
		t.Fatalf("shrank to %s, want the minimum %s", h.Cur, HeartbeatMin)
	}
}
