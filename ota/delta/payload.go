// Package delta is incremental system updates in the manner of Android's
// A/B updates (update_engine payloads, made by delta_generator), for the
// slots of 25-system-update.md: a payload rebuilds one target slot image
// from one source slot image, operation by operation, each with the
// SHA-256 of what it reads and of what it writes; the phone recomputes the
// target's dm-verity hash tree and the root hash must be the one in the
// target's signed descriptor before the slot may boot.
//
// A payload is a header ("KDLT", the format as a big-endian uint32, the
// manifest's length as a big-endian uint64), the manifest (JSON) and the
// operations' data, one after another in the manifest's order, so that a
// payload applies as it streams in.
package delta

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/kario-os/kario/ota/delta/bsdiff"
	"github.com/kario-os/kario/ota/verity"
)

// Formats are the payload formats this package reads, Format among them
// (phone-ota-delta can-apply: the install hook asks a system's applier
// before it uses the bundle's, 25-system-update.md §5.3). Format 2 adds
// patches in pieces (Op.Pieces).
var Formats = []int{1, Format}

// PayloadFormat is the format a payload's header gives, read without the
// rest of it.
func PayloadFormat(r io.Reader) (int, error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, fmt.Errorf("delta: reading the header: %w", err)
	}
	if string(h[:4]) != magic {
		return 0, errors.New("delta: not a payload")
	}
	return int(binary.BigEndian.Uint32(h[4:])), nil
}

const (
	magic = "KDLT"
	// Format is the payload format this package writes by default.
	Format = 2
	// BlockSize is the unit of every extent: the slots' 4 KiB blocks.
	BlockSize = verity.BlockSize

	maxManifest = 64 << 20
	// maxOpBlocks bounds what one operation holds in memory on the phone.
	maxOpBlocks = 1 << 16
	// maxImageBlocks: 64 GiB, far above any slot.
	maxImageBlocks = 1 << 24
	// maxOpData bounds one operation's data; maxPayloadData is the most a
	// payload Check accepts holds after its manifest: every operation with
	// data writes a target block of its own.
	maxOpData      = 1 << 30
	maxPayloadData = maxImageBlocks * maxOpData
)

// The operation types, as update_engine names them.
const (
	// Replace writes the operation's data (compressed or not).
	Replace = "REPLACE"
	// SourceCopy copies blocks of the source slot.
	SourceCopy = "SOURCE_COPY"
	// SourceBsdiff patches blocks of the source slot (package bsdiff).
	SourceBsdiff = "SOURCE_BSDIFF"
	// Zero writes zeros.
	Zero = "ZERO"
)

// Compression of a Replace operation's data.
const (
	CompressNone = "none"
	CompressXZ   = "xz"
)

// Extent is a run of blocks: the first block and the count.
type Extent [2]uint64

// Op is one operation. Src and Dst are read and written in order, as one
// stream each.
type Op struct {
	Type        string   `json:"type"`
	Src         []Extent `json:"src,omitempty"`
	Dst         []Extent `json:"dst"`
	DataOffset  uint64   `json:"data_offset,omitempty"`
	DataLength  uint64   `json:"data_length,omitempty"`
	Compression string   `json:"compression,omitempty"`
	DataSHA256  string   `json:"data_sha256,omitempty"`
	SrcSHA256   string   `json:"src_sha256,omitempty"`
	DstSHA256   string   `json:"dst_sha256"`
	// Piece and Pieces (format 2, SourceBsdiff only): the patch in pieces
	// (bsdiff.Pieces) that each make Piece bytes of the destination, the
	// last fewer, one after another in the data, each with its length and
	// SHA-256. Each piece is checked before it is used and applied as it
	// arrives; an install may stop and resume between two.
	Piece  uint64  `json:"piece,omitempty"`
	Pieces []Piece `json:"pieces,omitempty"`
}

// Piece is one piece of an operation's data.
type Piece struct {
	Length uint64 `json:"length"`
	SHA256 string `json:"sha256"`
}

// pieceShare is the destination bytes piece k of op makes.
func pieceShare(op Op, k int) uint64 {
	return min(op.Piece, blocks(op.Dst)*BlockSize-uint64(k)*op.Piece)
}

