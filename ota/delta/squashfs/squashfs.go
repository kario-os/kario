// Package squashfs finds where a file of a squashfs image (version 4.0, as
// mksquashfs writes it; a RAUC bundle's image is one) lies, block by block,
// so that an incremental update's applier reads its payload straight from
// the bundle's dm-verity device, each byte once (Stream), instead of
// through the kernel's squashfs reads and page cache. RAUC's streamed
// install has no cache under its network block device, and blocks the
// kernel reads twice are downloaded twice (25-system-update.md §5.5).
//
// Only what that needs: the superblock, the inode and directory tables
// (metadata stored, gzip- or xz-compressed), directory and regular file
// inodes, data blocks stored or compressed the same ways. Anything else is
// an error, and the caller reads the file (or that block) as a file.
package squashfs

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kario-os/kario/ota/delta/xz"
)

const (
	magic        = 0x73717368
	superSize    = 96
	metaMax      = 8192
	compGzip     = 1
	compXZ       = 4
	noFragment   = 0xFFFFFFFF
	blockStored  = 1 << 24
	sizeMask     = blockStored - 1
	typeDir      = 1
	typeFile     = 2
	typeExtDir   = 8
	typeExtFile  = 9
	maxListing   = 16 << 20
	maxBlockSize = 1 << 20
)

var errFormat = errors.New("squashfs: not an image this reads")

// FS is an image's superblock, as far as finding files needs it.
type FS struct {
	dev        io.ReaderAt
	size       int64
	BlockSize  int
	comp       int
	rootRef    uint64
	BytesUsed  int64
	inodeTable int64
	dirTable   int64
	tableEnd   int64
}

// Open reads the superblock of the image on dev (size bytes).
func Open(dev io.ReaderAt, size int64) (*FS, error) {
	var sb [superSize]byte
	if _, err := dev.ReadAt(sb[:], 0); err != nil {
		return nil, fmt.Errorf("squashfs: %w", err)
	}
	le := binary.LittleEndian
	fs := &FS{dev: dev, size: size, BlockSize: int(le.Uint32(sb[12:])), comp: int(le.Uint16(sb[20:])), rootRef: le.Uint64(sb[32:]),
		BytesUsed: int64(le.Uint64(sb[40:])), inodeTable: int64(le.Uint64(sb[64:])), dirTable: int64(le.Uint64(sb[72:]))}
	blockLog, major := le.Uint16(sb[22:]), le.Uint16(sb[28:])
	switch {
	case le.Uint32(sb[0:]) != magic || major != 4:
		return nil, errFormat
	case fs.BlockSize < 4096 || fs.BlockSize > maxBlockSize || 1<<blockLog != fs.BlockSize:
		return nil, errors.New("squashfs: bad block size")
	case fs.comp != compGzip && fs.comp != compXZ:
		return nil, fmt.Errorf("squashfs: compression %d, this reads gzip and xz", fs.comp)
	case fs.BytesUsed < superSize || fs.BytesUsed > size || fs.inodeTable < superSize || fs.dirTable < fs.inodeTable || fs.dirTable >= fs.BytesUsed:
		return nil, errors.New("squashfs: tables outside the image")
	}
	// The directory table ends at the first table after it.
	fs.tableEnd = fs.BytesUsed
	for _, at := range []int{48, 56, 80, 88} {
		if t := int64(le.Uint64(sb[at:])); t > fs.dirTable && t < fs.tableEnd {
			fs.tableEnd = t
		}
	}
	return fs, nil
}

