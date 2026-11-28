package squashfs

import (
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// Align is the unit of the device reads: the dm-verity block of a RAUC
// bundle, and what O_DIRECT wants of offsets, lengths and buffers.
const Align = 4096

// Stream reads a file from start to end (or skipping forward): its data
// blocks straight from dev, stored ones as they are and compressed ones
// decompressed here (gzip as far as the stream reads, xz a block at a
// time), in aligned reads that never read a device block twice while the
// stream goes forward. Through file, the file as the kernel's squashfs
// serves it, go only a tail in a fragment block and a block that does not
// decompress here. mksquashfs keeps a block compressed when deflate takes
// as much as a byte off it, as it does off some blocks of a payload whose
// data is compressed already; read through the kernel, such a block
// downloads again the device blocks it shares with the blocks around it.
type Stream struct {
	fs        *FS
	f         *File
	dev, file io.ReaderAt
	pos       int64
	buf       []byte
	bufAt     int64
	bufN      int
	// window is what the next device read asks for: first a squashfs
	// block, doubling while the stream reads on (to all of buf), back to a
	// block after a skip. A resumed install reads the manifest and then
	// skips, and the window must not read far past what it then skips.
	window, first int64
	// runEnd: where on the device the run of blocks that holds block k
	// ends; a read never goes past it.
	runEnd []int64
	// zk is the compressed block being read: z decompresses it (gzip), zAt
	// counts what z gave so far, zdev is what z reads from; or block holds
	// it decompressed whole (xz).
	zk    int64
	z     io.ReadCloser
	zAt   int64
	zdev  *blockReader
	block []byte
	// DevRead and FileRead count the bytes read from dev and file.
	DevRead, FileRead int64
}

// NewStream reads f of fs from dev, and the blocks it cannot from file;
// buf, whose length is a multiple of Align, holds what was read from dev
// (for O_DIRECT, aligned in memory too).
func NewStream(fs *FS, f *File, dev, file io.ReaderAt, buf []byte) (*Stream, error) {
	if len(buf) == 0 || len(buf)%Align != 0 {
		return nil, errors.New("squashfs: the buffer is not whole blocks")
	}
	s := &Stream{fs: fs, f: f, dev: dev, file: file, buf: buf, bufAt: -1, zk: -1, runEnd: make([]int64, len(f.Blocks))}
	s.first = min(int64(len(buf)), (int64(fs.BlockSize)+Align-1)&^(Align-1))
	for k := len(f.Blocks) - 1; k >= 0; k-- {
		b := f.Blocks[k]
		s.runEnd[k] = b.Start + int64(b.Size)
		if k+1 < len(f.Blocks) && f.Blocks[k+1].Start == s.runEnd[k] {
			s.runEnd[k] = s.runEnd[k+1]
		}
	}
	return s, nil
}

// stored: block k holds its bytes as they are.
func (s *Stream) stored(k int64) bool {
	return k < int64(len(s.f.Blocks)) && s.f.Blocks[k].Stored && int64(s.f.Blocks[k].Size) == s.blockLen(k)
}

// blockLen is the bytes of the file block k holds.
func (s *Stream) blockLen(k int64) int64 {
	return min(int64(s.fs.BlockSize), s.f.Size-k*int64(s.fs.BlockSize))
}

func (s *Stream) Read(p []byte) (int, error) {
	if s.pos >= s.f.Size {
		return 0, io.EOF
	}
	bs := int64(s.fs.BlockSize)
	k, within := s.pos/bs, s.pos%bs
	if k >= int64(len(s.f.Blocks)) {
		return s.fromFile(p[:min(int64(len(p)), s.f.Size-s.pos)])
	}
	n := min(int64(len(p)), s.blockLen(k)-within)
	switch b := s.f.Blocks[k]; {
	case b.Size == 0 && !b.Stored:
		clear(p[:n])
	case s.stored(k):
		if err := s.fromDev(p[:n], b.Start+within, s.runEnd[k]); err != nil {
			return 0, err
		}
	default:
		err := s.inflate(k, within, p[:n])
		if errors.Is(err, errBlock) {
			return s.fromFile(p[:n])
		} else if err != nil {
			return 0, err
		}
	}
	s.pos += n
	return int(n), nil
}

// fromFile reads p at the stream's place through the mounted file.
func (s *Stream) fromFile(p []byte) (int, error) {
	got, err := s.file.ReadAt(p, s.pos)
	s.FileRead += int64(got)
	s.pos += int64(got)
	if got < len(p) {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return got, err
	}
	return got, nil
}

// errBlock: a data block this does not decompress, which the kernel may.
var errBlock = errors.New("squashfs: a data block does not decompress to its size")

// blockReader reads a compressed block's bytes from the device, through
// the stream's window.
type blockReader struct {
	s             *Stream
	off, end, run int64
	err           error
}

// Read hands over what the window holds at the block's next byte, reading
// the device only when it holds none: the header (can-apply) comes from
// the first device block alone.
func (r *blockReader) Read(p []byte) (int, error) {
	if r.off >= r.end {
		return 0, io.EOF
	}
	if err := r.s.fill(r.off, r.run); err != nil {
		r.err = err
		return 0, err
	}
	n := copy(p[:min(int64(len(p)), r.end-r.off)], r.s.buf[r.off-r.s.bufAt:r.s.bufN])
	r.off += int64(n)
	return n, nil
}

// inflate fills p with compressed block k's bytes from within on: errBlock
// when it does not decompress here, the device's error when the device
// fails.
func (s *Stream) inflate(k, within int64, p []byte) error {
	b := s.f.Blocks[k]
	if b.Stored {
		return errBlock
	}
	if s.fs.comp == compXZ {
		if k != s.zk {
			raw := make([]byte, b.Size)
			if err := s.fromDev(raw, b.Start, s.runEnd[k]); err != nil {
				return err
			}
			out, err := s.fs.decompress(raw, int(s.blockLen(k)))
			if err != nil || int64(len(out)) != s.blockLen(k) {
				return errBlock
			}
			s.block, s.zk = out, k
		}
		copy(p, s.block[within:])
		return nil
	}
	if k != s.zk || within < s.zAt {
		if s.z != nil {
			s.z.Close()
		}
		s.zdev = &blockReader{s: s, off: b.Start, end: b.Start + int64(b.Size), run: s.runEnd[k]}
		z, err := zlib.NewReader(s.zdev)
		if err != nil {
			s.zk = -1
			if s.zdev.err != nil {
				return s.zdev.err
			}
			return errBlock
		}
		s.z, s.zk, s.zAt = z, k, 0
	}
	given := s.zAt > within || within > 0 && s.zAt == within
	if within > s.zAt {
		if _, err := io.CopyN(io.Discard, s.z, within-s.zAt); err != nil {
			return s.inflateFailed(k, given, err)
		}
		s.zAt = within
	}
	if _, err := io.ReadFull(s.z, p); err != nil {
		return s.inflateFailed(k, given, err)
	}
	s.zAt += int64(len(p))
	if s.zAt == s.blockLen(k) {
		// The block's end, and its checksum: nothing more comes out.
		if n, err := s.z.Read(make([]byte, 1)); n != 0 || err != io.EOF {
			return s.inflateFailed(k, true, errors.New("more than its size"))
		}
	}
	return nil
}

// inflateFailed: why decompressing block k stopped. Where none of it was
// handed over yet, the file may serve it (errBlock); else the stream
// fails.
func (s *Stream) inflateFailed(k int64, given bool, err error) error {
	s.zk = -1
	switch {
	case s.zdev.err != nil:
		return s.zdev.err
	case !given:
		return errBlock
	}
	return fmt.Errorf("squashfs: data block %d: %w", k, err)
}

// fromDev copies the device's bytes at off into p through the window of
// what was read last.
func (s *Stream) fromDev(p []byte, off, end int64) error {
	for len(p) > 0 {
		if err := s.fill(off, end); err != nil {
			return err
		}
		k := copy(p, s.buf[off-s.bufAt:s.bufN])
		p, off = p[k:], off+int64(k)
	}
	return nil
}

// fill makes the window hold the device's byte at off. A new window starts
// at the aligned block that holds off, which lies past the last window (the
// stream only goes forward), and ends at the aligned block that holds end.
func (s *Stream) fill(off, end int64) error {
	if s.bufAt >= 0 && off >= s.bufAt && off < s.bufAt+int64(s.bufN) {
		return nil
	}
	at := off &^ (Align - 1)
	if s.bufAt >= 0 && at == s.bufAt+int64(s.bufN) {
		s.window = min(2*s.window, int64(len(s.buf)))
	} else {
		s.window = s.first
	}
	n := min(s.window, (min(end, s.fs.size)-at+Align-1)&^(Align-1))
	if n <= 0 {
		return io.ErrUnexpectedEOF
	}
	got, err := s.dev.ReadAt(s.buf[:n], at)
	s.DevRead += int64(got)
	if got <= int(off-at) {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	s.bufAt, s.bufN = at, got
	return nil
}

// Seek moves the stream forward from where it is (the data of what a
// resumed install did), without reading.
func (s *Stream) Seek(off int64, whence int) (int64, error) {
	if whence != io.SeekCurrent || off < 0 || off > s.f.Size-s.pos {
		return 0, errors.New("squashfs: a stream moves only forward, within the file")
	}
	s.pos += off
	return s.pos, nil
}