// Image is a slot image's file system: the data dm-verity covers.
type Image struct {
	Version string `json:"version,omitempty"`
	Size    uint64 `json:"size"`
	SHA256  string `json:"sha256"`
	// RootHash: the source's verity root hash, which a phone compares with
	// the descriptor of the slot it runs instead of hashing the slot.
	RootHash string `json:"root_hash,omitempty"`
}

// Verity is the target's verity descriptor (verity.Descriptor in JSON).
type Verity struct {
	DataBlocks uint64 `json:"data_blocks"`
	HashOffset uint64 `json:"hash_offset"`
	Salt       string `json:"salt"`
	RootHash   string `json:"root_hash"`
}

// Manifest describes a payload.
type Manifest struct {
	Format    int    `json:"format"`
	BlockSize int    `json:"block_size"`
	Source    Image  `json:"source"`
	Target    Image  `json:"target"`
	Verity    Verity `json:"verity"`
	Ops       []Op   `json:"operations"`
}

// VerityOf puts a descriptor in the manifest's form.
func VerityOf(d verity.Descriptor) Verity {
	return Verity{DataBlocks: d.DataBlocks, HashOffset: d.HashOffset, Salt: hex.EncodeToString(d.Salt), RootHash: hex.EncodeToString(d.RootHash[:])}
}

// Descriptor is the target's descriptor as the manifest has it.
func (m *Manifest) Descriptor() (verity.Descriptor, error) {
	v := m.Verity
	text := fmt.Sprintf("data_blocks=%d\nhash_offset=%d\nsalt=%s\nroot_hash=%s\n", v.DataBlocks, v.HashOffset, v.Salt, v.RootHash)
	return verity.ParseDescriptor([]byte(text))
}

func blocks(ext []Extent) uint64 {
	var n uint64
	for _, e := range ext {
		n += e[1]
	}
	return n
}

func isHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}

// Check validates a manifest before anything is written: known
// operations, every extent inside its image, every target block written by
// exactly one operation, the data laid out in order, every hash present.
func (m *Manifest) Check() error {
	if !slices.Contains(Formats, m.Format) || m.BlockSize != BlockSize {
		return fmt.Errorf("payload: format %d with %d-byte blocks, this reads format %v", m.Format, m.BlockSize, Formats)
	}
	desc, err := m.Descriptor()
	if err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	switch {
	case m.Source.Size == 0 || m.Source.Size%BlockSize != 0 || !isHash(m.Source.SHA256):
		return errors.New("payload: bad source image")
	case m.Source.RootHash != "" && !isHash(m.Source.RootHash):
		return errors.New("payload: bad source root hash")
	case m.Target.Size != desc.DataBlocks*BlockSize || !isHash(m.Target.SHA256):
		return errors.New("payload: the target image does not match its verity descriptor")
	}
	srcBlocks, dstBlocks := m.Source.Size/BlockSize, desc.DataBlocks
	if srcBlocks > maxImageBlocks || dstBlocks > maxImageBlocks {
		return errors.New("payload: image too large")
	}
	written := make([]bool, dstBlocks)
	var data uint64
	for i, op := range m.Ops {
		bad := func(why string) error { return fmt.Errorf("payload: operation %d (%s): %s", i, op.Type, why) }
		n := blocks(op.Dst)
		if n == 0 || n > maxOpBlocks || !isHash(op.DstSHA256) {
			return bad("bad destination")
		}
		for _, e := range op.Dst {
			if e[1] == 0 || e[0] >= dstBlocks || e[1] > dstBlocks-e[0] {
				return bad("a destination extent outside the target")
			}
			for b := e[0]; b < e[0]+e[1]; b++ {
				if written[b] {
					return bad(fmt.Sprintf("block %d written twice", b))
				}
				written[b] = true
			}
		}
		hasSrc, hasData := op.Type == SourceCopy || op.Type == SourceBsdiff, op.Type == Replace || op.Type == SourceBsdiff
		switch op.Type {
		case Replace:
			if op.Compression != CompressNone && op.Compression != CompressXZ {
				return bad("unknown compression")
			}
			if op.Compression == CompressNone && op.DataLength != n*BlockSize {
				return bad("stored data of the wrong length")
			}
		case SourceCopy, SourceBsdiff, Zero:
		default:
			return bad("unknown operation")
		}
		if hasSrc {
			sn := blocks(op.Src)
			if sn == 0 || sn > maxOpBlocks || !isHash(op.SrcSHA256) || (op.Type == SourceCopy && sn != n) {
				return bad("bad source")
			}
			for _, e := range op.Src {
				if e[1] == 0 || e[0] >= srcBlocks || e[1] > srcBlocks-e[0] {
					return bad("a source extent outside the source")
				}
			}
		} else if len(op.Src) > 0 || op.SrcSHA256 != "" {
			return bad("a source it does not read")
		}
		if hasData {
			if op.DataLength == 0 || op.DataLength > maxOpData || op.DataOffset != data || !isHash(op.DataSHA256) {
				return bad("bad data")
			}
			data += op.DataLength
		} else if op.DataLength != 0 || op.DataOffset != 0 || op.DataSHA256 != "" || op.Compression != "" {
			return bad("data it does not use")
		}
		if err := checkPieces(m.Format, op, n); err != "" {
			return bad(err)
		}
	}
	for b, ok := range written {
		if !ok {
			return fmt.Errorf("payload: no operation writes target block %d", b)
		}
	}
	return nil
}

