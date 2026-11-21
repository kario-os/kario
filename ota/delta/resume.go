package delta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Resuming an install, as update_engine does: every so often, once the
// target is synced, the applier records in a checkpoint how far it got,
// and the next install of the same payload into the same target from the
// same source skips the operations done, without reading their data. A
// power cut between the writes and the next checkpoint only means those
// operations run again: each reads the source slot and its own data,
// never the target, and writes the same blocks. A resume in the middle of
// an operation in pieces reads back, only to hash them, the pieces done
// before the checkpoint, which were synced. An install that stops with
// the target still there (the payload stopped arriving, or it was asked
// to stop) records one where it stopped, after the last operation or piece
// it wrote whole. Whatever a resume skips, the read-back at the end covers
// the whole slot, so a wrong resume fails there and never yields a
// bootable slot.

const (
	checkpointFormat = 2
	// A checkpoint costs a sync of the target; one per this much written,
	// or this often, bounds what a power cut loses.
	checkpointBytes = 32 << 20
	checkpointEvery = 5 * time.Second
)

// The lines an install logs about its progress, which the shell reads from
// the install hook's log (ParseLog).
const (
	progressLine     = "progress: operation %d of %d, %d bytes written"
	resumedLine      = "resumed at operation %d of %d: %d bytes of payload data not read"
	resumedPieceLine = "resumed at operation %d of %d, piece %d of %d: %d bytes of payload data not read"
	stalledLine      = "the payload stopped arriving"
	stoppedLine      = "stopped on request"
)

// ErrStopped: the install stopped because ApplyConfig.Stop asked it to,
// with a checkpoint where it stopped.
var ErrStopped = errors.New("delta: " + stoppedLine)

// Checkpoint is how far an install got: the operations before Next are on
// the target, synced, and so are the pieces of operation Next before Piece.
type Checkpoint struct {
	// Payload is the SHA-256 of the payload's header and manifest, which
	// hold every operation's hashes.
	Payload string
	// Source is the source's root hash, or its SHA-256 where the manifest
	// has no root hash; TargetRoot the target's signed root hash.
	Source     string
	TargetRoot string
	TargetSize int64
	Next       int
	Piece      int
	// DataOffset is where the data of operation Next (of its piece Piece)
	// starts after the manifest.
	DataOffset uint64
}

func (k Checkpoint) body() string {
	return fmt.Sprintf("format=%d\npayload=%s\nsource=%s\ntarget_root_hash=%s\ntarget_size=%d\nnext_operation=%d\nnext_piece=%d\ndata_offset=%d\n",
		checkpointFormat, k.Payload, k.Source, k.TargetRoot, k.TargetSize, k.Next, k.Piece, k.DataOffset)
}

// Marshal is the checkpoint as kept: key=value lines and their SHA-256, so
// that a torn or damaged file reads as no checkpoint.
func (k Checkpoint) Marshal() []byte {
	b := k.body()
	return []byte(b + "sha256=" + sum([]byte(b)) + "\n")
}

var checkpointKeys = []string{"format", "payload", "source", "target_root_hash", "target_size", "next_operation", "next_piece", "data_offset", "sha256"}

// ParseCheckpoint reads a checkpoint, refusing anything but its exact form.
func ParseCheckpoint(raw []byte) (Checkpoint, error) {
	var k Checkpoint
	lines := strings.Split(string(raw), "\n")
	if len(lines) != len(checkpointKeys)+1 || lines[len(lines)-1] != "" {
		return k, errors.New("delta: not a checkpoint")
	}
	v := map[string]string{}
	for i, key := range checkpointKeys {
		val, ok := strings.CutPrefix(lines[i], key+"=")
		if !ok {
			return k, errors.New("delta: not a checkpoint")
		}
		v[key] = val
	}
	body := strings.Join(lines[:len(checkpointKeys)-1], "\n") + "\n"
	if v["sha256"] != sum([]byte(body)) || v["format"] != strconv.Itoa(checkpointFormat) {
		return k, errors.New("delta: a damaged checkpoint")
	}
	var err error
	k.Payload, k.Source, k.TargetRoot = v["payload"], v["source"], v["target_root_hash"]
	if k.TargetSize, err = strconv.ParseInt(v["target_size"], 10, 64); err != nil || k.TargetSize < 0 {
		return k, errors.New("delta: a damaged checkpoint")
	}
	if k.Next, err = strconv.Atoi(v["next_operation"]); err != nil || k.Next < 0 {
		return k, errors.New("delta: a damaged checkpoint")
	}
	if k.Piece, err = strconv.Atoi(v["next_piece"]); err != nil || k.Piece < 0 {
		return k, errors.New("delta: a damaged checkpoint")
	}
	off, err := strconv.ParseUint(v["data_offset"], 10, 64)
	if err != nil || off > maxPayloadData {
		return k, errors.New("delta: a damaged checkpoint")
	}
	k.DataOffset = off
	if !isHash(k.Payload) || !isHash(k.Source) || !isHash(k.TargetRoot) {
		return k, errors.New("delta: a damaged checkpoint")
	}
	return k, nil
}

