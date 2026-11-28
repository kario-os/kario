package main

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/kario-os/kario/ota/delta/squashfs"
)

// The superblock and tables are read in small pieces, one after another:
// the aligned reader reads each device block of them once.
func TestAlignedReaderKeepsItsBlock(t *testing.T) {
	img := make([]byte, 5*squashfs.Align+100)
	rand.New(rand.NewSource(1)).Read(img)
	a := &alignedReader{f: bytes.NewReader(img), buf: make([]byte, squashfs.Align), at: -1}
	got := make([]byte, 0, len(img))
	for off := 0; off < len(img); {
		n := min(len(img)-off, 2+off%7)
		p := make([]byte, n)
		if k, err := a.ReadAt(p, int64(off)); err != nil || k != n {
			t.Fatalf("at %d: %d %v", off, k, err)
		}
		got, off = append(got, p...), off+n
	}
	if !bytes.Equal(got, img) || a.reads != 6 {
		t.Fatalf("read the image in %d device reads, want 6", a.reads)
	}
	if _, err := a.ReadAt(make([]byte, 200), int64(len(img)-100)); err == nil {
		t.Fatal("read past the end")
	}
}
