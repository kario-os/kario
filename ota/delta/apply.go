package delta

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/kario-os/kario/ota/delta/bsdiff"
	"github.com/kario-os/kario/ota/delta/xz"
	"github.com/kario-os/kario/ota/verity"
)

// Device is where a slot is written: a partition, or a file in tests.
type Device interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
}

// ApplyConfig is what a payload is applied with.
type ApplyConfig struct {
	// Source is the running slot, on a phone read through its dm-verity
	// device so that every block read is verified; SourceSize its bytes.
	Source     io.ReaderAt
	SourceSize int64
	// SourceDesc is the running slot's descriptor. With it, and a root hash
	// in the manifest, the two root hashes must agree; otherwise the source
	// is hashed whole against the manifest.
	SourceDesc *verity.Descriptor
	// Target is the slot written, TargetSize its bytes.
	Target     Device
	TargetSize int64
	// TargetDesc is the target's signed descriptor: the manifest must
	// describe the same, and the slot written must come to its root hash.
	TargetDesc verity.Descriptor
	Log        func(format string, args ...any)
}

// Result is what an application did.
type Result struct {
	Ops      int
	Data     uint64 // payload bytes after the manifest
	Written  uint64 // slot bytes written, the hash tree included
	Duration time.Duration
}

func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Apply rebuilds the target slot from the source slot and the payload, read
// once from start to end. On an error the target holds no usable slot;
// nothing here marks a slot for boot.
func Apply(payload io.Reader, c ApplyConfig) (*Result, error) {
	start := time.Now()
	logf := c.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	m, err := ReadManifest(payload)
	if err != nil {
		return nil, err
	}
	want, _ := m.Descriptor()
	if !want.Equal(c.TargetDesc) {
		return nil, errors.New("delta: the payload is not for the slot image the signed descriptor describes")
	}
	tree := verity.TreeSize(want.DataBlocks)
	if c.TargetSize < int64(want.HashOffset+tree) {
		return nil, fmt.Errorf("delta: the target (%d bytes) is too small for the slot (%d bytes)", c.TargetSize, want.HashOffset+tree)
	}
	if c.SourceSize < int64(m.Source.Size) {
		return nil, errors.New("delta: the payload is for another system version than the one running (the slot is smaller)")
	}
	if err := checkSource(m, c); err != nil {
		return nil, err
	}
	logf("applying %d operations (%d bytes of data) from %s to %s", len(m.Ops), m.DataSize(), orUnknown(m.Source.Version), orUnknown(m.Target.Version))
	res := &Result{Ops: len(m.Ops), Data: m.DataSize()}
	for i, op := range m.Ops {
		if err := applyOp(op, payload, c); err != nil {
			return nil, fmt.Errorf("delta: operation %d (%s): %w", i, op.Type, err)
		}
		res.Written += blocks(op.Dst) * BlockSize
	}
	if err := c.Target.Sync(); err != nil {
		return nil, fmt.Errorf("delta: %w", err)
	}
	// Read back from the device, not the page cache, what the tree is built over.
	dropCache(c.Target)
	h, whole := verity.NewHasher(want.Salt, want.DataBlocks), sha256.New()
	buf := make([]byte, 1<<20)
	for off := uint64(0); off < m.Target.Size; {
		n := min(uint64(len(buf)), m.Target.Size-off)
		if _, err := c.Target.ReadAt(buf[:n], int64(off)); err != nil {
			return nil, fmt.Errorf("delta: reading the slot back: %w", err)
		}
		_, _ = h.Write(buf[:n])
		whole.Write(buf[:n])
		off += n
	}
	if hexSum(whole) != m.Target.SHA256 {
		return nil, errors.New("delta: the slot written is not the target image (SHA-256)")
	}
	t, root, err := h.Tree()
	if err != nil {
		return nil, fmt.Errorf("delta: %w", err)
	}
	if root != want.RootHash {
		return nil, errors.New("delta: the slot's verity root hash is not the signed one")
	}
	if _, err := c.Target.WriteAt(t, int64(want.HashOffset)); err != nil {
		return nil, fmt.Errorf("delta: writing the hash tree: %w", err)
	}
	if err := c.Target.Sync(); err != nil {
		return nil, fmt.Errorf("delta: %w", err)
	}
	res.Written += uint64(len(t))
	res.Duration = time.Since(start)
	logf("slot written: %d bytes in %s, root hash %x", res.Written, res.Duration.Round(time.Millisecond), root)
	return res, nil
}