// metaBlock reads the metadata block at pos: its data and where the next
// one starts.
func (fs *FS) metaBlock(pos, end int64) ([]byte, int64, error) {
	var h [2]byte
	if pos < 0 || pos+2 > end {
		return nil, 0, errors.New("squashfs: metadata outside its table")
	}
	if _, err := fs.dev.ReadAt(h[:], pos); err != nil {
		return nil, 0, err
	}
	hdr := binary.LittleEndian.Uint16(h[:])
	n := int64(hdr & 0x7FFF)
	if n == 0 || n > metaMax || pos+2+n > end {
		return nil, 0, errors.New("squashfs: bad metadata block")
	}
	raw := make([]byte, n)
	if _, err := fs.dev.ReadAt(raw, pos+2); err != nil {
		return nil, 0, err
	}
	next := pos + 2 + n
	if hdr&0x8000 != 0 {
		return raw, next, nil
	}
	out, err := fs.decompress(raw, metaMax)
	if err != nil {
		return nil, 0, fmt.Errorf("squashfs: a metadata block does not decompress: %v", err)
	}
	return out, next, nil
}

// decompress is what raw, compressed as the image says, holds: at most
// max bytes.
func (fs *FS) decompress(raw []byte, max int) ([]byte, error) {
	var out []byte
	var err error
	switch fs.comp {
	case compGzip:
		var zr io.ReadCloser
		if zr, err = zlib.NewReader(bytes.NewReader(raw)); err == nil {
			out, err = io.ReadAll(io.LimitReader(zr, int64(max)+1))
		}
	case compXZ:
		out, err = xz.DecodeUpTo(raw, max)
	}
	if err == nil && len(out) > max {
		err = errors.New("more than a block")
	}
	return out, err
}

// meta reads metadata from a table, block after block.
type meta struct {
	fs       *FS
	end      int64
	next     int64
	buf      []byte
	consumed int
}

func (fs *FS) meta(table, end int64, block uint64, offset int) (*meta, error) {
	m := &meta{fs: fs, end: end, next: table + int64(block)}
	if block > uint64(end-table) || m.fill() != nil || offset > len(m.buf) {
		return nil, errors.New("squashfs: metadata reference outside its table")
	}
	m.buf = m.buf[offset:]
	return m, nil
}

func (m *meta) fill() error {
	b, next, err := m.fs.metaBlock(m.next, m.end)
	if err != nil {
		return err
	}
	m.buf, m.next = b, next
	return nil
}

func (m *meta) read(n int) ([]byte, error) {
	if m.consumed += n; n < 0 || m.consumed > maxListing {
		return nil, errors.New("squashfs: metadata too long")
	}
	out := make([]byte, 0, n)
	for len(out) < n {
		if len(m.buf) == 0 {
			if err := m.fill(); err != nil {
				return nil, err
			}
		}
		k := min(n-len(out), len(m.buf))
		out, m.buf = append(out, m.buf[:k]...), m.buf[k:]
	}
	return out, nil
}

