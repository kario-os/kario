package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/ota/delta/squashfs"
	"golang.org/x/sys/unix"
)

const squashfsMagic = 0x73717368

// bundlePayload reads a payload in the bundle RAUC mounted (a squashfs on
// its dm-verity device) straight from the device: each block of it once,
// with O_DIRECT, in reads of up to window bytes (a multiple of
// squashfs.Align), compressed blocks decompressed here. Through the
// squashfs mount, RAUC's streamed install downloaded about a sixth of the
// bundle twice: blocks the kernel read again (nothing caches under its
// network block device), and the blocks after one read through the mount,
// which the kernel's read-ahead fetched although the payload was then read
// from the device. nil, and why, where the payload is not in such a bundle
// or the image is not one squashfs.Stream reads: the caller reads the
// file.
func bundlePayload(f *os.File, window int) (*bundleStream, error) {
	var sfs unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &sfs); err != nil || sfs.Type != squashfsMagic {
		return nil, errors.New("not in a mounted bundle")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, err
	}
	// Only the blocks squashfs.Stream asks for, no read-ahead past them.
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_RANDOM)
	uevent, err := os.ReadFile(fmt.Sprintf("/sys/dev/block/%d:%d/uevent", unix.Major(st.Dev), unix.Minor(st.Dev)))
	if err != nil {
		return nil, err
	}
	name := ""
	for _, l := range strings.Split(string(uevent), "\n") {
		if v, ok := strings.CutPrefix(l, "DEVNAME="); ok {
			name = v
		}
	}
	if name == "" || strings.Contains(name, "..") {
		return nil, errors.New("the bundle's device has no name")
	}
	path := "/dev/" + name
	dev, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECT, 0)
	if err != nil {
		if dev, err = os.Open(path); err != nil {
			return nil, err
		}
		_ = unix.Fadvise(int(dev.Fd()), 0, 0, unix.FADV_RANDOM)
	}
	ok := false
	defer func() {
		if !ok {
			dev.Close()
		}
	}()
	size, err := dev.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	buf, err := unix.Mmap(-1, 0, window+squashfs.Align, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return nil, err
	}
	fs, err := squashfs.Open(&alignedReader{f: dev, buf: buf[window:], at: -1}, size)
	if err != nil {
		return nil, err
	}
	file, err := fs.Lookup(filepath.Base(f.Name()))
	if err != nil {
		return nil, err
	}
	if uint64(file.Inode) != st.Ino || file.Size != st.Size {
		return nil, errors.New("the image's file is not the one opened")
	}
	s, err := squashfs.NewStream(fs, file, dev, f, buf[:window])
	if err != nil {
		return nil, err
	}
	ok = true
	b := &bundleStream{Stream: s, dev: dev, path: path, size: file.Size}
	for _, k := range file.Blocks {
		if b.onDev += int64(k.Size); !k.Stored && k.Size > 0 {
			b.compressed++
		}
	}
	return b, nil
}

type bundleStream struct {
	*squashfs.Stream
	dev         *os.File
	path        string
	onDev, size int64
	compressed  int
}

func (b *bundleStream) Close() error { return b.dev.Close() }

// alignedReader reads any range through whole aligned blocks of an
// O_DIRECT file (the superblock and the tables), keeping the last block
// read: a table's small reads one after another would each download it.
type alignedReader struct {
	f     io.ReaderAt
	buf   []byte
	at    int64
	n     int
	reads int
}

func (a *alignedReader) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for len(p) > 0 {
		at := off &^ (squashfs.Align - 1)
		if at != a.at || int(off-at) >= a.n {
			got, err := a.f.ReadAt(a.buf[:squashfs.Align], at)
			a.reads++
			a.at, a.n = at, got
			if got <= int(off-at) {
				a.at = -1
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return n, err
			}
		}
		k := copy(p, a.buf[off-at:a.n])
		p, off, n = p[k:], off+int64(k), n+k
	}
	return n, nil
}
