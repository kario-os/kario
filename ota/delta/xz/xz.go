// Package xz reads the xz format (tukaani.org/xz/xz-file-format.txt) as the
// xz program writes it with its default filter: one stream, LZMA2 alone,
// any number of blocks, a CRC32, CRC64 or SHA-256 check. Payload data is
// small enough to decode whole, so Decode takes the whole stream and the
// size it must come to. Compress runs the xz program (on the build host).
package xz

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"os/exec"
)

var (
	magic       = []byte{0xFD, '7', 'z', 'X', 'Z', 0}
	footerMagic = []byte{'Y', 'Z'}
	crc64Table  = crc64.MakeTable(crc64.ECMA)
)

const filterLZMA2 = 0x21

// checkSize: the bytes of each check type (the format reserves them all).
func checkSize(id byte) int {
	return [16]int{0, 4, 4, 4, 8, 8, 8, 16, 16, 16, 32, 32, 32, 64, 64, 64}[id]
}

func checkHash(id byte) (hash.Hash, error) {
	switch id {
	case 0:
		return nil, nil
	case 1:
		return crc32.NewIEEE(), nil
	case 4:
		return crc64.New(crc64Table), nil
	case 10:
		return sha256.New(), nil
	}
	return nil, fmt.Errorf("xz: unsupported check type %d", id)
}

func varint(b []byte, p int) (uint64, int, error) {
	var v uint64
	for i := 0; i < 9; i++ {
		if p >= len(b) {
			return 0, 0, errCorrupt
		}
		c := b[p]
		p++
		v |= uint64(c&0x7F) << (7 * i)
		if c&0x80 == 0 {
			if c == 0 && i > 0 {
				return 0, 0, errCorrupt
			}
			return v, p, nil
		}
	}
	return 0, 0, errCorrupt
}

func crcOK(data, sum []byte) bool {
	return crc32.ChecksumIEEE(data) == binary.LittleEndian.Uint32(sum)
}

type record struct{ unpadded, size uint64 }

// Decode decompresses an xz stream that must come to exactly size bytes.
func Decode(in []byte, size int) ([]byte, error) {
	if len(in) < 12 || !bytes.Equal(in[:6], magic) {
		return nil, errors.New("xz: not an xz stream")
	}
	flags := in[6:8]
	if flags[0] != 0 || flags[1]&0xF0 != 0 || !crcOK(flags, in[8:12]) {
		return nil, errors.New("xz: bad stream header")
	}
	check := flags[1]
	o := &out{b: make([]byte, 0, size), limit: size}
	var blocks []record
	p := 12
	for {
		if p >= len(in) {
			return nil, errCorrupt
		}
		if in[p] == 0 {
			break
		}
		n, err := block(in, p, check, o)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, n)
		p += int(n.unpadded+3) &^ 3
	}
	p, err := index(in, p, blocks)
	if err != nil {
		return nil, err
	}
	if p+12 > len(in) || !crcOK(in[p+4:p+10], in[p:p+4]) || !bytes.Equal(in[p+8:p+10], flags) || !bytes.Equal(in[p+10:p+12], footerMagic) {
		return nil, errors.New("xz: bad stream footer")
	}
	p += 12
	if (len(in)-p)%4 != 0 || !bytes.Equal(in[p:], make([]byte, len(in)-p)) {
		return nil, errors.New("xz: data after the stream")
	}
	if len(o.b) != size {
		return nil, fmt.Errorf("xz: %d bytes, %d expected", len(o.b), size)
	}
	return o.b, nil
}

