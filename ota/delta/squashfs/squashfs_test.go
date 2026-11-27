package squashfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bundleFiles stand for an incremental bundle's: a payload (a manifest
// that compresses, then data that does not), the hook, the descriptor and
// an applier.
func bundleFiles() map[string][]byte {
	r := rand.New(rand.NewSource(1))
	var manifest strings.Builder
	for i := 0; manifest.Len() < 40<<10; i++ {
		fmt.Fprintf(&manifest, `{"type":"SOURCE_COPY","src":[[%d,8]],"dst":[[%d,8]],"dst_sha256":"%064x"},`, i*8, i*8+3, r.Uint64())
	}
	data := make([]byte, 1300<<10+777)
	r.Read(data)
	// A run of zeros, which mksquashfs keeps as holes where it covers a
	// block.
	clear(data[512<<10 : 768<<10])
	applier := bytes.Repeat([]byte("\x7fELF kario applier code "), 40000)
	return map[string][]byte{
		"rootfs.kdelta":      append([]byte("KDLT"+manifest.String()), data...),
		"delta-hook":         []byte("#!/bin/sh\necho hook\n"),
		"target.verity.desc": []byte("data_blocks=1\nhash_offset=4096\nsalt=00\nroot_hash=" + strings.Repeat("0", 64) + "\n"),
		"phone-ota-delta":    applier,
	}
}

// image makes the squashfs image of files as RAUC's bundle command calls
// mksquashfs, with extra options.
func image(t *testing.T, files map[string][]byte, opts ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		t.Skip("no mksquashfs")
	}
	dir := t.TempDir()
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "bundle.img")
	args := append([]string{dir, out, "-all-root", "-noappend", "-no-progress", "-no-xattrs"}, opts...)
	if b, err := exec.Command("mksquashfs", args...).CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs %v: %v: %s", opts, err, b)
	}
	img, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// A verity bundle's image is padded to whole blocks of its hash tree.
	return append(img, make([]byte, (Align-len(img)%Align)%Align)...)
}

// device is the bundle's device: it counts each block read, and checks
// that reads are aligned as O_DIRECT wants them (the stream's: the
// superblock and tables are read through an aligning reader on a phone).
type device struct {
	b       []byte
	reads   map[int64]int
	bad     string
	devRead int64
}

func (d *device) ReadAt(p []byte, off int64) (int, error) {
	if off%Align != 0 || len(p)%Align != 0 {
		d.bad = fmt.Sprintf("read of %d bytes at %d", len(p), off)
	}
	for b := off / Align; b < (off+int64(len(p))+Align-1)/Align; b++ {
		d.reads[b]++
	}
	if off >= int64(len(d.b)) {
		return 0, io.EOF
	}
	n := copy(p, d.b[off:])
	d.devRead += int64(n)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// fileReads counts what is read through the mounted file.
type fileReads struct {
	b []byte
	n int64
}

func (f *fileReads) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.b)) {
		return 0, io.EOF
	}
	n := copy(p, f.b[off:])
	f.n += int64(n)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// The payload streamed from the image is the file, byte for byte; its
