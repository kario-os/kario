package delta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errCut = errors.New("power cut")

// cutDevice is a slot that loses power: writes since the last sync are
// pending, and when the power goes (at the cutAt-th write or sync) each
// pending write reaches the device whole, in part (some of its blocks) or
// not at all.
type cutDevice struct {
	durable []byte
	live    []byte
	pending []pendingWrite
	calls   int
	cutAt   int
	r       *rand.Rand
}

type pendingWrite struct {
	off  int64
	data []byte
}

func newCutDevice(img []byte, seed int64) *cutDevice {
	return &cutDevice{durable: append([]byte{}, img...), live: append([]byte{}, img...), r: rand.New(rand.NewSource(seed))}
}

func (d *cutDevice) tick() {
	if d.calls++; d.cutAt > 0 && d.calls == d.cutAt {
		panic(errCut)
	}
}

func (d *cutDevice) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(d.live)) {
		return 0, io.EOF
	}
	return copy(p, d.live[off:]), nil
}

func (d *cutDevice) WriteAt(p []byte, off int64) (int, error) {
	d.tick()
	if off < 0 || off+int64(len(p)) > int64(len(d.live)) {
		return 0, errors.New("write outside the device")
	}
	copy(d.live[off:], p)
	d.pending = append(d.pending, pendingWrite{off, append([]byte{}, p...)})
	return len(p), nil
}

func (d *cutDevice) Sync() error {
	d.tick()
	for _, w := range d.pending {
		copy(d.durable[w.off:], w.data)
	}
	d.pending = nil
	return nil
}

// reboot is the device after the power came back.
func (d *cutDevice) reboot() {
	for _, w := range d.pending {
		for b := 0; b < len(w.data); b += BlockSize {
			if d.r.Intn(3) > 0 {
				copy(d.durable[w.off+int64(b):], w.data[b:min(b+BlockSize, len(w.data))])
			}
		}
	}
	d.pending, d.calls, d.cutAt = nil, 0, 0
	d.live = append(d.live[:0], d.durable...)
}

// counted reads a payload, counting what it reads; with seek it skips
// without reading, as a file in a mounted bundle does.
type counted struct {
	r    *bytes.Reader
	read int
	seek bool
}

func (c *counted) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

type seekCounted struct{ *counted }

func (c seekCounted) Seek(off int64, whence int) (int64, error) { return c.r.Seek(off, whence) }

func (c *counted) reader() io.Reader {
	if c.seek {
		return seekCounted{c}
	}
	return c
}

// stalling is a payload whose network goes away after n bytes.
type stalling struct {
	r io.Reader
	n int
}

func (s *stalling) Read(p []byte) (int, error) {
	if s.n <= 0 {
		return 0, errors.New("nbd: input/output error")
	}
	p = p[:min(len(p), s.n)]
	n, err := s.r.Read(p)
	s.n -= n
	return n, err
}

type resumeCase struct {
	v                versions
	payload, src     []byte
	want             []byte // the target slot: data and hash tree
	part             int    // bytes of the inactive slot
	checkpoint       string
	manifest, header int
}

