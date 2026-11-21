package ext4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// image is a small ext4 image of 1 KiB blocks written by hand: a root
// directory with extents holding a file, a fast symlink and a directory
// without extents whose file goes through an indirect block.
func image() []byte {
	const bs = 1024
	img := make([]byte, 16*bs)
	sb := img[1024:]
	le.PutUint32(sb[0x00:], 32)
	le.PutUint32(sb[0x04:], 16)
	le.PutUint32(sb[0x14:], 1)
	le.PutUint32(sb[0x20:], 8192)
	le.PutUint32(sb[0x28:], 32)
	le.PutUint16(sb[0x38:], 0xEF53)
	le.PutUint32(sb[0x4C:], 1)
	le.PutUint16(sb[0x58:], 128)
	le.PutUint32(img[2*bs+0x08:], 3)
	inode := func(n int, mode uint16, size uint32, flags uint32) []byte {
		b := img[3*bs+(n-1)*128:]
		le.PutUint16(b, mode)
		le.PutUint32(b[0x04:], size)
		le.PutUint32(b[0x20:], flags)
		return b[0x28:]
	}
	extent := func(ib []byte, block uint32) {
		le.PutUint16(ib, extentMagic)
		le.PutUint16(ib[2:], 1)
		le.PutUint16(ib[4:], 4)
		le.PutUint16(ib[12+4:], 1)
		le.PutUint32(ib[12+8:], block)
	}
	dir := func(block int, entries ...any) {
		b := img[block*bs : (block+1)*bs]
		off := 0
		for i := 0; i < len(entries); i += 2 {
			name, ino := entries[i].(string), entries[i+1].(int)
			rec := 8 + (len(name)+3)&^3
			if i+2 == len(entries) {
				rec = bs - off
			}
			le.PutUint32(b[off:], uint32(ino))
			le.PutUint16(b[off+4:], uint16(rec))
			b[off+6] = byte(len(name))
			copy(b[off+8:], name)
			off += rec
		}
	}
	extent(inode(2, ModeDir|0o755, bs, flagExtents), 10)
	dir(10, ".", 2, "..", 2, "file", 12, "dir", 13, "link", 14)
	extent(inode(12, ModeRegular|0o644, 5, flagExtents), 11)
	copy(img[11*bs:], "hello")
	le.PutUint32(inode(13, ModeDir|0o755, bs, 0), 12)
	dir(12, ".", 13, "..", 2, "deep", 15)
	le.PutUint32(inode(15, ModeRegular|0o644, 13*bs, 0)[12*4:], 13)
	le.PutUint32(img[13*bs:], 14)
	copy(img[14*bs:], "deep")
	copy(inode(14, ModeSymlink|0o777, 6, 0), "target")
	return img
}

var errEnough = errors.New("enough")

// FuzzImage is a slot image or a state partition the update tools read:
// on the build host the images given, on the phone the state partition.
func FuzzImage(f *testing.F) {
	f.Add(image())
	f.Fuzz(func(t *testing.T, img []byte) {
		if len(img) > 1<<18 {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			fs, err := Open(bytes.NewReader(img))
			if err != nil {
				return
			}
			n := 0
			fs.Walk(func(fl File) error {
				if n++; n > 4096 {
					return errEnough
				}
				if fl.IsRegular() && fl.Size < 1<<20 {
					fs.Read(fl)
				}
				return nil
			})
			if fl, err := fs.Lookup("/dir/deep"); err == nil && fl.Size < 1<<20 {
				fs.Read(fl)
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("reading the image did not end")
		}
	})
}

// An extent tree whose every index entry points at the next level's one
// block would have Walk read 84^5 blocks for one inode.
func TestExtentTreeIsBounded(t *testing.T) {
	img := image()
	const bs = 1024
	img = append(img, make([]byte, 6*bs)...)
	le.PutUint32(img[1024+0x04:], 22)
	node := func(b []byte, depth int, child uint32) {
		le.PutUint16(b, extentMagic)
		n := (len(b) - 12) / 12
		le.PutUint16(b[2:], uint16(n))
		le.PutUint16(b[4:], uint16(n))
		le.PutUint16(b[6:], uint16(depth))
		for i := range n {
			e := b[12+12*i:]
			clear(e[:12])
			if depth == 0 {
				binary.LittleEndian.PutUint16(e[4:], 1)
				le.PutUint32(e[8:], 11)
			} else {
				le.PutUint32(e[4:], child)
			}
		}
	}
	node(img[3*bs+11*128+0x28:][:60], 5, 16)
	for level := 4; level >= 0; level-- {
		node(img[(16+4-level)*bs:][:bs], level, uint32(16+5-level))
	}
	done := make(chan error, 1)
	go func() {
		fs, err := Open(bytes.NewReader(img))
		if err == nil {
			_, err = fs.inode(12)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an extent tree of 4 billion leaves read")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading the extent tree did not end")
	}
}