// blocks come from the device with no device block read twice, stored ones
// as they are and compressed ones decompressed by the stream, holes as
// zeros; only a tail in a fragment comes through the file, and blocks the
// page cache holds (Cached).
func TestStream(t *testing.T) {
	files := bundleFiles()
	want := files["rootfs.kdelta"]
	for _, opts := range [][]string{nil, {"-comp", "xz"}, {"-no-fragments"}, {"-b", "65536"}, {"-noI", "-noD"}, {"-b", "1M", "-comp", "xz"}, {"-always-use-fragments"}} {
		img := image(t, files, opts...)
		dev := &device{b: img, reads: map[int64]int{}}
		fs, err := Open(dev, int64(len(img)))
		if err != nil {
			t.Fatalf("%v: %v", opts, err)
		}
		f, err := fs.Lookup("rootfs.kdelta")
		if err != nil {
			t.Fatalf("%v: %v", opts, err)
		}
		if f.Size != int64(len(want)) {
			t.Fatalf("%v: %d bytes, want %d", opts, f.Size, len(want))
		}
		onDev, stored, compressed, holes := int64(0), int64(0), 0, 0
		for _, b := range f.Blocks {
			onDev += int64(b.Size)
			switch {
			case b.Stored:
				stored += int64(b.Size)
			case b.Size == 0:
				holes++
			default:
				compressed++
			}
		}
		tail := int64(0)
		if f.Fragment {
			tail = f.Size - int64(len(f.Blocks)*fs.BlockSize)
		}
		dev.reads, dev.devRead, dev.bad = map[int64]int{}, 0, ""
		file := &fileReads{b: want}
		s, err := NewStream(fs, f, dev, file, make([]byte, 256<<10))
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if _, err := io.CopyBuffer(&got, struct{ io.Reader }{s}, make([]byte, 100<<10+3)); err != nil {
			t.Fatalf("%v: %v", opts, err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("%v: the stream is not the file", opts)
		}
		for b, n := range dev.reads {
			if n > 1 {
				t.Fatalf("%v: device block %d read %d times", opts, b, n)
			}
		}
		if dev.bad != "" {
			t.Fatalf("%v: an unaligned device read: %s", opts, dev.bad)
		}
		if file.n != tail || s.FileRead != tail || s.DevRead < onDev || s.DevRead > onDev+3*Align {
			t.Fatalf("%v: %d bytes from the device for %d there, %d through the file for a %d-byte tail", opts, s.DevRead, onDev, file.n, tail)
		}
		t.Logf("%-22v %d-byte blocks (%d compressed, %d holes): %d of %d bytes from the device (%d stored), %d through the file",
			strings.Join(opts, " "), fs.BlockSize, compressed, holes, s.DevRead, f.Size, stored, file.n)

		// The header alone (phone-ota-delta can-apply), through a window of
		// one device block: gzip decompresses only as far as its 32 KiB
		// window (an xz block comes whole), not the whole first block.
		dev.reads, dev.devRead = map[int64]int{}, 0
		s, _ = NewStream(fs, f, dev, &fileReads{b: want}, make([]byte, Align))
		head := make([]byte, 8)
		if _, err := io.ReadFull(s, head); err != nil || !bytes.Equal(head, want[:8]) {
			t.Fatalf("%v: the header: %v", opts, err)
		}
		if !strings.Contains(strings.Join(opts, " "), "xz") && f.Blocks[0].Size > 40<<10 && s.DevRead > 40<<10 {
			t.Fatalf("%v: %d bytes from the device for the header", opts, s.DevRead)
		}
		if len(opts) == 0 && (stored < f.Size/2 || holes == 0) || f.Fragment != (strings.Join(opts, " ") == "-always-use-fragments") {
			t.Fatalf("%v: %d of %d bytes stored, %d holes, fragment %v", opts, stored, f.Size, holes, f.Fragment)
		}

		// Skipping forward (a resumed install) reads none of what it skips.
		dev.reads, dev.devRead = map[int64]int{}, 0
		s, _ = NewStream(fs, f, dev, &fileReads{b: want}, make([]byte, 1<<20))
		head = make([]byte, 100)
		if _, err := io.ReadFull(s, head); err != nil {
			t.Fatal(err)
		}
		// The window starts at a squashfs block: what the skip passes over
		// is not read ahead of it.
		if before := s.DevRead; before > int64(fs.BlockSize)+Align {
			t.Fatalf("%v: read %d bytes from the device for the first 100", opts, before)
		}
		skip := f.Size / 2
		if _, err := s.Seek(skip, io.SeekCurrent); err != nil {
			t.Fatal(err)
		}
		rest, err := io.ReadAll(s)
		if err != nil || !bytes.Equal(rest, want[100+skip:]) {
			t.Fatalf("%v: after a skip: %v", opts, err)
		}
		if s.DevRead > f.Size-skip+2*int64(fs.BlockSize)+Align {
			t.Fatalf("%v: read %d bytes from the device skipping %d of %d", opts, s.DevRead, skip, f.Size)
		}
		if _, err := s.Seek(-1, io.SeekCurrent); err == nil {
			t.Fatal("a stream moved back")
		}

		for name, b := range files {
			g, err := fs.Lookup(name)
			if err != nil || g.Size != int64(len(b)) {
				t.Fatalf("%v: %s: %v", opts, name, err)
			}
		}
		for _, name := range []string{"rootfs.kdelt", "", "a/b", "rootfs.kdelta2"} {
			if _, err := fs.Lookup(name); err == nil {
				t.Fatalf("%v: found %q", opts, name)
			}
		}
	}
}

// stallingDevice stops answering past at (the network gone).
type stallingDevice struct {
	*device
	at int64
}

func (d *stallingDevice) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > d.at {
		return 0, errors.New("nbd: input/output error")
	}
	return d.device.ReadAt(p, off)
}

