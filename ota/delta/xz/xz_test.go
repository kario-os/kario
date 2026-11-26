package xz

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// sample is varied data: text, runs, near-repeats and noise, so that
// literals, matches, repeated distances and stored chunks all occur.
func sample(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	words := []string{"slot", "verity", "payload", "kario", "update ", "block\n", "\x00\x00\x00\x00", "\xff\xfe"}
	var b bytes.Buffer
	for b.Len() < n {
		switch r.Intn(5) {
		case 0:
			b.WriteString(words[r.Intn(len(words))])
		case 1:
			b.Write(bytes.Repeat([]byte{byte(r.Intn(256))}, r.Intn(300)))
		case 2:
			if b.Len() > 100 {
				at := r.Intn(b.Len() - 50)
				seg := append([]byte{}, b.Bytes()[at:at+r.Intn(50)]...)
				if len(seg) > 0 && r.Intn(2) == 0 {
					seg[r.Intn(len(seg))]++
				}
				b.Write(seg)
			}
		case 3:
			noise := make([]byte, r.Intn(200))
			r.Read(noise)
			b.Write(noise)
		default:
			b.WriteByte(byte('a' + r.Intn(26)))
		}
	}
	return b.Bytes()[:n]
}

func xzRun(t *testing.T, data []byte, args ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("no xz program")
	}
	cmd := exec.Command("xz", append([]string{"--stdout", "--format=xz", "--threads=1"}, args...)...)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("xz %v: %v", args, err)
	}
	return out
}

// Decoded as the xz program wrote it, across presets, checks, LZMA
// properties, several blocks and stored chunks.
func TestDecodeXZ(t *testing.T) {
	random := make([]byte, 300000)
	rand.New(rand.NewSource(7)).Read(random)
	self, _ := os.ReadFile(os.Args[0])
	if len(self) > 3<<20 {
		self = self[:3<<20]
	}
	inputs := map[string][]byte{
		"empty":  {},
		"one":    {42},
		"sample": sample(200000, 1),
		"big":    sample(5<<20, 2), // several 2 MiB LZMA2 chunks
		"random": random,           // stored chunks
		"zeros":  make([]byte, 1<<20),
		"binary": self,
	}
	options := [][]string{
		{"-6"},
		{"-0"},
		{"-9e"},
		{"--check=none"},
		{"--check=crc32"},
		{"--check=sha256"},
		{"--block-size=65536"},
		{"--lzma2=preset=6,lc=0,lp=2,pb=0"},
		{"--lzma2=preset=6,lc=4,lp=0,pb=4"},
		{"--lzma2=preset=6,lc=1,lp=3,pb=1"},
	}
	for name, data := range inputs {
		for _, opt := range options {
			if len(data) > 200000 && opt[0] != "-6" && opt[0] != "--block-size=65536" {
				continue
			}
			comp := xzRun(t, data, opt...)
			got, err := Decode(comp, len(data))
			if err != nil || !bytes.Equal(got, data) {
				t.Errorf("%s %v: %v (%d of %d bytes)", name, opt, err, len(got), len(data))
			}
		}
	}
}

func TestCompress(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("no xz program")
	}
	data := sample(100000, 3)
	comp, err := Compress(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(comp, len(data))
	if err != nil || !bytes.Equal(got, data) || len(comp) >= len(data) {
		t.Fatalf("%v, %d bytes compressed", err, len(comp))
	}
}

// testdata/sample.xz (sample(65536, 5), xz -9e): the decoder is tested
// without the xz program too.
func TestFixture(t *testing.T) {
	comp, err := os.ReadFile("testdata/sample.xz")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(comp, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if s := sha256.Sum256(got); !bytes.Equal(got, sample(65536, 5)) {
		t.Fatalf("decoded to %s", hex.EncodeToString(s[:]))
	}
}

// Damaged input is an error, never a crash, and never the data.
func TestCorrupt(t *testing.T) {
	comp, err := os.ReadFile("testdata/sample.xz")
	if err != nil {
		t.Fatal(err)
	}
	data := sample(65536, 5)
	r := rand.New(rand.NewSource(9))
	for i := 0; i < 1000; i++ {
		bad := append([]byte{}, comp...)
		switch i % 3 {
		case 0:
			bad[r.Intn(len(bad))] ^= byte(1 + r.Intn(255))
		case 1:
			bad = bad[:r.Intn(len(bad))]
		default:
			at := r.Intn(len(bad))
			bad = append(bad[:at], bad[at+1:]...)
		}
		if got, err := Decode(bad, len(data)); err == nil && !bytes.Equal(got, data) {
			t.Fatalf("damaged stream %d decoded to other data", i)
		}
	}
	if _, err := Decode(comp, len(data)-1); err == nil {
		t.Fatal("decoded to fewer bytes than the stream holds")
	}
	if _, err := Decode(comp, len(data)+1); err == nil {
		t.Fatal("decoded to more bytes than the stream holds")
	}
}

func BenchmarkDecode(b *testing.B) {
	data, _ := os.ReadFile(os.Args[0])
	cmd := exec.Command("xz", "--stdout", "--format=xz", "--threads=1", "-6")
	cmd.Stdin = bytes.NewReader(data)
	comp, err := cmd.Output()
	if err != nil {
		b.Skip("no xz program")
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Decode(comp, len(data)); err != nil {
			b.Fatal(err)
		}
	}
}
