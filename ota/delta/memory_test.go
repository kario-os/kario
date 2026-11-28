package delta

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/kario-os/kario/ota/verity"
)

// memChild names the directory of the payload a child of the memory test
// applies.
const memChild = "KARIO_DELTA_MEMORY_TEST"

// bigFile: a program of the size of the largest the phone has (kario-system
// is about 28 MB), larger than the memory the test allows the applier.
const bigFile = 32 << 20

// countedAt counts what is read through it.
type countedAt struct {
	r io.ReaderAt
	n int64
}

func (c *countedAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n += int64(n)
	return n, err
}

// The applier patches a large program without holding it: the old file is
// read from the source slot as the patch needs it and the new one is
// written as it is made, so its peak memory (a process of its own, as on
// the phone) stays below the program's size; the slot comes out the
// target byte for byte.
func TestBigFilePatchMemory(t *testing.T) {
	if dir := os.Getenv(memChild); dir != "" {
		memoryChild(t, dir)
		return
	}
	t.Parallel()
	if testing.Short() {
		t.Skip("a 32 MiB program to diff")
	}
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("no /proc/self/status to read the applier's peak memory from")
	}
	prog := program(t, bigFile)
	src, srcDesc := slotImage(t, map[string][]byte{"usr/bin/kario-system": prog, "etc/phone.conf": []byte("rollout=1\n")}, "48M")
	dst, dstDesc := slotImage(t, map[string][]byte{"usr/bin/kario-system": rebuild(prog, 3), "etc/phone.conf": []byte("rollout=10\n")}, "48M")
	v := versions{src, dst, srcDesc, dstDesc}
	payload := generateWith(t, v, Format, DefaultPiece)
	m, in := pieceOps(t, payload)
	pieces := 0
	for _, i := range in {
		pieces = max(pieces, len(m.Ops[i].Pieces))
	}
	if pieces < bigFile/DefaultPiece {
		t.Fatalf("the program's patch in %d pieces", pieces)
	}
	dir := t.TempDir()
	part := inactive(t, src)
	for name, b := range map[string][]byte{"payload": payload, "source.desc": srcDesc.Marshal(), "target.desc": dstDesc.Marshal()} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, p := range map[string]string{"source": src, "target": part} {
		if err := os.Symlink(p, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBigFilePatchMemory$", "-test.v")
	cmd.Env = append(os.Environ(), memChild+"="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the applier: %v\n%s", err, out)
	}
	said := regexp.MustCompile(`source read (\d+) bytes, peak (\d+) kB`).FindSubmatch(out)
	if said == nil {
		t.Fatalf("the applier said:\n%s", out)
	}
	sourceRead, _ := strconv.ParseInt(string(said[1]), 10, 64)
	peak, _ := strconv.ParseInt(string(said[2]), 10, 64)
	peak <<= 10
	want, _ := os.ReadFile(dst)
	got, _ := os.ReadFile(part)
	if !bytes.Equal(got[:len(want)], want) {
		t.Fatal("the slot written is not the target")
	}
	t.Logf("a %d MiB program in %d pieces of %d MiB (payload %d bytes): applied with a peak of %.1f MiB, %.1f MiB of the source read for a %.1f MiB image",
		bigFile>>20, pieces, DefaultPiece>>20, len(payload), float64(peak)/(1<<20), float64(sourceRead)/(1<<20), float64(len(want))/(1<<20))
	if peak >= bigFile*3/4 {
		t.Fatalf("peak memory %d bytes patching a %d-byte program", peak, bigFile)
	}
}

// memoryChild applies the payload in dir as phone-ota-delta does: the
// payload read as a stream, the slots as files.
func memoryChild(t *testing.T, dir string) {
	at := func(name string) string { return filepath.Join(dir, name) }
	desc := func(name string) verity.Descriptor {
		raw, err := os.ReadFile(at(name))
		if err != nil {
			t.Fatal(err)
		}
		d, err := verity.ParseDescriptor(raw)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	srcDesc, dstDesc := desc("source.desc"), desc("target.desc")
	p, err := os.Open(at("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	s, err := os.Open(at("source"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := os.OpenFile(at("target"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ss, _ := s.Stat()
	ds, _ := d.Stat()
	src := &countedAt{r: s}
	if _, err := Apply(bufio.NewReaderSize(p, 1<<20), ApplyConfig{Source: src, SourceSize: ss.Size(), SourceDesc: &srcDesc,
		Target: d, TargetSize: ds.Size(), TargetDesc: dstDesc}); err != nil {
		t.Fatal(err)
	}
	// The process's own peak: getrusage's would be the test's, which the
	// child had until it ran this binary.
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	hwm := regexp.MustCompile(`(?m)^VmHWM:\s+(\d+) kB`).FindSubmatch(status)
	if hwm == nil {
		t.Fatalf("no VmHWM in %s", status)
	}
	fmt.Printf("source read %d bytes, peak %s kB\n", src.n, hwm[1])
}
