package bsdiff

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os/exec"
	"testing"

	"github.com/kario-os/kario/ota/delta/xz"
)

// readsAt counts the reads of the old data and fails them from failAt on
// (0: never).
type readsAt struct {
	r            io.ReaderAt
	reads, bytes int
	most         int
	failAt       int64
}

func (r *readsAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	r.most = max(r.most, len(p))
	if r.failAt > 0 && off+int64(len(p)) > r.failAt {
		return 0, errors.New("the device went away")
	}
	n, err := r.r.ReadAt(p, off)
	r.bytes += n
	return n, err
}

// A patch applied with the old data read as it needs it makes what it
// makes from the old data in memory, byte for byte, whole or in pieces,
// its streams stored or compressed: one window of the old data read at a
// time, about once over, the decoded streams of one piece reused for the
// next.
func TestStreamedOldData(t *testing.T) {
	compressors := map[string]func([]byte) ([]byte, error){"stored": nil}
	if _, err := exec.LookPath("xz"); err == nil {
		compressors["xz"] = xz.Compress
	}
	r := rand.New(rand.NewSource(8))
	base := make([]byte, 2<<20)
	r.Read(base)
	news := [][]byte{nil, []byte("all new"), base, edit(r, base, 1500)}
	moved := append(append(append([]byte{}, base[3<<19:]...), base[:1<<19]...), base[1<<19:3<<19]...)
	news = append(news, edit(r, moved, 200))
	for _, new := range news {
		d := Make(base, new)
		for name, compress := range compressors {
			whole, err := d.Encode(compress)
			if err != nil {
				t.Fatal(err)
			}
			var want bytes.Buffer
			if err := Apply(base, whole, &want); err != nil || !bytes.Equal(want.Bytes(), new) {
				t.Fatalf("%s: in memory: %v", name, err)
			}
			old := &readsAt{r: bytes.NewReader(base)}
			var got bytes.Buffer
			if err := ApplyAt(old, int64(len(base)), whole, &got); err != nil || !bytes.Equal(got.Bytes(), new) {
				t.Fatalf("%s: the whole patch through a reader: %v", name, err)
			}
			if old.most > oldWindow || old.bytes > 2*len(base) {
				t.Fatalf("%s: read %d bytes of %d, up to %d at once", name, old.bytes, len(base), old.most)
			}
			const window = 256 << 10
			pieces, err := d.Pieces(window, compress)
			if err != nil {
				t.Fatal(err)
			}
			old = &readsAt{r: bytes.NewReader(base)}
			p := NewPatcher(old, int64(len(base)))
			got.Reset()
			for i, piece := range pieces {
				if err := p.Apply(piece, min(window, len(new)-i*window), &got); err != nil {
					t.Fatalf("%s: piece %d: %v", name, i, err)
				}
			}
			if !bytes.Equal(got.Bytes(), new) {
				t.Fatalf("%s: %d pieces through a reader: %d bytes, not the new data", name, len(pieces), got.Len())
			}
			if old.most > oldWindow || old.bytes > 2*len(base) {
				t.Fatalf("%s: pieces read %d bytes of %d, up to %d at once", name, old.bytes, len(base), old.most)
			}
			t.Logf("%s, %d bytes new: %d pieces, %d reads of the old data, %d bytes (%.2f of it)", name, len(new), len(pieces), old.reads, old.bytes, float64(old.bytes)/float64(len(base)))
		}
	}
}

// The old data failing to read (the source device gone) fails the patch,
// having made at most its share: never a panic, never data made of what
// was not read.
func TestOldDataUnreadable(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	base := make([]byte, 1<<20)
	r.Read(base)
	new := edit(r, base, 100)
	d := Make(base, new)
	whole, _ := d.Encode(nil)
	pieces, _ := d.Pieces(64<<10, nil)
	for _, at := range []int64{1, 4096, 300000, int64(len(base)) - 10} {
		var out bytes.Buffer
		err := ApplyAt(&readsAt{r: bytes.NewReader(base), failAt: at}, int64(len(base)), whole, &out)
		if err == nil || out.Len() >= len(new) || !bytes.Equal(out.Bytes(), new[:out.Len()]) {
			t.Fatalf("the whole patch, failing at %d: %v, %d bytes made", at, err, out.Len())
		}
		p := NewPatcher(&readsAt{r: bytes.NewReader(base), failAt: at}, int64(len(base)))
		out.Reset()
		for i, piece := range pieces {
			if err = p.Apply(piece, min(64<<10, len(new)-i*(64<<10)), &out); err != nil {
				break
			}
		}
		if err == nil || !bytes.Equal(out.Bytes(), new[:out.Len()]) {
			t.Fatalf("pieces, failing at %d: %v, %d bytes made", at, err, out.Len())
		}
	}
	if err := ApplyAt(bytes.NewReader(base[:100]), int64(len(base)), whole, &bytes.Buffer{}); err == nil {
		t.Fatal("old data shorter than its size applied")
	}
}
