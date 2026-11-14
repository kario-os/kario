package delta

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"path"
	"runtime"
	"sort"
	"sync"

	"github.com/kario-os/kario/ota/delta/bsdiff"
	"github.com/kario-os/kario/ota/delta/ext4"
	"github.com/kario-os/kario/ota/delta/xz"
	"github.com/kario-os/kario/ota/verity"
)

// GenerateConfig is what a payload is made from.
type GenerateConfig struct {
	Source, Target               *Slot // the target's descriptor is required
	SourceVersion, TargetVersion string
	// Compress compresses Replace data and patch streams into xz
	// (xz.Compress when nil).
	Compress func([]byte) ([]byte, error)
	Workers  int
	// MaxDiff: a file bigger than this, old or new, is not diffed (the phone
	// holds both and the patch while it applies the operation). 0: 64 MiB.
	MaxDiff int64
	// NoVerify skips applying the payload to the source in memory before
	// it is written out.
	NoVerify bool
	Log      func(format string, args ...any)
}

// Report is what a payload holds, against the full image and the adaptive
// install of the full bundle.
type Report struct {
	Source  Image              `json:"source"`
	Target  Image              `json:"target"`
	Payload int64              `json:"payload_bytes"`
	Ops     map[string]*OpStat `json:"operations"`
	// Adaptive estimates RAUC's adaptive install of the full bundle on a
	// phone whose slots both hold the source.
	Adaptive Adaptive `json:"adaptive"`
	// Files are the files that changed, the most payload first.
	Files []FileStat `json:"files"`
}

// OpStat counts one type of operation.
type OpStat struct {
	Count  int    `json:"count"`
	Blocks uint64 `json:"blocks"`
	Data   uint64 `json:"data_bytes"`
}

// Adaptive: the target blocks found nowhere in the source, the
// block-hash-index (a SHA-256 per block, downloaded whole), and the
// estimate: the index plus each 128 KiB bundle block holding a missing
// block, gzip-compressed as mksquashfs stores it.
type Adaptive struct {
	MissingBlocks int   `json:"missing_blocks"`
	MissingBytes  int64 `json:"missing_bytes"`
	IndexBytes    int64 `json:"index_bytes"`
	Estimate      int64 `json:"estimate_bytes"`
}

// FileStat is a changed file: how it is sent and what it costs (for files
// sent with metadata in shared operations, an estimate; -1 not estimated).
type FileStat struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Method string `json:"method"`
	Bytes  int64  `json:"bytes"`
	From   string `json:"from,omitempty"`
}

const (
	kindNew = iota
	kindZero
	kindCopy
	kindDiff // in a SourceBsdiff operation

	chunkBlocks = 1024 // 4 MiB: Replace and SourceCopy operations at most
	zeroBlocks  = 8192
	smallFile   = 64 << 10
	squashBlock = 128 << 10
)

type gen struct {
	c        GenerateConfig
	src, dst []byte
	kind     []uint8
	from     []uint32
	// missing: found nowhere in the source (what an adaptive install
	// downloads).
	missing  []bool
	compress func([]byte) ([]byte, error)
}

func (g *gen) sblock(i uint32) []byte { return g.src[int(i)*BlockSize : (int(i)+1)*BlockSize] }
func (g *gen) tblock(i uint32) []byte { return g.dst[int(i)*BlockSize : (int(i)+1)*BlockSize] }

func gather(img []byte, list []uint32) []byte {
	b := make([]byte, 0, len(list)*BlockSize)
	for _, i := range list {
		b = append(b, img[int(i)*BlockSize:(int(i)+1)*BlockSize]...)
	}
	return b
}

// toExtents merges a list of blocks into runs, in order.
func toExtents(list []uint32) []Extent {
	var out []Extent
	for _, b := range list {
		if k := len(out); k > 0 && out[k-1][0]+out[k-1][1] == uint64(b) {
			out[k-1][1]++
			continue
		}
		out = append(out, Extent{uint64(b), 1})
	}
	return out
}

