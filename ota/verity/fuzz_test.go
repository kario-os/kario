package verity

import "testing"

// FuzzDescriptor is a slot's verity descriptor (signed, but read from the
// slot's partition): what parses is what it marshals to.
func FuzzDescriptor(f *testing.F) {
	f.Add(Descriptor{DataBlocks: 4, HashOffset: 4 * BlockSize, Salt: []byte{1, 2}, RootHash: [32]byte{3}}.Marshal())
	f.Add([]byte("data_blocks=1\nhash_offset=4096\nsalt=ab\nroot_hash=" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\nextra=1\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := ParseDescriptor(b)
		if err != nil {
			return
		}
		e, err := ParseDescriptor(d.Marshal())
		if err != nil || !e.Equal(d) {
			t.Fatalf("%+v marshals to %q: %+v, %v", d, d.Marshal(), e, err)
		}
		if TreeSize(d.DataBlocks) > d.DataBlocks*BlockSize {
			t.Fatalf("a tree of %d bytes for %d blocks", TreeSize(d.DataBlocks), d.DataBlocks)
		}
	})
}
