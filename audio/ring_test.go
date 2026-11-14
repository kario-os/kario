package audio

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRingWrapAndEOF(t *testing.T) {
	f, err := NewRingFile("test", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prod, err := MapRing(f)
	if err != nil {
		t.Fatal(err)
	}
	cons, err := MapRing(f)
	if err != nil {
		t.Fatal(err)
	}
	if n := prod.Write([]int16{1, 2, 3, 4, 5, 6}); n != 6 {
		t.Fatalf("wrote %d", n)
	}
	got := make([]int16, 4)
	if n := cons.Read(got); n != 4 || got[0] != 1 || got[3] != 4 {
		t.Fatalf("read %d %v", n, got)
	}
	if n := prod.Write([]int16{7, 8, 9, 10, 11, 12, 13}); n != 6 {
		t.Fatalf("free space: wrote %d, want 6", n)
	}
	all := make([]int16, 16)
	n := cons.Read(all)
	want := []int16{5, 6, 7, 8, 9, 10, 11, 12}
	if n != len(want) {
		t.Fatalf("read %d after wrap", n)
	}
	for i, v := range want {
		if all[i] != v {
			t.Fatalf("after wrap %v, want %v", all[:n], want)
		}
	}
	src := NewRingSource(cons)
	if !src.Fill(make([]float64, 4)) {
		t.Fatal("ended before the producer finished")
	}
	prod.Finish()
	if src.Fill(make([]float64, 4)) {
		t.Fatal("did not end after finish with nothing left")
	}
}

func TestRingRejectsUnsealed(t *testing.T) {
	fd, err := unix.MemfdCreate("plain", unix.MFD_CLOEXEC)
	if err != nil {
		t.Skip(err)
	}
	f := os.NewFile(uintptr(fd), "plain")
	defer f.Close()
	_ = unix.Ftruncate(fd, ringHeader+16)
	if _, err := MapRing(f); err == nil {
		t.Fatal("unsealed memfd accepted: a peer could shrink it and crash us with SIGBUS")
	}
}

func TestRingBogusPeerPositions(t *testing.T) {
	f, _ := NewRingFile("test", 8)
	defer f.Close()
	r, _ := MapRing(f)
	*r.wpos() = 1 << 40 // a lying producer
	buf := make([]int16, 32)
	if n := r.Read(buf); n > r.Cap() {
		t.Fatalf("read %d frames from a ring of %d", n, r.Cap())
	}
	*r.rpos() = 1 << 50 // a lying consumer
	if n := r.Write(buf); n > r.Cap() {
		t.Fatalf("wrote %d frames into a ring of %d", n, r.Cap())
	}
}