// checkPieces: an operation in pieces is a patch of format 2 whose pieces
// each make Piece bytes (whole blocks, at most bsdiff.MaxPiece) and add up
// to its data.
func checkPieces(format int, op Op, dstBlocks uint64) string {
	if op.Piece == 0 && len(op.Pieces) == 0 {
		return ""
	}
	if format < 2 || op.Type != SourceBsdiff || op.Piece%BlockSize != 0 || op.Piece == 0 || op.Piece > bsdiff.MaxPiece {
		return "bad pieces"
	}
	if uint64(len(op.Pieces)) != (dstBlocks*BlockSize+op.Piece-1)/op.Piece {
		return "pieces that do not cover its destination"
	}
	// A piece holds its streams stored at worst (bsdiff's bounds on them).
	maxPiece := 4*op.Piece + 2<<20
	var total uint64
	for _, p := range op.Pieces {
		if p.Length == 0 || p.Length > maxPiece || !isHash(p.SHA256) {
			return "a bad piece"
		}
		total += p.Length
	}
	if total != op.DataLength {
		return "pieces that do not add up to its data"
	}
	return ""
}

// DataSize is the bytes of operation data after the manifest.
func (m *Manifest) DataSize() uint64 {
	var n uint64
	for _, op := range m.Ops {
		n += op.DataLength
	}
	return n
}

// WriteHeader writes the header and the manifest.
func WriteHeader(w io.Writer, m *Manifest) error {
	js, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(js) > maxManifest {
		return errors.New("payload: manifest too large")
	}
	if !slices.Contains(Formats, m.Format) {
		return fmt.Errorf("payload: format %d", m.Format)
	}
	h := make([]byte, 16, 16+len(js))
	copy(h, magic)
	binary.BigEndian.PutUint32(h[4:], uint32(m.Format))
	binary.BigEndian.PutUint64(h[8:], uint64(len(js)))
	_, err = w.Write(append(h, js...))
	return err
}

// ReadManifest reads the header and the manifest, and checks it.
func ReadManifest(r io.Reader) (*Manifest, error) {
	h := make([]byte, 16)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if string(h[:4]) != magic {
		return nil, errors.New("payload: not an incremental update payload")
	}
	f := binary.BigEndian.Uint32(h[4:])
	if !slices.Contains(Formats, int(f)) {
		return nil, fmt.Errorf("payload: format %d, this reads format %v", f, Formats)
	}
	n := binary.BigEndian.Uint64(h[8:])
	if n > maxManifest {
		return nil, errors.New("payload: manifest too large")
	}
	js := make([]byte, n)
	if _, err := io.ReadFull(r, js); err != nil {
		return nil, fmt.Errorf("payload: manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(js, &m); err != nil {
		return nil, fmt.Errorf("payload: manifest: %w", err)
	}
	if m.Format != int(f) {
		return nil, fmt.Errorf("payload: format %d in the header, %d in the manifest", f, m.Format)
	}
	return &m, m.Check()
}