// When the device stops answering, the stream says so, and does not read
// the rest through the file (which would download it again through the
// kernel); a compressed block that does not decompress here from its
// start is read through the file; one that goes wrong after some of it was
// handed over fails the stream.
func TestStreamErrors(t *testing.T) {
	files := bundleFiles()
	want := files["rootfs.kdelta"]
	img := image(t, files)
	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := fs.Lookup("rootfs.kdelta")
	if err != nil {
		t.Fatal(err)
	}
	file := &fileReads{b: want}
	dev := &stallingDevice{device: &device{b: img, reads: map[int64]int{}}, at: int64(len(img)) / 2}
	s, _ := NewStream(fs, f, dev, file, make([]byte, 64<<10))
	got, err := io.ReadAll(s)
	if err == nil || !bytes.Equal(got, want[:len(got)]) || file.n != 0 {
		t.Fatalf("read %d bytes, %d through the file: %v", len(got), file.n, err)
	}

	k := -1
	for i, b := range f.Blocks {
		if !b.Stored && b.Size > 0 {
			k = i
		}
	}
	if k < 0 {
		t.Fatal("no compressed block")
	}
	bad := append([]byte{}, img...)
	bad[f.Blocks[k].Start] ^= 0xFF
	file = &fileReads{b: want}
	s, _ = NewStream(fs, f, &device{b: bad, reads: map[int64]int{}}, file, make([]byte, 64<<10))
	got, err = io.ReadAll(s)
	if err != nil || !bytes.Equal(got, want) || file.n != s.blockLen(int64(k)) {
		t.Fatalf("a block damaged at its start: %v, %d bytes through the file", err, file.n)
	}
	bad = append([]byte{}, img...)
	bad[f.Blocks[k].Start+int64(f.Blocks[k].Size)/2] ^= 0xFF
	s, _ = NewStream(fs, f, &device{b: bad, reads: map[int64]int{}}, &fileReads{b: want}, make([]byte, 64<<10))
	if _, err := io.ReadAll(s); err == nil {
		t.Fatal("a block damaged in the middle read without an error")
	}
}

// Other compression, other versions and damaged superblocks are refused:
// the applier then reads the payload as a file.
func TestOpenRefuses(t *testing.T) {
	img := image(t, map[string][]byte{"rootfs.kdelta": []byte("KDLT")})
	if _, err := Open(bytes.NewReader(img), int64(len(img))); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(b []byte){
		"magic":       func(b []byte) { b[0] ^= 1 },
		"version":     func(b []byte) { b[28] = 3 },
		"compression": func(b []byte) { b[20] = 6 },
		"block size":  func(b []byte) { b[12] = 1 },
		"bytes used":  func(b []byte) { b[47] = 1 },
	} {
		bad := append([]byte{}, img...)
		change(bad)
		if _, err := Open(bytes.NewReader(bad), int64(len(bad))); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if _, err := Open(bytes.NewReader(img[:50]), 50); err == nil {
		t.Error("a cut superblock opened")
	}
}