func fileBlocks(f ext4.File) []uint32 {
	var list []uint32
	for _, e := range f.Extents {
		for i := uint64(0); i < e.Len; i++ {
			list = append(list, uint32(e.Physical+i))
		}
	}
	return list
}

// job is a changed target file: how to send its blocks not in the source.
type job struct {
	path, from string
	size       int64
	blocks     []uint32 // all its blocks, in file order
	fresh      []uint32 // those found nowhere in the source
	src        []uint32 // the source file's blocks, to diff against
	method     string   // "bsdiff", "replace" or "shared"
	patch      []byte
	ops        []pending
	cost       int64
}

// pending is an operation whose data is ready.
type pending struct {
	op   Op
	data []byte
}

// replaceOps compresses blocks into Replace operations of chunkBlocks.
func (g *gen) replaceOps(list []uint32) ([]pending, int64, error) {
	var out []pending
	var cost int64
	for len(list) > 0 {
		n := min(len(list), chunkBlocks)
		raw := gather(g.dst, list[:n])
		p := pending{op: Op{Type: Replace, Dst: toExtents(list[:n]), Compression: CompressNone}, data: raw}
		c, err := g.compress(raw)
		if err != nil {
			return nil, 0, err
		}
		if len(c) < len(raw) {
			p.op.Compression, p.data = CompressXZ, c
		}
		out = append(out, p)
		cost += int64(len(p.data))
		list = list[n:]
	}
	return out, cost, nil
}

func (g *gen) evaluate(j *job, maxDiff int64) error {
	canDiff := j.src != nil && j.size <= maxDiff && int64(len(j.src))*BlockSize <= maxDiff
	small := len(j.fresh)*BlockSize < smallFile
	j.cost = -1
	if !small || canDiff {
		ops, cost, err := g.replaceOps(j.fresh)
		if err != nil {
			return err
		}
		j.ops, j.cost = ops, cost
	}
	if canDiff {
		patch, err := bsdiff.Make(gather(g.src, j.src), gather(g.dst, j.blocks)).Encode(g.compress)
		if err != nil {
			return err
		}
		if int64(len(patch)) < j.cost {
			j.method, j.patch, j.cost, j.ops = "bsdiff", patch, int64(len(patch)), nil
			return nil
		}
	}
	j.method = "replace"
	if small {
		j.method, j.ops = "shared", nil
	}
	return nil
}

func isELF(b []byte) bool { return len(b) >= 4 && string(b[:4]) == "\x7fELF" }

