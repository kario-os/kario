package delta

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"unicode/utf16"

	"github.com/kario-os/kario/ota/delta/ext4"
	"github.com/kario-os/kario/ota/verity"
)

// Slot is a slot image's file system and, when known, its descriptor.
type Slot struct {
	Data []byte
	Desc *verity.Descriptor
}

// forward reads a file from start to end, keeping count (a gzip stream
// cannot seek).
type forward struct {
	r   io.Reader
	pos int64
}

func (f *forward) read(n int64) ([]byte, error) {
	b := make([]byte, n)
	k, err := io.ReadFull(f.r, b)
	f.pos += int64(k)
	return b[:k], err
}

func (f *forward) skipTo(at int64) error {
	if at < f.pos {
		return errors.New("partitions overlap")
	}
	k, err := io.CopyN(io.Discard, f.r, at-f.pos)
	f.pos += k
	return err
}

type partition struct {
	name       string
	start, len int64
}

// gptPartitions reads a GPT (512-byte sectors) from the start of a disk.
func gptPartitions(head []byte) ([]partition, bool) {
	if len(head) < 1024 || string(head[512:520]) != "EFI PART" {
		return nil, false
	}
	h := head[512:]
	lba, count, size := binary.LittleEndian.Uint64(h[72:]), binary.LittleEndian.Uint32(h[80:]), binary.LittleEndian.Uint32(h[84:])
	if size < 128 || count > 1024 || lba*512+uint64(count)*uint64(size) > uint64(len(head)) {
		return nil, false
	}
	var parts []partition
	for i := uint64(0); i < uint64(count); i++ {
		e := head[lba*512+i*uint64(size):]
		first, last := binary.LittleEndian.Uint64(e[32:]), binary.LittleEndian.Uint64(e[40:])
		if first == 0 || last < first {
			continue
		}
		name := make([]uint16, 36)
		for j := range name {
			name[j] = binary.LittleEndian.Uint16(e[56+2*j:])
		}
		n := string(utf16.Decode(name))
		parts = append(parts, partition{name: string(bytes.TrimRight([]byte(n), "\x00")), start: int64(first) * 512, len: int64(last-first+1) * 512})
	}
	return parts, true
}

// LoadSlot reads a slot from a slot image (ext4, or ext4 with its hash tree
// appended, as phone-verity.bbclass makes it) or from an A/B disk image
// (GPT: the partition rootfs_<slot> and its descriptor from the state
// partition); either may be gzip-compressed. descPath, when set, is the
// descriptor to use; for a slot image, <path>.desc is used when present.
func LoadSlot(path, descPath, slot string) (*Slot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	in := &forward{r: br}
	if m, _ := br.Peek(2); len(m) == 2 && m[0] == 0x1f && m[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		in.r = bufio.NewReaderSize(gz, 1<<20)
	}
	var desc *verity.Descriptor
	if descPath == "" {
		if _, err := os.Stat(path + ".desc"); err == nil {
			descPath = path + ".desc"
		}
	}
	if descPath != "" {
		raw, err := os.ReadFile(descPath)
		if err != nil {
			return nil, err
		}
		d, err := verity.ParseDescriptor(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", descPath, err)
		}
		desc = &d
	}
	head, err := in.read(1 << 20)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if parts, ok := gptPartitions(head); ok {
		return diskSlot(in, head, parts, slot, desc)
	}
	rest, err := io.ReadAll(in.r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return imageSlot(append(head, rest...), desc)
}

func imageSlot(img []byte, desc *verity.Descriptor) (*Slot, error) {
	size := uint64(0)
	if desc != nil {
		size = desc.DataBlocks * BlockSize
	} else {
		fs, err := ext4.Open(bytes.NewReader(img))
		if err != nil {
			return nil, err
		}
		if fs.BlockSize != BlockSize {
			return nil, fmt.Errorf("the slot's file system has %d-byte blocks, not %d", fs.BlockSize, BlockSize)
		}
		size = fs.Blocks * BlockSize
	}
	if size == 0 || size > uint64(len(img)) {
		return nil, fmt.Errorf("the slot image (%d bytes) is shorter than its file system (%d bytes)", len(img), size)
	}
	return &Slot{Data: img[:size], Desc: desc}, nil
}

func diskSlot(in *forward, head []byte, parts []partition, slot string, desc *verity.Descriptor) (*Slot, error) {
	want := map[string]bool{"rootfs_" + slot: true}
	if desc == nil {
		want["state"] = true
	}
	var need []partition
	for _, p := range parts {
		if want[p.name] {
			need = append(need, p)
			delete(want, p.name)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("the disk has no partition %v", want)
	}
	sort.Slice(need, func(i, j int) bool { return need[i].start < need[j].start })
	var data []byte
	for _, p := range need {
		if p.len > 4<<30 {
			return nil, fmt.Errorf("partition %s of %d bytes", p.name, p.len)
		}
		n := p.len
		if p.name != "state" && desc != nil {
			n = min(n, int64(desc.DataBlocks*BlockSize))
		}
		var b []byte
		if p.start+n <= int64(len(head)) {
			b = head[p.start : p.start+n]
		} else {
			if err := in.skipTo(p.start); err != nil {
				return nil, fmt.Errorf("partition %s: %w", p.name, err)
			}
			var err error
			if b, err = in.read(n); err != nil {
				return nil, fmt.Errorf("partition %s: %w", p.name, err)
			}
		}
		if p.name != "state" {
			data = b
			continue
		}
		fs, err := ext4.Open(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("the state partition: %w", err)
		}
		raw, err := fs.ReadFile("/rootfs_" + slot + ".verity")
		if err != nil {
			return nil, fmt.Errorf("the state partition: %w", err)
		}
		d, err := verity.ParseDescriptor(raw)
		if err != nil {
			return nil, fmt.Errorf("the state partition: %w", err)
		}
		desc = &d
	}
	return imageSlot(data, desc)
}
