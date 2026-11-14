package dsd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// DSFInfo is a DSF file's header (Sony's DSD Stream File, little-endian):
// the samples sit in blocks of BlockSize bytes, one channel after the
// other, usually earliest sample in the least significant bit.
type DSFInfo struct {
	Rate, Channels int
	ChannelType    int   // 1 mono, 2 stereo, … 7 5.1
	Samples        int64 // per channel
	BlockSize      int
	LSBFirst       bool
	Data           int64 // offset of the first block
	DataBytes      int64
	Meta           int64 // offset of the ID3v2 tag, 0 without one
	Size           int64 // of the file, by its header
}

// ReadDSF reads the header.
func ReadDSF(r io.ReadSeeker) (DSFInfo, error) {
	var d DSFInfo
	var h [28 + 52 + 12]byte
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return d, err
	}
	if _, err := io.ReadFull(r, h[:]); err != nil || string(h[0:4]) != "DSD " || string(h[28:32]) != "fmt " {
		return d, errors.New("not a DSF file")
	}
	le := binary.LittleEndian
	d.Size = int64(le.Uint64(h[12:]))
	d.Meta = int64(le.Uint64(h[20:]))
	fmtSize := int64(le.Uint64(h[32:]))
	f := h[40:]
	if le.Uint32(f[4:]) != 0 {
		return d, errors.New("DSF: not raw DSD")
	}
	d.ChannelType = int(le.Uint32(f[8:]))
	d.Channels = int(le.Uint32(f[12:]))
	d.Rate = int(le.Uint32(f[16:]))
	bits := le.Uint32(f[20:])
	d.Samples = int64(le.Uint64(f[24:]))
	d.BlockSize = int(le.Uint32(f[32:]))
	d.LSBFirst = bits == 1
	if bits != 1 && bits != 8 || d.Channels < 1 || d.Channels > 6 || d.Rate < Base || d.BlockSize <= 0 || d.BlockSize > 1<<20 {
		return d, fmt.Errorf("DSF: %d channels at %d Hz, %d bits, blocks of %d", d.Channels, d.Rate, bits, d.BlockSize)
	}
	dataAt := 28 + fmtSize
	var dh [12]byte
	if _, err := r.Seek(dataAt, io.SeekStart); err != nil {
		return d, err
	}
	if _, err := io.ReadFull(r, dh[:]); err != nil || string(dh[:4]) != "data" {
		return d, errors.New("DSF: no data chunk")
	}
	d.Data = dataAt + 12
	d.DataBytes = int64(le.Uint64(dh[4:])) - 12
	return d, nil
}

// DSF is a DSF file as a Stream.
type DSF struct {
	Info  DSFInfo
	f     *os.File
	group []byte // one block of every channel
	next  int64  // byte frame Read returns next
	have  int64  // block index in group, -1 for none
}

// OpenDSF opens a DSF file.
func OpenDSF(path string) (*DSF, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := ReadDSF(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &DSF{Info: info, f: f, group: make([]byte, info.BlockSize*info.Channels), have: -1}, nil
}

func (d *DSF) Rate() int     { return d.Info.Rate }
func (d *DSF) Channels() int { return d.Info.Channels }
func (d *DSF) Len() int64    { return d.Info.Samples / 8 }
func (d *DSF) Close() error  { return d.f.Close() }

func (d *DSF) SeekFrame(n int64) error {
	if n < 0 || n > d.Len() {
		return fmt.Errorf("DSF: seek to %d of %d", n, d.Len())
	}
	d.next = n
	return nil
}

func (d *DSF) Read(p []byte) (int, error) {
	ch, bs := d.Info.Channels, int64(d.Info.BlockSize)
	n := 0
	for n+ch <= len(p) {
		if d.next >= d.Len() {
			if n == 0 {
				return 0, io.EOF
			}
			break
		}
		blk := d.next / bs
		if blk != d.have {
			if _, err := d.f.ReadAt(d.group, d.Info.Data+blk*bs*int64(ch)); err != nil && err != io.EOF {
				return n, err
			}
			if d.Info.LSBFirst {
				for i, b := range d.group {
					d.group[i] = reverse[b]
				}
			}
			d.have = blk
		}
		in := d.next - blk*bs
		k := min(bs-in, int64((len(p)-n)/ch), d.Len()-d.next)
		for i := int64(0); i < k; i++ {
			for c := 0; c < ch; c++ {
				p[n] = d.group[int64(c)*bs+in+i]
				n++
			}
		}
		d.next += k
	}
	return n, nil
}