// Generate makes the payload from the source slot to the target slot and
// writes it to w.
func Generate(c GenerateConfig, w io.Writer) (*Report, error) {
	if c.Target == nil || c.Target.Desc == nil || c.Source == nil {
		return nil, errors.New("delta: a source, and a target with its verity descriptor")
	}
	desc := *c.Target.Desc
	if uint64(len(c.Target.Data)) != desc.DataBlocks*BlockSize || len(c.Source.Data)%BlockSize != 0 || len(c.Source.Data) == 0 {
		return nil, errors.New("delta: the images are not whole blocks, or the target is not its descriptor's size")
	}
	logf := c.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	g := &gen{c: c, src: c.Source.Data, dst: c.Target.Data, compress: c.Compress}
	if g.compress == nil {
		g.compress = xz.Compress
	}
	workers := c.Workers
	if workers <= 0 {
		workers = min(runtime.NumCPU(), 8)
	}
	maxDiff := c.MaxDiff
	if maxDiff <= 0 {
		maxDiff = 64 << 20
	}
	nS, nT := uint32(len(g.src)/BlockSize), uint32(len(g.dst)/BlockSize)
	zero := make([]byte, BlockSize)

	// Every target block the source holds somewhere is copied from there;
	// zeros are written as zeros.
	seed := maphash.MakeSeed()
	index := make(map[uint64]uint32, nS)
	for i := int64(nS) - 1; i >= 0; i-- {
		if b := g.sblock(uint32(i)); !bytes.Equal(b, zero) {
			index[maphash.Bytes(seed, b)] = uint32(i)
		}
	}
	g.kind, g.from, g.missing = make([]uint8, nT), make([]uint32, nT), make([]bool, nT)
	sourceHasZero := false
	for i := uint32(0); i < nS && !sourceHasZero; i++ {
		sourceHasZero = bytes.Equal(g.sblock(i), zero)
	}
	report := &Report{Ops: map[string]*OpStat{}}
	for t := uint32(0); t < nT; t++ {
		b := g.tblock(t)
		if bytes.Equal(b, zero) {
			g.kind[t], g.missing[t] = kindZero, !sourceHasZero
			continue
		}
		// The block after the last one copied, the same block, any block.
		var cands [3]uint32
		n := 0
		if t > 0 && g.kind[t-1] == kindCopy {
			cands[n], n = g.from[t-1]+1, n+1
		}
		cands[n], n = t, n+1
		if s, ok := index[maphash.Bytes(seed, b)]; ok {
			cands[n], n = s, n+1
		}
		g.kind[t], g.missing[t] = kindNew, true
		for _, s := range cands[:n] {
			if s < nS && bytes.Equal(g.sblock(s), b) {
				g.kind[t], g.from[t], g.missing[t] = kindCopy, s, false
				break
			}
		}
	}

	// Files with new blocks: diffed against the same file in the source
	// (or one like it) where the patch is smaller than the new blocks.
	tfs, err := ext4.Open(bytes.NewReader(g.dst))
	if err != nil {
		return nil, fmt.Errorf("delta: the target: %w", err)
	}
	sfs, err := ext4.Open(bytes.NewReader(g.src))
	if err != nil {
		return nil, fmt.Errorf("delta: the source: %w", err)
	}
	var tfiles []ext4.File
	seen := map[uint32]bool{}
	if err := tfs.Walk(func(f ext4.File) error {
		if f.IsRegular() && len(f.Extents) > 0 && !seen[f.Inode] {
			seen[f.Inode] = true
			tfiles = append(tfiles, f)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("delta: the target: %w", err)
	}
	byPath, byBase, byDir := map[string]ext4.File{}, map[string][]ext4.File{}, map[string][]ext4.File{}
	if err := sfs.Walk(func(f ext4.File) error {
		if f.IsRegular() && len(f.Extents) > 0 {
			byPath[f.Path] = f
			byBase[path.Base(f.Path)] = append(byBase[path.Base(f.Path)], f)
			if isELF(g.sblock(uint32(f.Extents[0].Physical))) {
				byDir[path.Dir(f.Path)] = append(byDir[path.Dir(f.Path)], f)
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("delta: the source: %w", err)
	}
	owned := make([]bool, nT)
	var jobs []*job
	for _, f := range tfiles {
		j := &job{path: f.Path, size: f.Size, blocks: fileBlocks(f)}
		for _, b := range j.blocks {
			if b >= nT {
				return nil, fmt.Errorf("delta: %s reaches outside the target", f.Path)
			}
			owned[b] = true
			if g.kind[b] == kindNew {
				j.fresh = append(j.fresh, b)
			}
		}
		if len(j.fresh) == 0 {
			continue
		}
		old, ok := byPath[f.Path]
		if !ok {
			if same := byBase[path.Base(f.Path)]; len(same) == 1 {
				old, ok = same[0], true
			}
		}
		if !ok && f.Size >= smallFile && isELF(g.tblock(j.blocks[0])) {
			for _, o := range byDir[path.Dir(f.Path)] {
				if o.Size*2 >= f.Size && o.Size <= f.Size*2 && (!ok || abs(o.Size-f.Size) < abs(old.Size-f.Size)) {
					old, ok = o, true
				}
			}
		}
		if ok {
			j.from, j.src = old.Path, fileBlocks(old)
			for _, b := range j.src {
				if b >= nS {
					return nil, fmt.Errorf("delta: %s reaches outside the source", old.Path)
				}
			}
		}
		jobs = append(jobs, j)
	}
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].size > jobs[b].size })
	for _, m := range g.missing {
		if m {
			report.Adaptive.MissingBlocks++
		}
	}
	logf("%d target blocks, %d found nowhere in the source; %d changed files to weigh", nT, report.Adaptive.MissingBlocks, len(jobs))
	if err := parallel(workers, len(jobs), func(i int) error { return g.evaluate(jobs[i], maxDiff) }); err != nil {
		return nil, err
	}

	var ops []pending
	var shared []uint32
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].path < jobs[b].path })
	for _, j := range jobs {
		report.Files = append(report.Files, FileStat{Path: j.path, Size: j.size, Method: j.method, Bytes: j.cost, From: j.from})
		switch j.method {
		case "bsdiff":
			for _, b := range j.blocks {
				g.kind[b] = kindDiff
			}
			ops = append(ops, pending{op: Op{Type: SourceBsdiff, Src: toExtents(j.src), Dst: toExtents(j.blocks)}, data: j.patch})
		case "replace":
			ops = append(ops, j.ops...)
		default:
			shared = append(shared, j.fresh...)
		}
	}
	sort.SliceStable(report.Files, func(a, b int) bool { return report.Files[a].Bytes > report.Files[b].Bytes })
	// New blocks outside files (the file system's metadata), with the small
	// files, compressed together.
	for t := uint32(0); t < nT; t++ {
		if g.kind[t] == kindNew && !owned[t] {
			shared = append(shared, t)
		}
	}
	var chunks [][]uint32
	for len(shared) > 0 {
		n := min(len(shared), chunkBlocks)
		chunks = append(chunks, shared[:n])
		shared = shared[n:]
	}
	sharedOps := make([][]pending, len(chunks))
	if err := parallel(workers, len(chunks), func(i int) error {
		var err error
		sharedOps[i], _, err = g.replaceOps(chunks[i])
		return err
	}); err != nil {
		return nil, err
	}
	for _, p := range sharedOps {
		ops = append(ops, p...)
	}
	var zeros, copies []uint32
	for t := uint32(0); t < nT; t++ {
		switch g.kind[t] {
		case kindZero:
			zeros = append(zeros, t)
		case kindCopy:
			copies = append(copies, t)
		}
	}
	var take []Extent
	var taken uint64
	flush := func() {
		if len(take) > 0 {
			ops = append(ops, pending{op: Op{Type: Zero, Dst: take}})
			take, taken = nil, 0
		}
	}
	for _, e := range toExtents(zeros) {
		for e[1] > 0 {
			k := min(e[1], zeroBlocks-taken)
			take, taken, e = append(take, Extent{e[0], k}), taken+k, Extent{e[0] + k, e[1] - k}
			if taken == zeroBlocks {
				flush()
			}
		}
	}
	flush()
	for len(copies) > 0 {
		n := min(len(copies), chunkBlocks)
		srcs := make([]uint32, n)
		for i, t := range copies[:n] {
			srcs[i] = g.from[t]
		}
		ops = append(ops, pending{op: Op{Type: SourceCopy, Src: toExtents(srcs), Dst: toExtents(copies[:n])}})
		copies = copies[n:]
	}

	// In target order, with their hashes and their data one after another.
	sort.Slice(ops, func(a, b int) bool { return ops[a].op.Dst[0][0] < ops[b].op.Dst[0][0] })
	m := &Manifest{Format: Format, BlockSize: BlockSize,
		Source: Image{Version: c.SourceVersion, Size: uint64(len(g.src)), SHA256: sum(g.src)},
		Target: Image{Version: c.TargetVersion, Size: uint64(len(g.dst)), SHA256: sum(g.dst)},
		Verity: VerityOf(desc)}
	if c.Source.Desc != nil {
		if c.Source.Desc.DataBlocks != uint64(nS) {
			return nil, errors.New("delta: the source is not its descriptor's size")
		}
		m.Source.RootHash = hex.EncodeToString(c.Source.Desc.RootHash[:])
	}
	var data uint64
	for i := range ops {
		op := &ops[i].op
		if len(ops[i].data) > 0 {
			op.DataOffset, op.DataLength, op.DataSHA256 = data, uint64(len(ops[i].data)), sum(ops[i].data)
			data += op.DataLength
		}
		if len(op.Src) > 0 {
			op.SrcSHA256 = sum(readAll(g.src, op.Src))
		}
		op.DstSHA256 = sum(readAll(g.dst, op.Dst))
		m.Ops = append(m.Ops, *op)
		st := report.Ops[op.Type]
		if st == nil {
			st = &OpStat{}
			report.Ops[op.Type] = st
		}
		st.Count++
		st.Blocks += blocks(op.Dst)
		st.Data += op.DataLength
	}
	if err := m.Check(); err != nil {
		return nil, fmt.Errorf("delta: made a bad manifest: %w", err)
	}
	var payload bytes.Buffer
	if err := WriteHeader(&payload, m); err != nil {
		return nil, err
	}
	for _, p := range ops {
		payload.Write(p.data)
	}
	report.Source, report.Target, report.Payload = m.Source, m.Target, int64(payload.Len())
	report.Adaptive.MissingBytes = int64(report.Adaptive.MissingBlocks) * BlockSize
	report.Adaptive.IndexBytes = int64(nT) * sha256.Size
	report.Adaptive.Estimate = report.Adaptive.IndexBytes + g.adaptiveBlocks()
	if !c.NoVerify {
		dev := &memDevice{b: make([]byte, desc.HashOffset+verity.TreeSize(desc.DataBlocks))}
		if _, err := Apply(bytes.NewReader(payload.Bytes()), ApplyConfig{Source: bytes.NewReader(g.src), SourceSize: int64(len(g.src)),
			SourceDesc: c.Source.Desc, Target: dev, TargetSize: int64(len(dev.b)), TargetDesc: desc}); err != nil {
			return nil, fmt.Errorf("delta: the payload does not apply: %w", err)
		}
		if !bytes.Equal(dev.b[:len(g.dst)], g.dst) {
			return nil, errors.New("delta: the payload applies to other data than the target")
		}
		logf("checked: the payload rebuilds the target and its root hash")
	}
	if _, err := w.Write(payload.Bytes()); err != nil {
		return nil, err
	}
	return report, nil
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func readAll(img []byte, ext []Extent) []byte {
	b := make([]byte, 0, blocks(ext)*BlockSize)
	for _, e := range ext {
		b = append(b, img[e[0]*BlockSize:(e[0]+e[1])*BlockSize]...)
	}
	return b
}

// adaptiveBlocks: the gzip-compressed size of the target's 128 KiB pieces
// (mksquashfs blocks of the bundle's image) that hold a block the source
// does not have.
func (g *gen) adaptiveBlocks() int64 {
	per := uint32(squashBlock / BlockSize)
	var total int64
	var mu sync.Mutex
	var pieces []uint32
	for p := uint32(0); p*per < uint32(len(g.kind)); p++ {
		for t := p * per; t < min((p+1)*per, uint32(len(g.kind))); t++ {
			if g.missing[t] {
				pieces = append(pieces, p)
				break
			}
		}
	}
	_ = parallel(runtime.NumCPU(), len(pieces), func(i int) error {
		start := int(pieces[i]) * squashBlock
		var b bytes.Buffer
		zw, _ := flate.NewWriter(&b, flate.BestCompression)
		_, _ = zw.Write(g.dst[start:min(start+squashBlock, len(g.dst))])
		_ = zw.Close()
		mu.Lock()
		total += int64(b.Len())
		mu.Unlock()
		return nil
	})
	return total
}

func parallel(workers, n int, fn func(int) error) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if err := fn(i); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
	return first
}