func (m *meta) u16() (uint16, error) {
	b, err := m.read(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

func (m *meta) u32() (uint32, error) {
	b, err := m.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (m *meta) u64() (uint64, error) {
	b, err := m.read(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// Block is one of a file's data blocks: where it starts in the image, its
// bytes there, and whether they are stored as they are (else compressed;
// a block of no bytes is a hole, zeros).
type Block struct {
	Start  int64
	Size   int
	Stored bool
}

// File is a regular file: its inode number and size, its blocks, and
// whether its tail lies in a fragment block (with other files' tails).
type File struct {
	Inode    uint32
	Size     int64
	Blocks   []Block
	Fragment bool
}

// inode reads the inode ref points at: its type and number, and the reader
// at what follows the common header.
func (fs *FS) inode(ref uint64) (uint16, uint32, *meta, error) {
	m, err := fs.meta(fs.inodeTable, fs.dirTable, ref>>16, int(ref&0xFFFF))
	if err != nil {
		return 0, 0, nil, err
	}
	h, err := m.read(16)
	if err != nil {
		return 0, 0, nil, err
	}
	return binary.LittleEndian.Uint16(h), binary.LittleEndian.Uint32(h[12:]), m, nil
}

// Lookup finds a regular file in the root directory.
func (fs *FS) Lookup(name string) (*File, error) {
	if name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("squashfs: %q is not a name in the root directory", name)
	}
	typ, _, m, err := fs.inode(fs.rootRef)
	if err != nil {
		return nil, err
	}
	var block uint32
	var size uint32
	var offset uint16
	switch typ {
	case typeDir:
		if block, err = m.u32(); err == nil {
			_, err = m.u32()
		}
		var s16 uint16
		if err == nil {
			s16, err = m.u16()
		}
		if err == nil {
			offset, err = m.u16()
		}
		size = uint32(s16)
	case typeExtDir:
		if _, err = m.u32(); err == nil {
			size, err = m.u32()
		}
		if err == nil {
			block, err = m.u32()
		}
		if err == nil {
			_, err = m.read(6)
		}
		if err == nil {
			offset, err = m.u16()
		}
	default:
		return nil, errors.New("squashfs: the root is not a directory")
	}
	if err != nil {
		return nil, err
	}
	if size < 3 {
		return nil, fmt.Errorf("squashfs: no %s", name)
	}
	d, err := fs.meta(fs.dirTable, fs.tableEnd, uint64(block), int(offset))
	if err != nil {
		return nil, err
	}
	for left := int(size) - 3; left > 0; {
		h, err := d.read(12)
		if err != nil {
			return nil, err
		}
		count, start, base := binary.LittleEndian.Uint32(h)+1, binary.LittleEndian.Uint32(h[4:]), binary.LittleEndian.Uint32(h[8:])
		left -= 12
		if count > 256 {
			return nil, errors.New("squashfs: bad directory")
		}
		for i := uint32(0); i < count; i++ {
			e, err := d.read(8)
			if err != nil {
				return nil, err
			}
			off, delta, nameLen := binary.LittleEndian.Uint16(e), int16(binary.LittleEndian.Uint16(e[2:])), int(binary.LittleEndian.Uint16(e[6:]))+1
			n, err := d.read(nameLen)
			if err != nil {
				return nil, err
			}
			left -= 8 + nameLen
			if string(n) == name {
				return fs.file(uint64(start)<<16|uint64(off), uint32(int64(base)+int64(delta)))
			}
		}
	}
	return nil, fmt.Errorf("squashfs: no %s", name)
}

// file reads a regular file's inode.
func (fs *FS) file(ref uint64, number uint32) (*File, error) {
	typ, ino, m, err := fs.inode(ref)
	if err != nil {
		return nil, err
	}
	if ino != number {
		return nil, errors.New("squashfs: the directory and the inode disagree")
	}
	var start, size uint64
	var frag uint32
	switch typ {
	case typeFile:
		var s, z uint32
		if s, err = m.u32(); err == nil {
			frag, err = m.u32()
		}
		if err == nil {
			_, err = m.u32()
		}
		if err == nil {
			z, err = m.u32()
		}
		start, size = uint64(s), uint64(z)
	case typeExtFile:
		if start, err = m.u64(); err == nil {
			size, err = m.u64()
		}
		if err == nil {
			_, err = m.read(12)
		}
		if err == nil {
			frag, err = m.u32()
		}
		if err == nil {
			_, err = m.read(8)
		}
	default:
		return nil, errors.New("squashfs: not a regular file")
	}
	if err != nil {
		return nil, err
	}
	bs := uint64(fs.BlockSize)
	if size > uint64(fs.BytesUsed)*uint64(maxBlockSize) || start > uint64(fs.BytesUsed) {
		return nil, errors.New("squashfs: bad file inode")
	}
	f := &File{Inode: ino, Size: int64(size), Fragment: frag != noFragment}
	n := (size + bs - 1) / bs
	if f.Fragment {
		n = size / bs
	}
	at := int64(start)
	for i := uint64(0); i < n; i++ {
		w, err := m.u32()
		if err != nil {
			return nil, err
		}
		b := Block{Start: at, Size: int(w & sizeMask), Stored: w&blockStored != 0}
		if b.Size > fs.BlockSize || at+int64(b.Size) > fs.BytesUsed {
			return nil, errors.New("squashfs: a data block outside the image")
		}
		f.Blocks = append(f.Blocks, b)
		at += int64(b.Size)
	}
	return f, nil
}