// WriteCheckpoint replaces the checkpoint at path atomically: a power cut
// leaves the old one or the new one.
func WriteCheckpoint(path string, k Checkpoint) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(k.Marshal())
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// RemoveCheckpoint removes the checkpoint at path, durably.
func RemoveCheckpoint(path string) error {
	_ = os.Remove(path + ".tmp")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// readError: the payload stopped arriving (a network gone, a bundle
// unmounted). What was written so far stays good for a resume.
type readError struct{ error }

func (e readError) Unwrap() error { return e.error }

// dataOffset is where the data of operation i's piece k starts (all of it
// for i past the last), the operations' data and the pieces of each lying
// one after another (Check).
func dataOffset(ops []Op, i, k int) uint64 {
	var n uint64
	for _, op := range ops[:i] {
		n += op.DataLength
	}
	if i < len(ops) {
		for _, p := range ops[i].Pieces[:min(k, len(ops[i].Pieces))] {
			n += p.Length
		}
	}
	return n
}

// skipPayload moves past n bytes of payload data: by seeking where the
// payload allows it (a file in the mounted bundle, so a streamed install
// downloads none of them), else by reading them.
func skipPayload(r io.Reader, n uint64) error {
	if n == 0 {
		return nil
	}
	if s, ok := r.(io.Seeker); ok {
		_, err := s.Seek(int64(n), io.SeekCurrent)
		return err
	}
	_, err := io.CopyN(io.Discard, r, int64(n))
	return err
}

// checkpointer keeps the checkpoint of one install.
type checkpointer struct {
	path   string
	target Device
	want   Checkpoint
	ops    []Op
	logf   func(string, ...any)
	every  uint64
	// at and last: the bytes written and the time at the last checkpoint;
	// next and piece: how far the target is written whole (done).
	at          uint64
	last        time.Time
	next, piece int
}

// resume starts the checkpoint of an install of m (payloadID: the SHA-256
// of its header and manifest) and, where the one kept is for this very
// install, skips the payload data of the operations it covers; res.Resumed
// is then where the install goes on.
func resume(m *Manifest, payloadID string, c ApplyConfig, payload io.Reader, res *Result, logf func(string, ...any)) (*checkpointer, error) {
	source := m.Source.SHA256
	if c.SourceDesc != nil && m.Source.RootHash != "" {
		source = m.Source.RootHash
	}
	cp := &checkpointer{path: c.Checkpoint, target: c.Target, ops: m.Ops, logf: logf, last: time.Now(), every: c.CheckpointBytes,
		want: Checkpoint{Payload: payloadID, Source: source, TargetRoot: m.Verity.RootHash, TargetSize: c.TargetSize}}
	if cp.every == 0 {
		cp.every = checkpointBytes
	}
	var raw []byte
	err := fs.ErrNotExist
	if cp.path != "" {
		raw, err = os.ReadFile(cp.path)
	}
	k, perr := ParseCheckpoint(raw)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		logf(progressLine, 0, len(m.Ops), 0)
		return cp, nil
	case err != nil || perr != nil:
		logf("the checkpoint is unreadable (%v): starting from the beginning", errors.Join(err, perr))
	case k.Payload != cp.want.Payload || k.Source != cp.want.Source || k.TargetRoot != cp.want.TargetRoot || k.TargetSize != cp.want.TargetSize ||
		k.Next > len(m.Ops) || k.Piece > 0 && (k.Next == len(m.Ops) || k.Piece >= len(m.Ops[k.Next].Pieces)) ||
		k.DataOffset != dataOffset(m.Ops, k.Next, k.Piece):
		logf("the checkpoint is another install's: starting from the beginning")
	default:
		cp.want.Next, cp.want.Piece, cp.want.DataOffset = k.Next, k.Piece, k.DataOffset
		cp.next, cp.piece = k.Next, k.Piece
		if err := skipPayload(payload, k.DataOffset); err != nil {
			err = readError{fmt.Errorf("delta: skipping the data of the operations done: %w", err)}
			cp.failed(err)
			return nil, err
		}
		res.Resumed, res.ResumedPiece, res.Skipped = k.Next, k.Piece, k.DataOffset
		if k.Piece > 0 {
			logf(resumedPieceLine, k.Next, len(m.Ops), k.Piece, len(m.Ops[k.Next].Pieces), k.DataOffset)
		} else {
			logf(resumedLine, k.Next, len(m.Ops), k.DataOffset)
		}
		return cp, nil
	}
	// Gone before anything is written: it would vouch for blocks this
	// install overwrites.
	if err := RemoveCheckpoint(cp.path); err != nil {
		return nil, fmt.Errorf("delta: removing the checkpoint: %w", err)
	}
	logf(progressLine, 0, len(m.Ops), 0)
	return cp, nil
}

