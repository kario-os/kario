// Package ext4 reads what an incremental system update needs from an ext4
// image (a slot's file system, the state partition): which blocks hold each
// file's data, and small files' contents. Read-only and without the
// journal: the images come from mkfs.ext4 and are only ever mounted
// read-only.
package ext4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

const (
	incompatMetaBG     = 0x10
	incompat64Bit      = 0x80
	flagExtents        = 0x80000
	flagInlineData     = 0x10000000
	extentMagic        = 0xF30A
	maxDepth           = 5
	rootInode          = 2
	modeType           = 0xF000
	ModeRegular        = 0x8000
	ModeDir            = 0x4000
	ModeSymlink        = 0xA000
	uninitExtentLength = 32768
)

// FS is an open ext4 image.
type FS struct {
	r              io.ReaderAt
	BlockSize      int64
	Blocks         uint64
	inodeSize      int64
	inodes         uint32
	inodesPerGroup uint32
	inodeTables    []uint64
}

// Extent is a run of a file's blocks: Len blocks from file block Logical
// stored from image block Physical.
type Extent struct {
	Logical, Physical, Len uint64
}

// File is an inode reached by a path.
type File struct {
	Path    string
	Inode   uint32
	Mode    uint16
	Size    int64
	Extents []Extent // in file order; holes and inline data have none
}

// IsRegular, IsDir: the inode's type.
func (f File) IsRegular() bool { return f.Mode&modeType == ModeRegular }
func (f File) IsDir() bool     { return f.Mode&modeType == ModeDir }

var le = binary.LittleEndian

// Open reads the superblock and the group descriptors.
func Open(r io.ReaderAt) (*FS, error) {
	sb := make([]byte, 1024)
	if _, err := r.ReadAt(sb, 1024); err != nil {
		return nil, fmt.Errorf("ext4: %w", err)
	}
	if le.Uint16(sb[0x38:]) != 0xEF53 {
		return nil, errors.New("ext4: not an ext4 file system")
	}
	logBlock := le.Uint32(sb[0x18:])
	if logBlock > 6 {
		return nil, errors.New("ext4: bad block size")
	}
	fs := &FS{r: r, BlockSize: 1024 << logBlock, inodes: le.Uint32(sb[0x00:]), inodesPerGroup: le.Uint32(sb[0x28:]), inodeSize: 128}
	incompat := le.Uint32(sb[0x60:])
	if incompat&incompatMetaBG != 0 {
		return nil, errors.New("ext4: meta_bg is not supported")
	}
	if le.Uint32(sb[0x4C:]) >= 1 {
		fs.inodeSize = int64(le.Uint16(sb[0x58:]))
	}
	fs.Blocks = uint64(le.Uint32(sb[0x04:]))
	descSize := int64(32)
	if incompat&incompat64Bit != 0 {
		fs.Blocks |= uint64(le.Uint32(sb[0x150:])) << 32
		descSize = int64(le.Uint16(sb[0xFE:]))
	}
	first, perGroup := uint64(le.Uint32(sb[0x14:])), uint64(le.Uint32(sb[0x20:]))
	if fs.inodeSize < 128 || fs.inodeSize > fs.BlockSize || descSize < 32 || perGroup == 0 || fs.inodesPerGroup == 0 || fs.Blocks <= first {
		return nil, errors.New("ext4: bad superblock")
	}
	groups := (fs.Blocks - first + perGroup - 1) / perGroup
	if groups > 1<<24 || uint64(fs.inodesPerGroup)*groups < uint64(fs.inodes) {
		return nil, errors.New("ext4: bad superblock")
	}
	gdt := make([]byte, int64(groups)*descSize)
	if _, err := r.ReadAt(gdt, int64(first+1)*fs.BlockSize); err != nil {
		return nil, fmt.Errorf("ext4: group descriptors: %w", err)
	}
	fs.inodeTables = make([]uint64, groups)
	for g := range fs.inodeTables {
		d := gdt[int64(g)*descSize:]
		t := uint64(le.Uint32(d[0x08:]))
		if descSize >= 64 {
			t |= uint64(le.Uint32(d[0x28:])) << 32
		}
		fs.inodeTables[g] = t
	}
	return fs, nil
}

