package shell

import (
	"testing"
	"time"
)

func TestProcCPU(t *testing.T) {
	stat := "4242 (phone-app (x) y) S 1 4242 4242 0 -1 4194560 1200 0 0 0 250 75 0 0 20 0 9 0 1000 123456 789 18446744073709551615"
	got, ok := procCPU(stat)
	if !ok || got != 3250*time.Millisecond {
		t.Fatalf("procCPU = %v, %v; want 3.25s", got, ok)
	}
	if _, ok := procCPU("4242 (short) S 1"); ok {
		t.Fatal("a short line parsed")
	}
}