func newResumeCase(t *testing.T) *resumeCase {
	v := twoVersions(t)
	payload, _ := generate(t, v)
	m, err := ReadManifest(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(v.src)
	want, _ := os.ReadFile(v.dst)
	return &resumeCase{v: v, payload: payload, src: src[:v.srcDesc.HashOffset], want: want, part: len(src) + 1<<20,
		checkpoint: filepath.Join(t.TempDir(), "rootfs_b.progress"), header: len(payload) - int(m.DataSize()), manifest: len(m.Ops)}
}

// inactiveSlot is the inactive slot as a phone has it: the source image.
func (rc *resumeCase) inactiveSlot() []byte {
	b := make([]byte, rc.part)
	copy(b, rc.src)
	return b
}

func (rc *resumeCase) apply(payload io.Reader, dev Device, size int, log *strings.Builder) (*Result, error) {
	return Apply(payload, ApplyConfig{Source: bytes.NewReader(rc.src), SourceSize: int64(len(rc.src)), SourceDesc: &rc.v.srcDesc,
		Target: dev, TargetSize: int64(size), TargetDesc: rc.v.dstDesc, Checkpoint: rc.checkpoint, CheckpointBytes: 64 << 10,
		Log: func(format string, a ...any) {
			if log != nil {
				fmt.Fprintf(log, format+"\n", a...)
			}
		}})
}

// cut applies until the power goes at the cutAt-th write or sync; false
// when the install ended first.
func (rc *resumeCase) cut(dev *cutDevice, cutAt int) (cutOff bool) {
	defer func() {
		if r := recover(); r != nil {
			if r != errCut {
				panic(r)
			}
			cutOff = true
		}
	}()
	dev.cutAt = cutAt
	if _, err := rc.apply(bytes.NewReader(rc.payload), dev, rc.part, nil); err != nil {
		panic(err)
	}
	return false
}

// A power cut anywhere in an install, writes since the last sync lost in
// part: the next install of the same payload resumes and the slot comes
// out byte for byte the target, reading less of the payload when it
// resumed past data.
func TestResumeAfterPowerCut(t *testing.T) {
	rc := newResumeCase(t)
	total := 0
	{
		dev := newCutDevice(rc.inactiveSlot(), 0)
		if rc.cut(dev, 0) {
			t.Fatal("cut without a cut point")
		}
		total = dev.calls
	}
	if _, err := os.Stat(rc.checkpoint); !os.IsNotExist(err) {
		t.Fatal("a completed install left its checkpoint")
	}
	resumed, less := 0, 0
	for at := 1; at <= total; at += max(1, total/30) {
		dev := newCutDevice(rc.inactiveSlot(), int64(at))
		if !rc.cut(dev, at) {
			t.Fatalf("the install ended before its %d-th of %d writes and syncs", at, total)
		}
		dev.reboot()
		in := &counted{r: bytes.NewReader(rc.payload), seek: at%2 == 0}
		var log strings.Builder
		res, err := rc.apply(in.reader(), dev, rc.part, &log)
		if err != nil {
			t.Fatalf("cut at %d of %d: the next install failed: %v\n%s", at, total, err, log.String())
		}
		if !bytes.Equal(dev.live[:len(rc.want)], rc.want) || !bytes.Equal(dev.durable[:len(rc.want)], rc.want) {
			t.Fatalf("cut at %d of %d (resumed at operation %d): the slot is not the target", at, total, res.Resumed)
		}
		if _, err := os.Stat(rc.checkpoint); !os.IsNotExist(err) {
			t.Fatalf("cut at %d: the checkpoint outlived the install", at)
		}
		if res.Resumed > 0 {
			resumed++
			if !strings.Contains(log.String(), fmt.Sprintf("resumed at operation %d of %d", res.Resumed, rc.manifest)) {
				t.Fatalf("cut at %d: the log does not say where it resumed:\n%s", at, log.String())
			}
			if want := len(rc.payload) - int(res.Skipped); in.seek && in.read != want {
				t.Fatalf("cut at %d: read %d bytes of the payload, want %d (%d skipped)", at, in.read, want, res.Skipped)
			}
			if res.Skipped > 0 && in.seek {
				less++
			}
		}
	}
	t.Logf("%d writes and syncs in an install; %d cut installs resumed, %d of them reading less of the payload", total, resumed, less)
	if resumed < 10 || less < 3 {
		t.Fatalf("too few resumed (%d) or reading less (%d)", resumed, less)
	}
}

// The network goes away mid-install: the checkpoint stays, the log says
// so, and the next install of the payload reads only the rest of it.
func TestResumeAfterNetworkLoss(t *testing.T) {
	rc := newResumeCase(t)
	dev := newCutDevice(rc.inactiveSlot(), 1)
	var log strings.Builder
	cutAt := rc.header + (len(rc.payload)-rc.header)*3/4
	if _, err := rc.apply(&stalling{r: bytes.NewReader(rc.payload), n: cutAt}, dev, rc.part, &log); err == nil {
		t.Fatal("applied a payload that stopped arriving")
	}
	p := ParseLog([]byte(log.String()))
	if !p.Stalled || p.Total != rc.manifest || p.Done == 0 {
		t.Fatalf("the log reads %+v:\n%s", p, log.String())
	}
	k, err := ParseCheckpoint(must(os.ReadFile(rc.checkpoint)))
	if err != nil || k.Next == 0 {
		t.Fatalf("no checkpoint kept: %+v %v", k, err)
	}
	for _, seek := range []bool{true, false} {
		in := &counted{r: bytes.NewReader(rc.payload), seek: seek}
		var log2 strings.Builder
		d := newCutDevice(dev.durable, 2)
		_ = os.WriteFile(rc.checkpoint, k.Marshal(), 0o600)
		res, err := rc.apply(in.reader(), d, rc.part, &log2)
		if err != nil {
			t.Fatal(err)
		}
		if res.Resumed != k.Next || res.Skipped == 0 || !bytes.Equal(d.live[:len(rc.want)], rc.want) {
			t.Fatalf("resumed at %d (checkpoint %d), skipped %d", res.Resumed, k.Next, res.Skipped)
		}
		if p := ParseLog([]byte(log2.String())); p.Resumed != k.Next || p.Stalled {
			t.Fatalf("the resumed install's log reads %+v", p)
		}
		if seek {
			if in.read != len(rc.payload)-int(res.Skipped) {
				t.Fatalf("read %d of %d payload bytes, %d skipped", in.read, len(rc.payload), res.Skipped)
			}
			t.Logf("resumed at operation %d of %d: read %d of %d payload bytes", res.Resumed, rc.manifest, in.read, len(rc.payload))
			// The same, from a stream that cannot seek: it reads past.
			continue
		}
		if in.read != len(rc.payload) {
			t.Fatalf("a stream read %d of %d bytes", in.read, len(rc.payload))
		}
	}
}

// boundary is the operation and piece whose data holds payload data byte
// at (counted after the manifest).
func boundary(m *Manifest, at uint64) (int, int) {
	for i, op := range m.Ops {
		if op.DataLength == 0 || at >= op.DataOffset+op.DataLength {
			continue
		}
		for k := range op.Pieces {
			if at < dataOffset(m.Ops, i, k+1) {
				return i, k
			}
		}
		return i, 0
	}
	return len(m.Ops), 0
}

// The network goes long after the last periodic checkpoint: the install
// records one where it stopped, at the operation (or piece) whose data was
// arriving, and the next install downloads only the data from there on.
// The periodic one alone lies up to 32 MiB of slot or 5 seconds behind,
// and all the payload between is downloaded again.
func TestNetworkLossCheckpointsWhereItStopped(t *testing.T) {
	for _, pieces := range []bool{false, true} {
		rc := newResumeCase(t)
		if pieces {
			rc.payload = generateWith(t, rc.v, 2, smallPieces)
		}
		m, err := ReadManifest(bytes.NewReader(rc.payload))
		if err != nil {
			t.Fatal(err)
		}
		header := len(rc.payload) - int(m.DataSize())
		for _, frac := range []int{30, 55, 80} {
			at := uint64(int(m.DataSize()) * frac / 100)
			wantNext, wantPiece := boundary(m, at)
			dev := newCutDevice(rc.inactiveSlot(), int64(frac))
			var log strings.Builder
			_, err := Apply(&stalling{r: bytes.NewReader(rc.payload), n: header + int(at)}, ApplyConfig{Source: bytes.NewReader(rc.src),
				SourceSize: int64(len(rc.src)), SourceDesc: &rc.v.srcDesc, Target: dev, TargetSize: int64(rc.part), TargetDesc: rc.v.dstDesc,
				Checkpoint: rc.checkpoint, CheckpointBytes: 1 << 40, Log: func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) }})
			if err == nil {
				t.Fatal("applied a payload that stopped arriving")
			}
			raw, _ := os.ReadFile(rc.checkpoint)
			k, perr := ParseCheckpoint(raw)
			if perr != nil || k.Next != wantNext || k.Piece != wantPiece || k.DataOffset != dataOffset(m.Ops, wantNext, wantPiece) {
				t.Fatalf("pieces %v, stopped at %d%% of the data: checkpoint %+v %v, want operation %d piece %d\n%s", pieces, frac, k, perr, wantNext, wantPiece, log.String())
			}
			if !ParseLog([]byte(log.String())).Stalled || !strings.Contains(log.String(), fmt.Sprintf("resumes at operation %d", wantNext)) {
				t.Fatalf("the log:\n%s", log.String())
			}
			dev.reboot()
			in := &counted{r: bytes.NewReader(rc.payload), seek: true}
			res, err := rc.apply(in.reader(), dev, rc.part, nil)
			if err != nil || res.Resumed != wantNext || !bytes.Equal(dev.durable[:len(rc.want)], rc.want) {
				t.Fatalf("pieces %v at %d%%: the resumed install: %v (resumed at %d)", pieces, frac, err, res.Resumed)
			}
			if in.read != len(rc.payload)-int(k.DataOffset) {
				t.Fatalf("pieces %v at %d%%: read %d of %d payload bytes again", pieces, frac, in.read, len(rc.payload))
			}
		}
	}
}