func (fs *FS) block(n uint64) ([]byte, error) {
	if n >= fs.Blocks {
		return nil, fmt.Errorf("ext4: block %d is outside the file system", n)
	}
	b := make([]byte, fs.BlockSize)
	_, err := fs.r.ReadAt(b, int64(n)*fs.BlockSize)
	return b, err
}

// inode reads inode ino into a File (without its path).
func (fs *FS) inode(ino uint32) (File, error) {
	f := File{Inode: ino}
	if ino == 0 || ino > fs.inodes {
		return f, fmt.Errorf("ext4: no inode %d", ino)
	}
	g, i := (ino-1)/fs.inodesPerGroup, (ino-1)%fs.inodesPerGroup
	raw := make([]byte, min(fs.inodeSize, 160))
	if _, err := fs.r.ReadAt(raw, int64(fs.inodeTables[g])*fs.BlockSize+int64(i)*fs.inodeSize); err != nil {
		return f, fmt.Errorf("ext4: inode %d: %w", ino, err)
	}
	f.Mode = le.Uint16(raw[0x00:])
	f.Size = int64(uint64(le.Uint32(raw[0x04:])) | uint64(le.Uint32(raw[0x6C:]))<<32)
	flags := le.Uint32(raw[0x20:])
	iblock := raw[0x28 : 0x28+60]
	var err error
	switch {
	case flags&flagInlineData != 0:
	case f.Mode&modeType == ModeSymlink && f.Size < 60 && flags&flagExtents == 0:
		// A fast symlink: the target is in i_block.
	case flags&flagExtents != 0:
		err = fs.extentNode(iblock, -1, &f.Extents)
	default:
		err = fs.blockMap(iblock, f.Size, &f.Extents)
	}
	if err != nil {
		return f, fmt.Errorf("ext4: inode %d: %w", ino, err)
	}
	sort.Slice(f.Extents, func(a, b int) bool { return f.Extents[a].Logical < f.Extents[b].Logical })
	return f, nil
}

func (fs *FS) add(out *[]Extent, logical, physical, n uint64) error {
	if physical+n > fs.Blocks || physical+n < physical {
		return errors.New("an extent reaches outside the file system")
	}
	if k := len(*out); k > 0 {
		e := &(*out)[k-1]
		if e.Logical+e.Len == logical && e.Physical+e.Len == physical {
			e.Len += n
			return nil
		}
	}
	*out = append(*out, Extent{Logical: logical, Physical: physical, Len: n})
	return nil
}

// extentNode reads an extent tree node; below the root (depth -1: any),
// each level is one less deep than its parent.
func (fs *FS) extentNode(b []byte, depth int, out *[]Extent) error {
	if len(b) < 12 || le.Uint16(b) != extentMagic {
		return errors.New("bad extent header")
	}
	entries, max, d := int(le.Uint16(b[2:])), int(le.Uint16(b[4:])), int(le.Uint16(b[6:]))
	if entries > max || 12+12*entries > len(b) || d > maxDepth || (depth >= 0 && d != depth) {
		return errors.New("bad extent node")
	}
	for i := 0; i < entries; i++ {
		e := b[12+12*i:]
		if d == 0 {
			n := uint64(le.Uint16(e[4:]))
			if n > uninitExtentLength {
				n -= uninitExtentLength
			}
			if err := fs.add(out, uint64(le.Uint32(e)), uint64(le.Uint16(e[6:]))<<32|uint64(le.Uint32(e[8:])), n); err != nil {
				return err
			}
			continue
		}
		child, err := fs.block(uint64(le.Uint16(e[8:]))<<32 | uint64(le.Uint32(e[4:])))
		if err != nil {
			return err
		}
		if err := fs.extentNode(child, d-1, out); err != nil {
			return err
		}
	}
	return nil
}

// blockMap reads the block map of an inode without extents (ext2/ext3
// style: 12 direct pointers, then single, double and triple indirect).
func (fs *FS) blockMap(iblock []byte, size int64, out *[]Extent) error {
	blocks := uint64((size + fs.BlockSize - 1) / fs.BlockSize)
	per := uint64(fs.BlockSize / 4)
	var walk func(ptr uint64, level int, logical uint64) error
	walk = func(ptr uint64, level int, logical uint64) error {
		if ptr == 0 || logical >= blocks {
			return nil
		}
		if level == 0 {
			return fs.add(out, logical, ptr, 1)
		}
		b, err := fs.block(ptr)
		if err != nil {
			return err
		}
		span := uint64(1)
		for i := 1; i < level; i++ {
			span *= per
		}
		for i := uint64(0); i < per; i++ {
			if err := walk(uint64(le.Uint32(b[4*i:])), level-1, logical+i*span); err != nil {
				return err
			}
		}
		return nil
	}
	logical := uint64(0)
	for i := 0; i < 15; i++ {
		level := max(i-11, 0)
		if err := walk(uint64(le.Uint32(iblock[4*i:])), level, logical); err != nil {
			return err
		}
		switch level {
		case 0:
			logical++
		case 1:
			logical += per
		case 2:
			logical += per * per
		}
	}
	return nil
}

