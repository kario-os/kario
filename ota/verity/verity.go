// Package verity is a system slot's dm-verity side (25-system-update.md):
// the signed descriptor phone-verity.bbclass writes and phone-initramfs opens
// the slot with, and the hash tree veritysetup appends to the slot image
// (format 1, SHA-256, 4 KiB data and hash blocks, no superblock), which an
// incremental update rebuilds on the phone.
package verity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"regexp"
	"strconv"
)

// BlockSize is both the data and the hash block size of the slots.
const BlockSize = 4096

// hashesPerBlock: SHA-256 digests in a hash block.
const hashesPerBlock = BlockSize / sha256.Size

// Descriptor is a slot's verity descriptor: what veritysetup needs besides
// the device. The text form is what phone-verity.bbclass signs.
type Descriptor struct {
	DataBlocks uint64
	HashOffset uint64
	Salt       []byte
	RootHash   [sha256.Size]byte
}

// The same shapes phone-initramfs' slot-init accepts.
var descLines = []struct {
	key string
	re  *regexp.Regexp
}{
	{"data_blocks", regexp.MustCompile(`(?m)^data_blocks=([0-9]{1,12})$`)},
	{"hash_offset", regexp.MustCompile(`(?m)^hash_offset=([0-9]{1,15})$`)},
	{"salt", regexp.MustCompile(`(?m)^salt=([0-9a-f]{1,128})$`)},
	{"root_hash", regexp.MustCompile(`(?m)^root_hash=([0-9a-f]{64})$`)},
}

// ParseDescriptor reads a descriptor's text.
func ParseDescriptor(b []byte) (Descriptor, error) {
	var d Descriptor
	v := map[string]string{}
	for _, l := range descLines {
		m := l.re.FindAllSubmatch(b, -1)
		if len(m) != 1 {
			return d, fmt.Errorf("verity descriptor: no single %s line", l.key)
		}
		v[l.key] = string(m[0][1])
	}
	d.DataBlocks, _ = strconv.ParseUint(v["data_blocks"], 10, 64)
	d.HashOffset, _ = strconv.ParseUint(v["hash_offset"], 10, 64)
	salt, err := hex.DecodeString(v["salt"])
	if err != nil {
		return d, errors.New("verity descriptor: the salt is not whole bytes")
	}
	d.Salt = salt
	root, _ := hex.DecodeString(v["root_hash"])
	copy(d.RootHash[:], root)
	return d, d.Check()
}

// Check: the layout phone-verity.bbclass makes, the hash tree right after
// the data.
func (d Descriptor) Check() error {
	switch {
	case d.DataBlocks == 0:
		return errors.New("verity descriptor: no data blocks")
	case d.HashOffset != d.DataBlocks*BlockSize:
		return fmt.Errorf("verity descriptor: hash offset %d is not right after the %d data blocks", d.HashOffset, d.DataBlocks)
	case len(d.Salt) == 0:
		return errors.New("verity descriptor: no salt")
	}
	return nil
}

// Marshal is the descriptor's text, as phone-verity.bbclass writes it.
func (d Descriptor) Marshal() []byte {
	return fmt.Appendf(nil, "data_blocks=%d\nhash_offset=%d\nsalt=%x\nroot_hash=%x\n", d.DataBlocks, d.HashOffset, d.Salt, d.RootHash)
}

// Equal: the same slot layout and root hash.
func (d Descriptor) Equal(o Descriptor) bool {
	return d.DataBlocks == o.DataBlocks && d.HashOffset == o.HashOffset && bytes.Equal(d.Salt, o.Salt) && d.RootHash == o.RootHash
}

// levelSizes is the blocks of each hash level, the one over the data first
// (veritysetup's hash_levels): as many levels as it takes to come down to
// one block. A single data block has no tree.
func levelSizes(dataBlocks uint64) []uint64 {
	var sizes []uint64
	for span := uint64(hashesPerBlock); dataBlocks > span/hashesPerBlock; span *= hashesPerBlock {
		sizes = append(sizes, (dataBlocks+span-1)/span)
	}
	return sizes
}

// TreeSize is the bytes of the hash tree after the data.
func TreeSize(dataBlocks uint64) uint64 {
	var n uint64
	for _, s := range levelSizes(dataBlocks) {
		n += s
	}
	return n * BlockSize
}

// Hasher builds a slot's hash tree from its data blocks, written in order.
type Hasher struct {
	salt       []byte
	dataBlocks uint64
	n          uint64
	partial    []byte
	level0     []byte
	h          hash.Hash
}

// NewHasher starts the tree of dataBlocks blocks under salt.
func NewHasher(salt []byte, dataBlocks uint64) *Hasher {
	return &Hasher{salt: salt, dataBlocks: dataBlocks, h: sha256.New(), level0: make([]byte, 0, dataBlocks*sha256.Size)}
}

func (h *Hasher) sum(block []byte, dst []byte) []byte {
	h.h.Reset()
	h.h.Write(h.salt)
	h.h.Write(block)
	return h.h.Sum(dst)
}

// Write takes the next data bytes.
func (h *Hasher) Write(p []byte) (int, error) {
	n := len(p)
	if len(h.partial) > 0 {
		k := min(BlockSize-len(h.partial), len(p))
		h.partial = append(h.partial, p[:k]...)
		p = p[k:]
		if len(h.partial) < BlockSize {
			return n, nil
		}
		if err := h.block(h.partial); err != nil {
			return 0, err
		}
		h.partial = h.partial[:0]
	}
	for len(p) >= BlockSize {
		if err := h.block(p[:BlockSize]); err != nil {
			return 0, err
		}
		p = p[BlockSize:]
	}
	h.partial = append(h.partial, p...)
	return n, nil
}

func (h *Hasher) block(b []byte) error {
	if h.n == h.dataBlocks {
		return errors.New("verity: more data than the descriptor's blocks")
	}
	h.level0 = h.sum(b, h.level0)
	h.n++
	return nil
}

// Tree is the hash tree as it goes after the data (the top level first)
// and the root hash.
func (h *Hasher) Tree() ([]byte, [sha256.Size]byte, error) {
	var root [sha256.Size]byte
	if h.n != h.dataBlocks || len(h.partial) != 0 {
		return nil, root, fmt.Errorf("verity: %d of %d data blocks hashed", h.n, h.dataBlocks)
	}
	sizes := levelSizes(h.dataBlocks)
	if len(sizes) == 0 {
		copy(root[:], h.level0)
		return nil, root, nil
	}
	off := make([]uint64, len(sizes))
	var at uint64
	for i := len(sizes) - 1; i >= 0; i-- {
		off[i] = at * BlockSize
		at += sizes[i]
	}
	tree := make([]byte, at*BlockSize)
	copy(tree[off[0]:], h.level0)
	for i := 1; i < len(sizes); i++ {
		dst := tree[off[i]:off[i]]
		for b := uint64(0); b < sizes[i-1]; b++ {
			at := off[i-1] + b*BlockSize
			dst = h.sum(tree[at:at+BlockSize], dst)
		}
	}
	top := off[len(sizes)-1]
	copy(root[:], h.sum(tree[top:top+BlockSize], nil))
	return tree, root, nil
}