// block decodes the block at in[p:] and returns its index record.
func block(in []byte, p int, check byte, o *out) (record, error) {
	var rec record
	hsize := (int(in[p]) + 1) * 4
	if p+hsize > len(in) {
		return rec, errCorrupt
	}
	h := in[p : p+hsize]
	if !crcOK(h[:hsize-4], h[hsize-4:]) {
		return rec, errors.New("xz: bad block header")
	}
	bflags := h[1]
	if bflags&0x3C != 0 || bflags&3 != 0 {
		return rec, errors.New("xz: only LZMA2 alone is supported")
	}
	q := 2
	compressed, size := ^uint64(0), ^uint64(0)
	var err error
	if bflags&0x40 != 0 {
		if compressed, q, err = varint(h, q); err != nil {
			return rec, err
		}
	}
	if bflags&0x80 != 0 {
		if size, q, err = varint(h, q); err != nil {
			return rec, err
		}
	}
	id, q, err := varint(h, q)
	if err != nil {
		return rec, err
	}
	psize, q, err := varint(h, q)
	if err != nil {
		return rec, err
	}
	if id != filterLZMA2 || psize != 1 || q >= hsize-4 || h[q] > 40 {
		return rec, errors.New("xz: only LZMA2 alone is supported")
	}
	if !bytes.Equal(h[q+1:hsize-4], make([]byte, hsize-4-q-1)) {
		return rec, errCorrupt
	}
	start := len(o.b)
	n, err := lzma2(in[p+hsize:], o)
	if err != nil {
		return rec, err
	}
	produced := uint64(len(o.b) - start)
	if (compressed != ^uint64(0) && compressed != uint64(n)) || (size != ^uint64(0) && size != produced) {
		return rec, errCorrupt
	}
	c := p + hsize + n
	for ; (c-p)%4 != 0; c++ {
		if c >= len(in) || in[c] != 0 {
			return rec, errCorrupt
		}
	}
	cs := checkSize(check)
	if c+cs > len(in) {
		return rec, errCorrupt
	}
	sum, err := checkHash(check)
	if err != nil {
		return rec, err
	}
	if sum != nil {
		sum.Write(o.b[start:])
		got := sum.Sum(nil)
		if check != 10 {
			for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
				got[i], got[j] = got[j], got[i]
			}
		}
		if !bytes.Equal(got, in[c:c+cs]) {
			return rec, errors.New("xz: check failed")
		}
	}
	return record{unpadded: uint64(hsize + n + cs), size: produced}, nil
}

// index checks the stream index against the blocks decoded and returns
// where the footer starts.
func index(in []byte, p int, blocks []record) (int, error) {
	start := p
	p++
	count, p, err := varint(in, p)
	if err != nil || count != uint64(len(blocks)) {
		return 0, errors.New("xz: bad index")
	}
	for _, b := range blocks {
		var unpadded, size uint64
		if unpadded, p, err = varint(in, p); err != nil {
			return 0, err
		}
		if size, p, err = varint(in, p); err != nil {
			return 0, err
		}
		if unpadded != b.unpadded || size != b.size {
			return 0, errors.New("xz: index does not match the blocks")
		}
	}
	for ; (p-start)%4 != 0; p++ {
		if p >= len(in) || in[p] != 0 {
			return 0, errCorrupt
		}
	}
	if p+4 > len(in) || !crcOK(in[start:p], in[p:p+4]) {
		return 0, errors.New("xz: bad index")
	}
	p += 4
	if p+8 > len(in) || uint64(binary.LittleEndian.Uint32(in[p+4:p+8])+1)*4 != uint64(p-start) {
		return 0, errors.New("xz: footer does not match the index")
	}
	return p, nil
}

// Compress runs the xz program: one block, a CRC32, the strongest preset
// with a dictionary as big as the data (the default 64 MiB costs time and
// memory for nothing on small data).
func Compress(data []byte) ([]byte, error) {
	dict := 1 << 16
	for dict < len(data) && dict < 64<<20 {
		dict <<= 1
	}
	cmd := exec.Command("xz", "--format=xz", "--check=crc32", "--threads=1", "--stdout", "--quiet",
		fmt.Sprintf("--lzma2=preset=9e,dict=%d", dict))
	cmd.Stdin = bytes.NewReader(data)
	var outb, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outb, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("xz: %v: %s", err, bytes.TrimSpace(errb.Bytes()))
	}
	return outb.Bytes(), nil
}
