package bsdiff

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"testing"

	"github.com/kario-os/kario/ota/delta/xz"
)

func naiveSA(b []byte) []int32 {
	sa := make([]int32, len(b))
	for i := range sa {
		sa[i] = int32(i)
	}
	sort.Slice(sa, func(i, j int) bool { return bytes.Compare(b[sa[i]:], b[sa[j]:]) < 0 })
	return sa
}

func TestSuffixArray(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	cases := [][]byte{nil, []byte("a"), []byte("aaaaaaaaaaaaaaaa"), []byte("abababababababab"), []byte("abcabcabcabcabd"),
		[]byte("mississippi"), []byte("banana"), {0, 0, 0, 255, 255, 0, 0}}
	for i := 0; i < 2000; i++ {
		b := make([]byte, r.Intn(400))
		alpha := []int{1, 2, 3, 4, 256}[r.Intn(5)]
		for j := range b {
			b[j] = byte(r.Intn(alpha))
		}
		cases = append(cases, b)
	}
	for _, b := range cases {
		got, want := suffixArray(b), naiveSA(b)
		if len(got) != len(want) {
			t.Fatalf("%q: %d suffixes", b, len(got))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%q: %v, want %v", b, got, want)
			}
		}
	}
}

func roundTrip(t *testing.T, old, new []byte, compress func([]byte) ([]byte, error)) []byte {
	t.Helper()
	patch, err := Make(old, new).Encode(compress)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := Apply(old, patch, &got); err != nil || !bytes.Equal(got.Bytes(), new) {
		t.Fatalf("old %d new %d bytes: %v (%d bytes back)", len(old), len(new), err, got.Len())
	}
	if n, _ := Size(patch); n != int64(len(new)) {
		t.Fatalf("size %d", n)
	}
	return patch
}

// edit changes b the way a new build changes a program: bytes changed,
// stretches inserted, removed and moved.
func edit(r *rand.Rand, b []byte, n int) []byte {
	b = append([]byte{}, b...)
	for i := 0; i < n && len(b) > 100; i++ {
		at := r.Intn(len(b) - 50)
		switch r.Intn(4) {
		case 0:
			b[at]++
		case 1:
			ins := make([]byte, r.Intn(64))
			r.Read(ins)
			b = append(b[:at], append(ins, b[at:]...)...)
		case 2:
			b = append(b[:at], b[at+r.Intn(40):]...)
		default:
			seg := append([]byte{}, b[at:at+r.Intn(50)]...)
			to := r.Intn(len(b))
			b = append(b[:to], append(seg, b[to:]...)...)
		}
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	random := func(n int) []byte {
		b := make([]byte, n)
		r.Read(b)
		return b
	}
	base := random(20000)
	cases := [][2][]byte{
		{nil, nil},
		{nil, []byte("new")},
		{[]byte("old"), nil},
		{base, base},
		{base, random(20000)},
		{bytes.Repeat([]byte{0}, 5000), bytes.Repeat([]byte{0}, 7000)},
		{[]byte("the quick brown fox"), []byte("the quick red fox jumps")},
	}
	for i := 0; i < 50; i++ {
		cases = append(cases, [2][]byte{base, edit(r, base, 1+r.Intn(30))})
	}
	for _, c := range cases {
		roundTrip(t, c[0], c[1], nil)
	}
}

// A program with code moved and changed patches to a small part of its
// size once compressed (needs the xz program).
func TestProgramPatch(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("no xz program")
	}
	old, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(old) > 2<<20 {
		old = old[:2<<20]
	}
	r := rand.New(rand.NewSource(3))
	// A shift near the start (all later addresses move) and scattered edits.
	new := append(append(append([]byte{}, old[:4096]...), []byte("a longer string in the new build")...), old[4096:]...)
	new = edit(r, new, 200)
	patch := roundTrip(t, old, new, xz.Compress)
	whole, err := xz.Compress(new)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("patch %d bytes, the new data compressed %d", len(patch), len(whole))
	if len(patch)*10 > len(whole) {
		t.Fatalf("patch %d bytes for %d compressed", len(patch), len(whole))
	}
}

// A damaged patch fails, or makes other data that the payload's hashes
// catch: it never reads outside old or crashes.
func TestDamagedPatch(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	old := make([]byte, 3000)
	r.Read(old)
	new := edit(r, old, 20)
	patch, _ := Make(old, new).Encode(nil)
	for i := 0; i < 3000; i++ {
		bad := append([]byte{}, patch...)
		if i%2 == 0 {
			bad[r.Intn(len(bad))] ^= byte(1 + r.Intn(255))
		} else {
			bad = bad[:r.Intn(len(bad))]
		}
		var out bytes.Buffer
		_ = Apply(old, bad, &out)
	}
	if err := Apply(old, []byte("KBSDIFF1"), &bytes.Buffer{}); err == nil {
		t.Fatal("a header alone applied")
	}
}

func BenchmarkMake(b *testing.B) {
	old, _ := os.ReadFile(os.Args[0])
	r := rand.New(rand.NewSource(5))
	new := edit(r, old, 1000)
	b.SetBytes(int64(len(new)))
	for i := 0; i < b.N; i++ {
		Make(old, new)
	}
}
