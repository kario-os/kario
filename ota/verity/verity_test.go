package verity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestLevelSizes(t *testing.T) {
	for _, c := range []struct {
		blocks uint64
		want   []uint64
	}{
		{1, nil},
		{2, []uint64{1}},
		{128, []uint64{1}},
		{129, []uint64{2, 1}},
		{16384, []uint64{128, 1}},
		{16385, []uint64{129, 2, 1}},
		// The 0.7.0 release's slot: 4022272 bytes of tree after its data.
		{124465, []uint64{973, 8, 1}},
	} {
		got := levelSizes(c.blocks)
		if len(got) != len(c.want) {
			t.Errorf("%d blocks: %v, want %v", c.blocks, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%d blocks: %v, want %v", c.blocks, got, c.want)
			}
		}
	}
	if TreeSize(124465) != 4022272 {
		t.Errorf("tree size %d", TreeSize(124465))
	}
}

func testData(blocks int) []byte {
	b := make([]byte, blocks*BlockSize)
	for i := range b {
		b[i] = byte(i/BlockSize*7 + i%13)
	}
	return b
}

// The tree as veritysetup lays it out (checked byte for byte against the
// trees of the 0.6.0 and 0.7.0 release slots): top level first, each
// level's hashes packed into zero-padded blocks, SHA-256 of salt and block.
func TestTree(t *testing.T) {
	salt := []byte("0123456789abcdef0123456789abcdef")
	data := testData(300)
	h := NewHasher(salt, 300)
	// Odd write sizes: blocks span calls.
	for p := data; len(p) > 0; {
		n := min(len(p), 5000)
		_, _ = h.Write(p[:n])
		p = p[n:]
	}
	tree, root, err := h.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(tree)) != TreeSize(300) || len(tree) != 4*BlockSize {
		t.Fatalf("tree of %d bytes", len(tree))
	}
	sum := func(b []byte) []byte {
		s := sha256.Sum256(append(append([]byte{}, salt...), b...))
		return s[:]
	}
	// Level 0 (the last 3 blocks): the data blocks' hashes.
	for i := 0; i < 300; i++ {
		if !bytes.Equal(tree[BlockSize+i*32:BlockSize+i*32+32], sum(data[i*BlockSize:(i+1)*BlockSize])) {
			t.Fatalf("level 0 hash %d", i)
		}
	}
	if !bytes.Equal(tree[BlockSize+300*32:], make([]byte, 4*BlockSize-BlockSize-300*32)) {
		t.Fatal("level 0 not zero-padded")
	}
	// Level 1 (the first block): the hashes of the three level-0 blocks.
	for i := 0; i < 3; i++ {
		if !bytes.Equal(tree[i*32:i*32+32], sum(tree[(1+i)*BlockSize:(2+i)*BlockSize])) {
			t.Fatalf("level 1 hash %d", i)
		}
	}
	if !bytes.Equal(root[:], sum(tree[:BlockSize])) {
		t.Fatal("root is not the hash of the top block")
	}
	if got := hex.EncodeToString(root[:]); got != "de436626a03fc60642aec1ac57bbb68562123fd3103fc421ddee33af4973d1d3" {
		t.Fatalf("root %s changed", got)
	}
}

func TestTreeShort(t *testing.T) {
	h := NewHasher([]byte{1}, 3)
	_, _ = h.Write(make([]byte, 2*BlockSize))
	if _, _, err := h.Tree(); err == nil {
		t.Fatal("a tree of missing blocks")
	}
	if _, err := h.Write(make([]byte, 2*BlockSize)); err == nil {
		t.Fatal("more blocks than the descriptor's")
	}
	one := NewHasher([]byte{1}, 1)
	_, _ = one.Write(make([]byte, BlockSize))
	tree, root, err := one.Tree()
	want := sha256.Sum256(append([]byte{1}, make([]byte, BlockSize)...))
	if err != nil || tree != nil || root != want {
		t.Fatalf("one block: %d bytes of tree, root %x", len(tree), root)
	}
}

func TestDescriptor(t *testing.T) {
	text := "data_blocks=124465\nhash_offset=509808640\nsalt=cc8f4ca8ffe34b6ebd9676d0883c846f0d710e4af81a51f963b5f241ab26bd8d\nroot_hash=c02eb5fb3706f7eb78f84a8da06a82fbaf6d6b4eb8bac681ea546fd2a0f93579\n"
	d, err := ParseDescriptor([]byte(text))
	if err != nil || d.DataBlocks != 124465 || len(d.Salt) != 32 || d.RootHash[0] != 0xc0 {
		t.Fatalf("%+v %v", d, err)
	}
	if string(d.Marshal()) != text {
		t.Fatalf("marshalled differently:\n%s", d.Marshal())
	}
	o := d
	o.Salt = append([]byte{}, d.Salt...)
	if !d.Equal(o) {
		t.Fatal("not equal to itself")
	}
	o.Salt[0] ^= 1
	if d.Equal(o) {
		t.Fatal("equal with another salt")
	}
	for _, bad := range []string{
		"",
		text + "root_hash=" + string(bytes.Repeat([]byte("1"), 64)) + "\n",
		"data_blocks=2\nhash_offset=4096\nsalt=00\nroot_hash=" + string(bytes.Repeat([]byte("0"), 64)) + "\n",
		"data_blocks=1\nhash_offset=4096\nsalt=0\nroot_hash=" + string(bytes.Repeat([]byte("0"), 64)) + "\n",
		"data_blocks=1\nhash_offset=4096\nsalt=00\nroot_hash=" + string(bytes.Repeat([]byte("0"), 63)) + "\n",
	} {
		if _, err := ParseDescriptor([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