// Asked to stop (an automatic install whose conditions went away), the
// install ends before the next operation or piece with ErrStopped and a
// checkpoint there, and installing it again resumes from it.
func TestStopAndResume(t *testing.T) {
	for _, pieces := range []bool{false, true} {
		rc := newResumeCase(t)
		if pieces {
			rc.payload = generateWith(t, rc.v, 2, smallPieces)
		}
		for _, after := range []int{0, 1, 3, 5} {
			asked := 0
			dev := newCutDevice(rc.inactiveSlot(), int64(after))
			var log strings.Builder
			_, err := Apply(bytes.NewReader(rc.payload), ApplyConfig{Source: bytes.NewReader(rc.src), SourceSize: int64(len(rc.src)), SourceDesc: &rc.v.srcDesc,
				Target: dev, TargetSize: int64(rc.part), TargetDesc: rc.v.dstDesc, Checkpoint: rc.checkpoint, CheckpointBytes: 1 << 40,
				Stop: func() bool { asked++; return asked > after },
				Log:  func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) }})
			if !errors.Is(err, ErrStopped) {
				t.Fatalf("pieces %v, stopped after %d steps: %v", pieces, after, err)
			}
			p := ParseLog([]byte(log.String()))
			if !p.Stopped || p.Stalled {
				t.Fatalf("the log reads %+v:\n%s", p, log.String())
			}
			raw, _ := os.ReadFile(rc.checkpoint)
			k, perr := ParseCheckpoint(raw)
			if after > 0 && (perr != nil || k.Next == 0 && k.Piece == 0) {
				t.Fatalf("pieces %v, stopped after %d steps: no checkpoint (%v)\n%s", pieces, after, perr, log.String())
			}
			if after == 0 && perr == nil {
				t.Fatal("a checkpoint before anything was written")
			}
			dev.reboot()
			in := &counted{r: bytes.NewReader(rc.payload), seek: true}
			var log2 strings.Builder
			res, err := rc.apply(in.reader(), dev, rc.part, &log2)
			if err != nil || !bytes.Equal(dev.durable[:len(rc.want)], rc.want) {
				t.Fatalf("pieces %v after %d: installing again: %v\n%s", pieces, after, err, log2.String())
			}
			if after > 0 && (res.Resumed != k.Next || res.ResumedPiece != k.Piece || in.read != len(rc.payload)-int(k.DataOffset)) {
				t.Fatalf("pieces %v after %d: resumed at %d piece %d (checkpoint %+v), read %d", pieces, after, res.Resumed, res.ResumedPiece, k, in.read)
			}
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// A checkpoint that is not this install's (another payload, source or
// target, or a damaged one) is ignored and removed: the install starts
// from the beginning. One that matches but vouches for blocks the target
// no longer has fails at the read-back, never yields the slot.
func TestResumeMismatch(t *testing.T) {
	rc := newResumeCase(t)
	var dev *cutDevice
	for at := 20; ; at += 20 {
		dev = newCutDevice(rc.inactiveSlot(), 3)
		if !rc.cut(dev, at) {
			t.Fatal("the install ended before a checkpoint")
		}
		if _, err := os.Stat(rc.checkpoint); err == nil {
			break
		}
	}
	dev.reboot()
	good, err := ParseCheckpoint(must(os.ReadFile(rc.checkpoint)))
	if err != nil || good.Next == 0 {
		t.Fatalf("no checkpoint to start from: %v", err)
	}
	other := strings.Repeat("ab", 32)
	raw := good.Marshal()
	torn := append([]byte{}, raw...)
	torn[len("format=2\npayload=")] ^= 1
	for name, c := range map[string]struct {
		file []byte
		size int
	}{
		"another payload":     {func() []byte { k := good; k.Payload = other; return k.Marshal() }(), rc.part},
		"another source":      {func() []byte { k := good; k.Source = other; return k.Marshal() }(), rc.part},
		"another target":      {func() []byte { k := good; k.TargetRoot = other; return k.Marshal() }(), rc.part},
		"another target size": {raw, rc.part + BlockSize},
		"past the operations": {func() []byte { k := good; k.Next = rc.manifest + 1; return k.Marshal() }(), rc.part},
		"another data offset": {func() []byte { k := good; k.DataOffset++; return k.Marshal() }(), rc.part},
		"torn":                {raw[:len(raw)/2], rc.part},
		"damaged":             {torn, rc.part},
		"empty":               {nil, rc.part},
	} {
		if err := os.WriteFile(rc.checkpoint, c.file, 0o600); err != nil {
			t.Fatal(err)
		}
		img := make([]byte, c.size)
		copy(img, dev.durable)
		d := newCutDevice(img, 4)
		var log strings.Builder
		res, err := rc.apply(bytes.NewReader(rc.payload), d, c.size, &log)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, log.String())
		}
		if res.Resumed != 0 || !bytes.Equal(d.live[:len(rc.want)], rc.want) {
			t.Fatalf("%s: resumed at %d\n%s", name, res.Resumed, log.String())
		}
		if !strings.Contains(log.String(), "starting from the beginning") {
			t.Fatalf("%s: the log does not say it started afresh:\n%s", name, log.String())
		}
		if _, err := os.Stat(rc.checkpoint); !os.IsNotExist(err) {
			t.Fatalf("%s: the checkpoint is still there", name)
		}
	}

	// The checkpoint is this install's, but the blocks it vouches for are
	// gone (something else wrote the slot): the resumed install fails at
	// the read-back, and the one after it starts afresh and succeeds.
	if err := os.WriteFile(rc.checkpoint, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	scribbled := make([]byte, rc.part)
	rand.New(rand.NewSource(5)).Read(scribbled)
	d := newCutDevice(scribbled, 5)
	if _, err := rc.apply(bytes.NewReader(rc.payload), d, rc.part, nil); err == nil || !strings.Contains(err.Error(), "not the target image") {
		t.Fatalf("a resume over other blocks: %v", err)
	}
	if _, err := os.Stat(rc.checkpoint); !os.IsNotExist(err) {
		t.Fatal("a failed verification kept the checkpoint")
	}
	if res, err := rc.apply(bytes.NewReader(rc.payload), d, rc.part, nil); err != nil {
		t.Fatalf("after the failed resume: %v", err)
	} else if res.Resumed != 0 || !bytes.Equal(d.live[:len(rc.want)], rc.want) {
		t.Fatalf("after the failed resume: resumed at %d", res.Resumed)
	}

	// Damaged data stops the install and its checkpoint goes too: only the
	// payload not arriving keeps one.
	d = newCutDevice(rc.inactiveSlot(), 6)
	damaged := append([]byte{}, rc.payload...)
	damaged[len(damaged)-1] ^= 0x40
	if _, err := rc.apply(bytes.NewReader(damaged), d, rc.part, nil); err == nil {
		t.Fatal("damaged data applied")
	}
	if _, err := os.Stat(rc.checkpoint); !os.IsNotExist(err) {
		t.Fatal("damaged data kept the checkpoint")
	}
}