// done notes that the operations before next, and the pieces of operation
// next before piece, are written; when one is due (or force), it syncs the
// target and records the checkpoint.
func (cp *checkpointer) done(next, piece int, written uint64, force bool) error {
	cp.next, cp.piece = next, piece
	if !force && written-cp.at < cp.every && time.Since(cp.last) < checkpointEvery {
		return nil
	}
	cp.at, cp.last = written, time.Now()
	if err := cp.record(next, piece); err != nil {
		return err
	}
	cp.logf(progressLine, next, len(cp.ops), written)
	return nil
}

// record syncs the target and records the checkpoint at operation next,
// piece piece.
func (cp *checkpointer) record(next, piece int) error {
	if cp.path == "" {
		return nil
	}
	if err := cp.target.Sync(); err != nil {
		return fmt.Errorf("delta: %w", err)
	}
	k := cp.want
	k.Next, k.Piece, k.DataOffset = next, piece, dataOffset(cp.ops, next, piece)
	if err := WriteCheckpoint(cp.path, k); err != nil {
		// The install goes on; one kept from earlier still holds.
		cp.logf("the checkpoint cannot be written (%v): this install will not resume past operation %d", err, cp.want.Next)
		cp.path = ""
		return nil
	}
	cp.want = k
	return nil
}

// failed ends an install that stopped with err. The payload stopped
// arriving, or the install was asked to stop: the target is all there, and
// what it has whole is recorded, so that the next install downloads
// nothing it already wrote (the last periodic checkpoint may lie seconds
// of data behind). On any other failure the checkpoint goes.
func (cp *checkpointer) failed(err error) {
	line := stoppedLine
	switch {
	case errors.Is(err, ErrStopped):
	case errors.As(err, new(readError)):
		line = stalledLine
	default:
		cp.finished()
		return
	}
	if cp.path != "" && (cp.next > cp.want.Next || cp.next == cp.want.Next && cp.piece > cp.want.Piece) {
		if rerr := cp.record(cp.next, cp.piece); rerr != nil {
			cp.logf("the checkpoint cannot be written (%v)", rerr)
		}
	}
	switch _, serr := os.Stat(cp.path); {
	case cp.path == "" || serr != nil:
		cp.logf(line + "; installing it again starts from the beginning")
	case cp.want.Piece > 0:
		cp.logf(line+"; installing it again resumes at operation %d, piece %d", cp.want.Next, cp.want.Piece)
	default:
		cp.logf(line+"; installing it again resumes at operation %d", cp.want.Next)
	}
}

// finished removes the checkpoint of an install that completed or failed
// its verification.
func (cp *checkpointer) finished() {
	if cp.path == "" {
		return
	}
	if err := RemoveCheckpoint(cp.path); err != nil {
		cp.logf("removing the checkpoint: %v", err)
	}
}

// Progress is what an install's log says of it (ParseLog).
type Progress struct {
	// Done and Total: operations; Resumed: the operation it resumed at.
	Done, Total, Resumed int
	// Stalled: it stopped because the payload stopped arriving (a network
	// error); Stopped: because it was asked to. Installing it again resumes
	// where a checkpoint was kept.
	Stalled, Stopped bool
}

var (
	progressRE = regexp.MustCompile(`progress: operation (\d+) of (\d+),`)
	resumedRE  = regexp.MustCompile(`resumed at operation (\d+) of (\d+)[,:]`)
)

// ParseLog reads an install's progress from the applier's log (complete
// lines only: the applier may be writing the last).
func ParseLog(log []byte) Progress {
	var p Progress
	i := bytes.LastIndexByte(log, '\n')
	if i < 0 {
		return p
	}
	for _, line := range strings.Split(string(log[:i]), "\n") {
		if m := progressRE.FindStringSubmatch(line); m != nil {
			p.Done, _ = strconv.Atoi(m[1])
			p.Total, _ = strconv.Atoi(m[2])
		} else if m := resumedRE.FindStringSubmatch(line); m != nil {
			p.Resumed, _ = strconv.Atoi(m[1])
			p.Done = p.Resumed
			p.Total, _ = strconv.Atoi(m[2])
		} else if strings.Contains(line, stalledLine) {
			p.Stalled = true
		} else if strings.Contains(line, stoppedLine) {
			p.Stopped = true
		}
	}
	return p
}
