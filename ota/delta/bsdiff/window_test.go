package bsdiff

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"testing"

	"github.com/kario-os/kario/ota/delta/xz"
)

// applyPieces makes the new data piece by piece, as an install streaming
// the patch in does.
func applyPieces(old []byte, pieces [][]byte, window, size int) ([]byte, error) {
	var out bytes.Buffer
	for i, p := range pieces {
		want := min(window, size-i*window)
		if err := ApplyPiece(old, p, want, &out); err != nil {
			return out.Bytes(), err
		}
	}
	return out.Bytes(), nil
}

// The patch applied piece by piece makes what the whole patch makes, for
// any size of piece, and each piece makes exactly its share.
func TestPiecesEqualWhole(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	base := make([]byte, 30000)
	r.Read(base)
	cases := [][2][]byte{{nil, []byte("new")}, {[]byte("old"), []byte("old and more")}, {base, base}}
	for i := 0; i < 30; i++ {
		cases = append(cases, [2][]byte{base, edit(r, base, 1+r.Intn(40))})
	}
	for _, c := range cases {
		d := Make(c[0], c[1])
		whole, _ := d.Encode(nil)
		var want bytes.Buffer
		if err := Apply(c[0], whole, &want); err != nil {
			t.Fatal(err)
		}
		for _, window := range []int{1, 7, 4096, 10000, 1 << 20} {
			pieces, err := d.Pieces(window, nil)
			if err != nil {
				t.Fatal(err)
			}
			if n := (len(c[1]) + window - 1) / window; len(pieces) != n {
				t.Fatalf("%d pieces of %d bytes for %d bytes, want %d", len(pieces), window, len(c[1]), n)
			}
			got, err := applyPieces(c[0], pieces, window, len(c[1]))
			if err != nil || !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("pieces of %d: %v (%d of %d bytes)", window, err, len(got), want.Len())
			}
		}
	}
	if _, err := Make(base, base).Pieces(0, nil); err == nil {
		t.Fatal("pieces of no bytes")
	}
}

// Compressed pieces of a program cost little more than the whole patch:
// each piece's streams lose the context of the pieces before (needs the xz
// program).
func TestPiecesSize(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("no xz program")
	}
	old, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	old = old[:min(len(old), 8<<20)]
	r := rand.New(rand.NewSource(3))
	new := append(append(append([]byte{}, old[:4096]...), []byte("a longer string in the new build")...), old[4096:]...)
	new = edit(r, new, 800)
	d := Make(old, new)
	whole, err := d.Encode(xz.Compress)
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range []int{1 << 20, 4 << 20} {
		pieces, err := d.Pieces(window, xz.Compress)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, p := range pieces {
			n += len(p)
		}
		got, err := applyPieces(old, pieces, window, len(new))
		if err != nil || !bytes.Equal(got, new) {
			t.Fatalf("pieces of %d: %v", window, err)
		}
		t.Logf("%d bytes of new data: whole patch %d bytes, %d pieces of %d KiB %d bytes (%+.1f%%)", len(new), len(whole), len(pieces), window>>10, n, float64(n-len(whole))*100/float64(len(whole)))
		if n > len(whole)*5/4 {
			t.Fatalf("pieces of %d bytes cost %d, the whole patch %d", window, n, len(whole))
		}
	}
}

// A damaged piece fails or makes data the payload's hashes catch: it never
// reads outside old, makes more than its share or crashes; one malformed
// before its steps writes nothing.
func TestDamagedPiece(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	old := make([]byte, 5000)
	r.Read(old)
	new := edit(r, old, 30)
	pieces, _ := Make(old, new).Pieces(2048, nil)
	for i := 0; i < 5000; i++ {
		k := r.Intn(len(pieces))
		bad := append([]byte{}, pieces[k]...)
		if i%2 == 0 {
			bad[r.Intn(len(bad))] ^= byte(1 + r.Intn(255))
		} else {
			bad = bad[:r.Intn(len(bad))]
		}
		want := min(2048, len(new)-k*2048)
		var out bytes.Buffer
		if err := ApplyPiece(old, bad, want, &out); err == nil && out.Len() != want || out.Len() > want {
			t.Fatalf("a damaged piece made %d bytes of %d", out.Len(), want)
		}
	}
	var out bytes.Buffer
	if err := ApplyPiece(old, pieces[0][:pieceHeader-1], 2048, &out); err == nil || out.Len() != 0 {
		t.Fatal("a cut header applied")
	}
	if err := ApplyPiece(old, pieces[0], 2047, &out); err == nil {
		t.Fatal("a piece applied for another share of the data")
	}
}