// Read is a file's data (holes as zeros).
func (fs *FS) Read(f File) ([]byte, error) {
	if f.Size < 0 || f.Size > 1<<30 {
		return nil, fmt.Errorf("ext4: %s: size %d", f.Path, f.Size)
	}
	data := make([]byte, f.Size)
	for _, e := range f.Extents {
		for i := uint64(0); i < e.Len; i++ {
			at := int64(e.Logical+i) * fs.BlockSize
			if at >= f.Size {
				break
			}
			b, err := fs.block(e.Physical + i)
			if err != nil {
				return nil, err
			}
			copy(data[at:], b)
		}
	}
	return data, nil
}

type dirent struct {
	name  string
	inode uint32
}

func (fs *FS) readDir(f File) ([]dirent, error) {
	data, err := fs.Read(f)
	if err != nil {
		return nil, err
	}
	var out []dirent
	for off := 0; off+8 <= len(data); {
		ino, recLen, nameLen := le.Uint32(data[off:]), int(le.Uint16(data[off+4:])), int(data[off+6])
		if recLen < 8 || off+recLen > len(data) || 8+nameLen > recLen {
			return nil, fmt.Errorf("ext4: %s: bad directory entry", f.Path)
		}
		if name := string(data[off+8 : off+8+nameLen]); ino != 0 && name != "." && name != ".." {
			out = append(out, dirent{name, ino})
		}
		off += recLen
	}
	return out, nil
}

// Walk calls fn for every file, directory and symlink reachable from the
// root, parents first; a hard-linked file comes once per path.
func (fs *FS) Walk(fn func(File) error) error {
	root, err := fs.inode(rootInode)
	if err != nil {
		return err
	}
	root.Path = "/"
	seen := map[uint32]bool{}
	var visit func(dir File, depth int) error
	visit = func(dir File, depth int) error {
		if depth > 256 || seen[dir.Inode] {
			return fmt.Errorf("ext4: %s: directory loop", dir.Path)
		}
		seen[dir.Inode] = true
		entries, err := fs.readDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(a, b int) bool { return entries[a].name < entries[b].name })
		for _, e := range entries {
			f, err := fs.inode(e.inode)
			if err != nil {
				return err
			}
			f.Path = path.Join(dir.Path, e.name)
			if err := fn(f); err != nil {
				return err
			}
			if f.IsDir() {
				if err := visit(f, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := fn(root); err != nil {
		return err
	}
	return visit(root, 0)
}

// Lookup finds the inode at an absolute path (no symlinks followed).
func (fs *FS) Lookup(p string) (File, error) {
	f, err := fs.inode(rootInode)
	if err != nil {
		return f, err
	}
	f.Path = "/"
	for _, name := range strings.Split(strings.Trim(path.Clean(p), "/"), "/") {
		if name == "" {
			continue
		}
		if !f.IsDir() {
			return f, fmt.Errorf("ext4: %s: not a directory", f.Path)
		}
		entries, err := fs.readDir(f)
		if err != nil {
			return f, err
		}
		found := false
		for _, e := range entries {
			if e.name == name {
				dir := f.Path
				if f, err = fs.inode(e.inode); err != nil {
					return f, err
				}
				f.Path, found = path.Join(dir, name), true
				break
			}
		}
		if !found {
			return f, fmt.Errorf("ext4: %s: no such file", p)
		}
	}
	return f, nil
}

// ReadFile is the data of the regular file at p.
func (fs *FS) ReadFile(p string) ([]byte, error) {
	f, err := fs.Lookup(p)
	if err != nil {
		return nil, err
	}
	if !f.IsRegular() {
		return nil, fmt.Errorf("ext4: %s: not a regular file", p)
	}
	return fs.Read(f)
}