func TestCheckpointFile(t *testing.T) {
	h := strings.Repeat("0f", 32)
	k := Checkpoint{Payload: h, Source: h, TargetRoot: h, TargetSize: 1 << 30, Next: 7, DataOffset: 12345}
	path := filepath.Join(t.TempDir(), "rootfs_a.progress")
	if err := WriteCheckpoint(path, k); err != nil {
		t.Fatal(err)
	}
	got, err := ParseCheckpoint(must(os.ReadFile(path)))
	if err != nil || got != k {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temporary file stayed")
	}
	raw := string(k.Marshal())
	for _, bad := range []string{"", raw + "x=1\n", strings.Replace(raw, "next_operation=7", "next_operation=8", 1),
		strings.Replace(raw, "format=2", "format=1", 1), strings.Replace(raw, "next_piece=0\n", "", 1), strings.TrimSuffix(raw, "\n"), "format=2\n" + raw} {
		if _, err := ParseCheckpoint([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := RemoveCheckpoint(path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveCheckpoint(path); err != nil {
		t.Fatalf("removing none: %v", err)
	}
}

// A data offset past any payload's data (one that would turn negative as a
// seek) is a damaged checkpoint, however well its own hash checks.
func TestCheckpointDataOffsetBound(t *testing.T) {
	h := strings.Repeat("0f", 32)
	for _, c := range []struct {
		off uint64
		ok  bool
	}{{maxPayloadData, true}, {maxPayloadData + 1, false}, {1 << 63, false}, {^uint64(0), false}} {
		k := Checkpoint{Payload: h, Source: h, TargetRoot: h, TargetSize: 1 << 30, Next: 7, DataOffset: c.off}
		got, err := ParseCheckpoint(k.Marshal())
		if c.ok && (err != nil || got != k) || !c.ok && err == nil {
			t.Errorf("data offset %d: %+v %v", c.off, got, err)
		}
	}
}

func TestParseLog(t *testing.T) {
	log := "phone-ota-delta: applying 83 operations (5 bytes of data) from a to b\n" +
		"phone-ota-delta: " + fmt.Sprintf(resumedLine, 40, 83, 1000) + "\n" +
		"phone-ota-delta: " + fmt.Sprintf(progressLine, 52, 83, 1<<20) + "\n" +
		"phone-ota-delta: progress: operation 60 of 8"
	if p := ParseLog([]byte(log)); p != (Progress{Done: 52, Total: 83, Resumed: 40}) {
		t.Fatalf("%+v", p)
	}
	stalled := log + "3, 9 bytes written\nphone-ota-delta: " + stalledLine + "; installing it again resumes at operation 52\n"
	if p := ParseLog([]byte(stalled)); !p.Stalled || p.Done != 60 {
		t.Fatalf("%+v", p)
	}
	if p := ParseLog(nil); p != (Progress{}) {
		t.Fatalf("%+v", p)
	}
}
