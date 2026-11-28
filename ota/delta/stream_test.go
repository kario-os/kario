package delta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/iotest"
)

// generateWith makes the payload of v with the format and piece size given.
func generateWith(t *testing.T, v versions, format, piece int) []byte {
	t.Helper()
	src, err := LoadSlot(v.src, "", "a")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := LoadSlot(v.dst, "", "a")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Generate(GenerateConfig{Source: src, Target: dst, Compress: compressor(t), Format: format, Piece: piece}, &out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// smallPieces: pieces of 64 KiB, so that the test's programs are patched in
// many.
const smallPieces = 64 << 10

func pieceOps(t *testing.T, payload []byte) (*Manifest, []int) {
	t.Helper()
	m, err := ReadManifest(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	var in []int
	for i, op := range m.Ops {
		if len(op.Pieces) > 0 {
			in = append(in, i)
		}
	}
	return m, in
}

// A payload whose patches stream in pieces rebuilds the same slot as the
// one with whole patches, read a byte at a time as a slow download hands
// it over; its patches are what bsdiff makes whole, only cut.
func TestStreamedEqualsWhole(t *testing.T) {
	t.Parallel()
	v := twoVersions(t)
	whole := generateWith(t, v, 1, 0)
	streamed := generateWith(t, v, 2, smallPieces)
	m, in := pieceOps(t, streamed)
	if m.Format != 2 || len(in) == 0 {
		t.Fatalf("format %d, %d operations in pieces", m.Format, len(in))
	}
	most := 0
	for _, i := range in {
		most = max(most, len(m.Ops[i].Pieces))
	}
	if most < 8 {
		t.Fatalf("at most %d pieces in an operation", most)
	}
	t.Logf("whole patches %d bytes, in pieces of %d KiB %d bytes; %d operations in pieces, up to %d pieces", len(whole), smallPieces>>10, len(streamed), len(in), most)
	want, _ := os.ReadFile(v.dst)
	for name, p := range map[string][]byte{"whole": whole, "streamed": streamed} {
		part := inactive(t, v.src)
		s, _ := os.Open(v.src)
		d, _ := os.OpenFile(part, os.O_RDWR, 0)
		ss, _ := s.Stat()
		ds, _ := d.Stat()
		_, err := Apply(iotest.OneByteReader(bytes.NewReader(p)), ApplyConfig{Source: s, SourceSize: ss.Size(), SourceDesc: &v.srcDesc,
			Target: d, TargetSize: ds.Size(), TargetDesc: v.dstDesc})
		s.Close()
		d.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := os.ReadFile(part); !bytes.Equal(got[:len(want)], want) {
			t.Fatalf("%s: the slot written is not the target", name)
		}
	}
	// An older applier's format: written as asked, and read as before.
	if f, err := PayloadFormat(bytes.NewReader(whole)); err != nil || f != 1 {
		t.Fatalf("format %d %v", f, err)
	}
	if bytes.Contains(whole, []byte(`"pieces"`)) {
		t.Fatal("pieces in a format 1 payload")
	}
}

// A power cut in the middle of an operation in pieces: the next install
// resumes at the piece after the last checkpoint, without reading the data
// of the pieces before it, and the slot comes out the target.
func TestResumeMidPiece(t *testing.T) {
	t.Parallel()
	rc := newResumeCase(t)
	rc.payload = generateWith(t, rc.v, 2, smallPieces)
	m, in := pieceOps(t, rc.payload)
	rc.header, rc.manifest = len(rc.payload)-int(m.DataSize()), len(m.Ops)
	total := 0
	{
		dev := newCutDevice(rc.inactiveSlot(), 0)
		if rc.cut(dev, 0) {
			t.Fatal("cut without a cut point")
		}
		total = dev.calls
	}
	midPiece, less := 0, 0
	for at := 1; at <= total; at += max(1, total/30) {
		dev := newCutDevice(rc.inactiveSlot(), int64(at))
		if !rc.cut(dev, at) {
			t.Fatalf("the install ended before its %d-th of %d writes and syncs", at, total)
		}
		dev.reboot()
		in := &counted{r: bytes.NewReader(rc.payload), seek: true}
		var log strings.Builder
		res, err := rc.apply(in.reader(), dev, rc.part, &log)
		if err != nil {
			t.Fatalf("cut at %d of %d: the next install failed: %v\n%s", at, total, err, log.String())
		}
		if !bytes.Equal(dev.durable[:len(rc.want)], rc.want) {
			t.Fatalf("cut at %d (resumed at operation %d piece %d): the slot is not the target", at, res.Resumed, res.ResumedPiece)
		}
		if res.ResumedPiece > 0 {
			midPiece++
			line := fmt.Sprintf("resumed at operation %d of %d, piece %d of %d", res.Resumed, rc.manifest, res.ResumedPiece, len(m.Ops[res.Resumed].Pieces))
			if !strings.Contains(log.String(), line) {
				t.Fatalf("cut at %d: the log does not say %q:\n%s", at, line, log.String())
			}
			if p := ParseLog([]byte(log.String())); p.Resumed != res.Resumed {
				t.Fatalf("the log reads %+v", p)
			}
			if res.Skipped == 0 || in.read != len(rc.payload)-int(res.Skipped) {
				t.Fatalf("cut at %d: read %d bytes of %d, skipped %d", at, in.read, len(rc.payload), res.Skipped)
			}
			less++
		}
	}
	t.Logf("%d writes and syncs; %d operations in pieces; %d cut installs resumed part way through one", total, len(in), midPiece)
	if midPiece < 5 || less < 5 {
		t.Fatalf("resumed part way through an operation %d times", midPiece)
	}
}

// The network goes in the middle of an operation in pieces: the checkpoint
// kept names the piece, and the next install reads only the rest.
func TestResumeMidPieceAfterNetworkLoss(t *testing.T) {
	t.Parallel()
	rc := newResumeCase(t)
	rc.payload = generateWith(t, rc.v, 2, smallPieces)
	m, in := pieceOps(t, rc.payload)
	header := len(rc.payload) - int(m.DataSize())
	op := m.Ops[in[0]]
	for _, i := range in {
		if len(m.Ops[i].Pieces) > len(op.Pieces) {
			op = m.Ops[i]
		}
	}
	cutAt := header + int(op.DataOffset+dataOffset([]Op{op}, 0, len(op.Pieces)*3/4))
	dev := newCutDevice(rc.inactiveSlot(), 8)
	var log strings.Builder
	if _, err := Apply(&stalling{r: bytes.NewReader(rc.payload), n: cutAt}, ApplyConfig{Source: bytes.NewReader(rc.src), SourceSize: int64(len(rc.src)),
		SourceDesc: &rc.v.srcDesc, Target: dev, TargetSize: int64(rc.part), TargetDesc: rc.v.dstDesc, Checkpoint: rc.checkpoint, CheckpointBytes: smallPieces,
		Log: func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) }}); err == nil {
		t.Fatal("applied a payload that stopped arriving")
	}
	k, err := ParseCheckpoint(must(os.ReadFile(rc.checkpoint)))
	if err != nil || k.Piece == 0 || !ParseLog([]byte(log.String())).Stalled {
		t.Fatalf("checkpoint %+v %v\n%s", k, err, log.String())
	}
	in2 := &counted{r: bytes.NewReader(rc.payload), seek: true}
	res, err := rc.apply(in2.reader(), dev, rc.part, nil)
	if err != nil || res.Resumed != k.Next || res.ResumedPiece != k.Piece || in2.read != len(rc.payload)-int(k.DataOffset) {
		t.Fatalf("resumed at %d piece %d (checkpoint %d piece %d), read %d of %d: %v", res.Resumed, res.ResumedPiece, k.Next, k.Piece, in2.read, len(rc.payload), err)
	}
	if !bytes.Equal(dev.live[:len(rc.want)], rc.want) {
		t.Fatal("the slot is not the target")
	}
}

// A damaged piece stops the install before it is applied: the pieces
// before it are on the target, its own blocks are not written, and the
// checkpoint goes. So does a manifest whose pieces do not add up.
func TestDamagedPieceRejected(t *testing.T) {
	t.Parallel()
	rc := newResumeCase(t)
	rc.payload = generateWith(t, rc.v, 2, smallPieces)
	m, in := pieceOps(t, rc.payload)
	header := len(rc.payload) - int(m.DataSize())
	op := m.Ops[in[0]]
	for _, i := range in {
		if len(m.Ops[i].Pieces) > len(op.Pieces) {
			op = m.Ops[i]
		}
	}
	k := len(op.Pieces) / 2
	at := header + int(dataOffset([]Op{op}, 0, k)+op.DataOffset) + int(op.Pieces[k].Length)/2
	damaged := append([]byte{}, rc.payload...)
	damaged[at] ^= 0x10
	dev := newCutDevice(rc.inactiveSlot(), 7)
	before := append([]byte{}, dev.live...)
	_, err := rc.apply(bytes.NewReader(damaged), dev, rc.part, nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("piece %d does not match its hash", k)) {
		t.Fatalf("a damaged piece: %v", err)
	}
	w := &extentWriter{ext: op.Dst}
	w.skip(uint64(k) * op.Piece)
	for _, e := range w.ext[:1] {
		off := int64(e[0]*BlockSize + w.off)
		if !bytes.Equal(dev.live[off:off+BlockSize], before[off:off+BlockSize]) {
			t.Fatal("the damaged piece's blocks were written")
		}
	}
	if _, err := os.Stat(rc.checkpoint); !os.IsNotExist(err) {
		t.Fatal("a damaged piece kept the checkpoint")
	}

	var mm Manifest
	_ = json.Unmarshal(rc.payload[16:header], &mm)
	for name, change := range map[string]func(o *Op){
		"a piece missing":     func(o *Op) { o.Pieces = o.Pieces[1:] },
		"lengths off":         func(o *Op) { o.Pieces[0].Length++ },
		"pieces of odd bytes": func(o *Op) { o.Piece++ },
		"pieces too big":      func(o *Op) { o.Piece = 32 << 20 },
		"a piece not hashed":  func(o *Op) { o.Pieces[0].SHA256 = "" },
		"pieces of a copy":    func(o *Op) { o.Type = SourceCopy },
	} {
		bad := mm
		bad.Ops = append([]Op{}, mm.Ops...)
		o := bad.Ops[in[0]]
		o.Pieces = append([]Piece{}, o.Pieces...)
		change(&o)
		bad.Ops[in[0]] = o
		if bad.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	old := mm
	old.Format = 1
	if old.Check() == nil {
		t.Error("pieces in format 1: accepted")
	}
}
