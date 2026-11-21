package delta

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ota/verity"
)

func fuzzManifest() *Manifest {
	d := verity.Descriptor{DataBlocks: 4, HashOffset: 4 * BlockSize, Salt: []byte{1}}
	h := strings.Repeat("ab", 32)
	return &Manifest{Format: Format, BlockSize: BlockSize, Source: Image{Size: 4 * BlockSize, SHA256: h, RootHash: h}, Target: Image{Size: 4 * BlockSize, SHA256: h}, Verity: VerityOf(d),
		Ops: []Op{
			{Type: Zero, Dst: []Extent{{0, 1}}, DstSHA256: h},
			{Type: SourceCopy, Src: []Extent{{2, 2}}, Dst: []Extent{{1, 2}}, SrcSHA256: h, DstSHA256: h},
			{Type: Replace, Dst: []Extent{{3, 1}}, Compression: CompressXZ, DataLength: 100, DataSHA256: h, DstSHA256: h},
		}}
}

// FuzzManifest is an incremental update payload's header and manifest:
// what is accepted encodes to a payload accepted the same.
func FuzzManifest(f *testing.F) {
	var b bytes.Buffer
	WriteHeader(&b, fuzzManifest())
	f.Add(b.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ReadManifest(bytes.NewReader(data))
		if err != nil {
			return
		}
		var again bytes.Buffer
		if err := WriteHeader(&again, m); err != nil {
			t.Fatal(err)
		}
		n, err := ReadManifest(&again)
		if err != nil || !reflect.DeepEqual(m, n) {
			t.Fatalf("round trip: %+v, then %+v (%v)", m, n, err)
		}
	})
}

// FuzzSlot is a slot or disk image phone-ota-delta reads.
func FuzzSlot(f *testing.F) {
	head := make([]byte, 2048)
	copy(head[512:], "EFI PART")
	le := func(b []byte, v uint64, n int) {
		for i := range n {
			b[i] = byte(v >> (8 * i))
		}
	}
	le(head[512+72:], 2, 8)
	le(head[512+80:], 4, 4)
	le(head[512+84:], 128, 4)
	le(head[1024+32:], 3, 8)
	le(head[1024+40:], 3, 8)
	for i, c := range "rootfs_a" {
		head[1024+56+2*i] = byte(c)
	}
	f.Add(head)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<17 {
			return
		}
		p := filepath.Join(t.TempDir(), "slot.img")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			LoadSlot(p, "", "a")
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("reading the image did not end")
		}
	})
}

// A partition table whose entries' LBA wraps around when counted in bytes
// is not a partition table.
func TestGPTOffsetsDoNotWrap(t *testing.T) {
	head := make([]byte, 2048)
	copy(head[512:], "EFI PART")
	for i, b := range []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f, 0x00} {
		head[512+72+i] = b
	}
	head[512+80], head[512+84] = 4, 128
	if parts, ok := gptPartitions(head); ok {
		t.Fatalf("partitions %v", parts)
	}
}