func orUnknown(v string) string {
	if v == "" {
		return "(unnamed)"
	}
	return v
}

func checkSource(m *Manifest, c ApplyConfig) error {
	wrong := errors.New("delta: the payload is for another system version than the one running")
	if c.SourceDesc != nil && m.Source.RootHash != "" {
		if hex.EncodeToString(c.SourceDesc.RootHash[:]) != m.Source.RootHash || c.SourceDesc.DataBlocks*BlockSize != m.Source.Size {
			return wrong
		}
		return nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(c.Source, 0, int64(m.Source.Size))); err != nil {
		return fmt.Errorf("delta: reading the source: %w", err)
	}
	if hexSum(h) != m.Source.SHA256 {
		return wrong
	}
	return nil
}

func readExtents(r io.ReaderAt, ext []Extent) ([]byte, error) {
	b := make([]byte, blocks(ext)*BlockSize)
	at := 0
	for _, e := range ext {
		n := int(e[1] * BlockSize)
		if _, err := r.ReadAt(b[at:at+n], int64(e[0]*BlockSize)); err != nil {
			return nil, err
		}
		at += n
	}
	return b, nil
}

// extentWriter writes a stream over a list of extents.
type extentWriter struct {
	dev io.WriterAt
	ext []Extent
	off uint64 // into ext[0]
}

func (w *extentWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		if len(w.ext) == 0 {
			return n, errors.New("more data than its destination")
		}
		e := w.ext[0]
		k := min(uint64(len(p)), e[1]*BlockSize-w.off)
		if _, err := w.dev.WriteAt(p[:k], int64(e[0]*BlockSize+w.off)); err != nil {
			return n, err
		}
		n += int(k)
		p = p[k:]
		if w.off += k; w.off == e[1]*BlockSize {
			w.ext, w.off = w.ext[1:], 0
		}
	}
	return n, nil
}

func applyOp(op Op, payload io.Reader, c ApplyConfig) error {
	var data []byte
	if op.DataLength > 0 {
		data = make([]byte, op.DataLength)
		if _, err := io.ReadFull(payload, data); err != nil {
			return fmt.Errorf("reading its data: %w", err)
		}
		if sum(data) != op.DataSHA256 {
			return errors.New("its data does not match its hash")
		}
	}
	var src []byte
	if len(op.Src) > 0 {
		var err error
		if src, err = readExtents(c.Source, op.Src); err != nil {
			return fmt.Errorf("reading the source: %w", err)
		}
		if sum(src) != op.SrcSHA256 {
			return errors.New("the source blocks do not match their hash")
		}
	}
	size := blocks(op.Dst) * BlockSize
	dst := &extentWriter{dev: c.Target, ext: op.Dst}
	h := sha256.New()
	out := io.MultiWriter(dst, h)
	var err error
	switch op.Type {
	case Replace:
		if op.Compression == CompressXZ {
			if data, err = xz.Decode(data, int(size)); err != nil {
				return err
			}
		}
		_, err = out.Write(data)
	case SourceCopy:
		_, err = out.Write(src)
	case SourceBsdiff:
		if n, perr := bsdiff.Size(data); perr != nil || uint64(n) != size {
			return errors.New("the patch is not for its destination")
		}
		err = bsdiff.Apply(src, data, out)
	case Zero:
		zero := make([]byte, min(size, 1<<20))
		for left := size; left > 0 && err == nil; left -= min(left, uint64(len(zero))) {
			_, err = out.Write(zero[:min(left, uint64(len(zero)))])
		}
	}
	if err != nil {
		return err
	}
	if len(dst.ext) != 0 {
		return errors.New("its result is shorter than its destination")
	}
	if hexSum(h) != op.DstSHA256 {
		return errors.New("its result does not match its hash")
	}
	return nil
}

// memDevice is a slot in memory (the generator's own check of a payload).
type memDevice struct{ b []byte }

func (d *memDevice) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(d.b)) {
		return 0, io.EOF
	}
	return copy(p, d.b[off:]), nil
}

func (d *memDevice) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(d.b)) {
		return 0, errors.New("write outside the device")
	}
	return copy(d.b[off:], p), nil
}

func (d *memDevice) Sync() error { return nil }
