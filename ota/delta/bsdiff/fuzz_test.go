package bsdiff

import (
	"bytes"
	"testing"
)

// FuzzApply is a patch of an update payload: it makes its data from old
// or fails, whatever it holds.
func FuzzApply(f *testing.F) {
	old := []byte("kario update payload: slot verity blocks, operation by operation")
	for _, new := range [][]byte{[]byte("kario update payload: slot verity blocks, op by op, with more data"), nil, old} {
		p, _ := Make(old, new).Encode(nil)
		f.Add(old, p)
	}
	f.Fuzz(func(t *testing.T, old, patch []byte) {
		if n, err := Size(patch); err != nil || n > 1<<20 {
			return
		}
		var out bytes.Buffer
		if err := Apply(old, patch, &out); err == nil {
			if n, _ := Size(patch); int64(out.Len()) != n {
				t.Fatalf("%d bytes made, the patch says %d", out.Len(), n)
			}
		}
	})
}

// FuzzApplyPiece is one piece of a streamed patch: it makes its share of
// the data from old or fails, whatever it holds.
func FuzzApplyPiece(f *testing.F) {
	old := []byte("kario update payload: slot verity blocks, operation by operation")
	ps, _ := Make(old, []byte("kario update payload: slot verity blocks, op by op, with more data")).Pieces(16, nil)
	for _, p := range ps {
		f.Add(old, p, 16)
	}
	f.Fuzz(func(t *testing.T, old, piece []byte, want int) {
		if want < 0 || want > 1<<16 {
			return
		}
		var out bytes.Buffer
		if err := ApplyPiece(old, piece, want, &out); err == nil && out.Len() != want || out.Len() > want {
			t.Fatalf("%d bytes made, the piece's share is %d", out.Len(), want)
		}
	})
}

// FuzzMake: a patch made from old to new makes new from old.
func FuzzMake(f *testing.F) {
	f.Add([]byte("abcabcabcabd mississippi"), []byte("abcabXabcabd misSissippi banana"))
	f.Fuzz(func(t *testing.T, old, new []byte) {
		if len(old) > 4096 || len(new) > 4096 {
			return
		}
		p, err := Make(old, new).Encode(nil)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := Apply(old, p, &out); err != nil || !bytes.Equal(out.Bytes(), new) {
			t.Fatalf("%q to %q: %v, made %q", old, new, err, out.Bytes())
		}
	})
}
