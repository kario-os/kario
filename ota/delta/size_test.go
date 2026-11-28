package delta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kario-os/kario/ota/verity"
)

// The fixed synthetic system of the payload size test: made from seeds
// alone, so that a change to the generator shows as a change of size.

// synthCode is a stand-in for a program built from funcs: instruction
// words drawn from a skewed vocabulary, and calls whose relative offsets
// (as an ARM64 BL encodes them) change wherever the functions move.
type synthCode struct {
	funcs [][]uint32 // a word below 1<<20 is a call to that function
	vocab []uint32
}

func newSynthCode(seed int64, funcs, words int) *synthCode {
	r := rand.New(rand.NewSource(seed))
	s := &synthCode{vocab: make([]uint32, 700)}
	for i := range s.vocab {
		s.vocab[i] = r.Uint32() | 1<<31
	}
	zipf := rand.NewZipf(r, 1.2, 4, uint64(len(s.vocab)-1))
	for f := 0; f < funcs; f++ {
		body := make([]uint32, 8+r.Intn(2*words/funcs))
		for i := range body {
			if r.Intn(9) == 0 {
				body[i] = uint32(r.Intn(funcs))
			} else {
				body[i] = s.vocab[zipf.Uint64()]
			}
		}
		s.funcs = append(s.funcs, body)
	}
	return s
}

func (s *synthCode) bytes(strs []string) []byte {
	at := make([]int, len(s.funcs))
	n := 0
	for i, f := range s.funcs {
		at[i], n = n, n+len(f)
	}
	out := []byte("\x7fELF\x02\x01\x01\x00")
	for i, f := range s.funcs {
		for j, w := range f {
			if w < 1<<20 {
				w = 0x94000000 | uint32(at[w%uint32(len(at))]-(at[i]+j))&0x03FFFFFF
			}
			out = binary.LittleEndian.AppendUint32(out, w)
		}
	}
	for _, str := range strs {
		out = append(append(out, str...), 0)
	}
	return out
}

// relink is the next build: a few functions edited, one added near the
// start (every later function moves), as a change to a package all
// programs link does.
func (s *synthCode) relink(seed int64) *synthCode {
	r := rand.New(rand.NewSource(seed))
	n := &synthCode{vocab: s.vocab}
	for _, f := range s.funcs {
		n.funcs = append(n.funcs, append([]uint32{}, f...))
	}
	for i := 0; i < len(n.funcs)/40+1; i++ {
		f := n.funcs[r.Intn(len(n.funcs))]
		for j := 0; j < 4; j++ {
			f[r.Intn(len(f))] = s.vocab[r.Intn(len(s.vocab))]
		}
	}
	add := make([]uint32, 300)
	for i := range add {
		add[i] = s.vocab[r.Intn(60)]
	}
	at := len(n.funcs) / 5
	n.funcs = append(n.funcs[:at], append([][]uint32{add}, n.funcs[at:]...)...)
	for _, f := range n.funcs {
		for j, w := range f {
			if w < 1<<20 && int(w) >= at {
				f[j] = w + 1
			}
		}
	}
	return n
}

func synthStrings(seed int64, n int) []string {
	r := rand.New(rand.NewSource(seed))
	words := strings.Fields("system update slot verity payload operation install resume network metered settings shell renderer notification wifi mobile battery charger kario phone store app package version")
	out := make([]string, n)
	for i := range out {
		var b strings.Builder
		for j := 0; j < 2+r.Intn(6); j++ {
			fmt.Fprintf(&b, "%s.", words[r.Intn(len(words))])
		}
		out[i] = b.String() + fmt.Sprint(r.Intn(1000))
	}
	return out
}

// synthConfig is a small text file: a locale catalogue or a settings file.
func synthConfig(r *rand.Rand, words []string) []byte {
	var b bytes.Buffer
	for i, n := 0, 20+r.Intn(80); i < n; i++ {
		fmt.Fprintf(&b, "%s = %q\n", words[r.Intn(len(words))], words[r.Intn(len(words))])
	}
	return b.Bytes()
}

// synthVersions is the fixed system and its next build: the system
// program and two apps relinked, an app added, small files edited, added
// and removed, data that stays.
func synthVersions(t *testing.T) versions {
	strs := synthStrings(1, 3000)
	sys, app1, app2 := newSynthCode(2, 9000, 1400000), newSynthCode(3, 1200, 160000), newSynthCode(4, 1000, 120000)
	data := make([]byte, 900<<10)
	rand.New(rand.NewSource(5)).Read(data)
	old := map[string][]byte{
		"usr/bin/kario-system":            sys.bytes(strs),
		"usr/lib/phone/bin/phone-app-a":   app1.bytes(strs[:900]),
		"usr/lib/phone/bin/phone-app-b":   app2.bytes(strs[400:1200]),
		"usr/share/phone/firmware.bin":    data,
		"usr/lib/phone/bin/phone-app-old": newSynthCode(6, 300, 30000).bytes(strs[:50]),
	}
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 900; i++ {
		old[fmt.Sprintf("usr/share/phone/locale/%03d/%s.json", i/30, strs[i])] = synthConfig(r, strs)
	}
	new := map[string][]byte{}
	for k, v := range old {
		new[k] = v
	}
	strs2 := append(append([]string{}, strs...), "a string the new build added")
	new["usr/bin/kario-system"] = sys.relink(8).bytes(strs2)
	new["usr/lib/phone/bin/phone-app-a"] = app1.relink(9).bytes(strs2[:900])
	new["usr/lib/phone/bin/phone-app-b"] = app2.relink(10).bytes(strs2[400:1200])
	new["usr/lib/phone/bin/phone-app-snake"] = newSynthCode(11, 900, 110000).bytes(strs2[100:700])
	delete(new, "usr/lib/phone/bin/phone-app-old")
	r = rand.New(rand.NewSource(12))
	keys := make([]string, 0, len(old))
	for k := range old {
		if strings.Contains(k, "/locale/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for i, k := range keys {
		switch {
		case i%25 == 0:
			new[k] = append(append([]byte{}, new[k]...), synthConfig(r, strs2)[:40]...)
		case i%97 == 0:
			delete(new, k)
		}
	}
	for i := 0; i < 30; i++ {
		new[fmt.Sprintf("usr/share/phone/locale/new/%s.json", strs2[2000+i])] = synthConfig(r, strs2)
	}
	src, sd := fixedSlotImage(t, old)
	dst, dd := fixedSlotImage(t, new)
	return versions{src, dst, sd, dd}
}

// fixedSlotImage is slotImage with everything mkfs.ext4 would draw at
// random fixed, and the files written in order.
func fixedSlotImage(t *testing.T, files map[string][]byte) (string, verity.Descriptor) {
	t.Helper()
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("no mkfs.ext4")
	}
	dir, out := t.TempDir(), filepath.Join(t.TempDir(), "slot.ext4.verity")
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		writeTree(t, dir, map[string][]byte{n: files[n]})
	}
	cmd := exec.Command("mkfs.ext4", "-q", "-F", "-b", "4096", "-U", "6f3c1d2e-0000-4000-8000-000000000002", "-d", dir,
		"-E", "hash_seed=6f3c1d2e-0000-4000-8000-000000000001", out, "32M")
	cmd.Env = append(os.Environ(), "E2FSPROGS_FAKE_TIME=1767225600")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, b)
	}
	// mkfs.ext4 -d copies each file's times, which writing it just set;
	// the inodes it used (sequentially, after the reserved ones) get fixed
	// times instead.
	head, err := exec.Command("dumpe2fs", "-h", out).Output()
	if err != nil {
		t.Skipf("dumpe2fs: %v", err)
	}
	var count, free int
	for _, l := range strings.Split(string(head), "\n") {
		_, _ = fmt.Sscanf(l, "Inode count: %d", &count)
		_, _ = fmt.Sscanf(l, "Free inodes: %d", &free)
	}
	var cmds strings.Builder
	for ino := 2; ino <= count-free; ino++ {
		if ino == 2 || ino >= 11 {
			for _, f := range []string{"atime", "mtime", "ctime"} {
				fmt.Fprintf(&cmds, "sif <%d> %s 20260101000000\n", ino, f)
			}
		}
	}
	script := filepath.Join(t.TempDir(), "times")
	_ = os.WriteFile(script, []byte(cmds.String()), 0o644)
	dbg := exec.Command("debugfs", "-w", "-f", script, out)
	dbg.Env = cmd.Env
	if b, err := dbg.CombinedOutput(); err != nil {
		t.Fatalf("debugfs: %v: %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	d := verity.Descriptor{DataBlocks: uint64(len(data)) / BlockSize, HashOffset: uint64(len(data)), Salt: []byte("kario-size-test")}
	h := verity.NewHasher(d.Salt, d.DataBlocks)
	_, _ = h.Write(data)
	tree, root, err := h.Tree()
	if err != nil {
		t.Fatal(err)
	}
	d.RootHash = root
	if err := os.WriteFile(out, append(data, tree...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out+".desc", d.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}
	return out, d
}

// sizeBound is the most the fixed system's payload may take (format 2):
// 310465 bytes with xz 5.2.4 and e2fsprogs 1.45.6 (format 1: 309328), and
// room for other versions of the tools.
const sizeBound = 330000

// The payload of the fixed synthetic system stays under its bound, and
// applies.
func TestPayloadSize(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("no xz program: the payload's size depends on it")
	}
	v := synthVersions(t)
	src, _ := LoadSlot(v.src, "", "a")
	dst, _ := LoadSlot(v.dst, "", "a")
	var out bytes.Buffer
	rep, err := Generate(GenerateConfig{Source: src, Target: dst, Format: 2}, &out)
	if err != nil {
		t.Fatal(err)
	}
	payload := out.Bytes()
	var parts []string
	for typ, st := range rep.Ops {
		parts = append(parts, fmt.Sprintf("%s %d (%d blocks, %d bytes)", typ, st.Count, st.Blocks, st.Data))
	}
	sort.Strings(parts)
	t.Logf("payload %d bytes for a %d-byte image (adaptive about %d): %s", len(payload), rep.Target.Size, rep.Adaptive.Estimate, strings.Join(parts, ", "))
	for _, f := range rep.Files[:min(6, len(rep.Files))] {
		t.Logf("  %-8s %7d  %s (%d) %s", f.Method, f.Bytes, f.Path, f.Size, f.From)
	}
	if len(payload) > sizeBound {
		t.Errorf("the payload is %d bytes, over its bound of %d", len(payload), sizeBound)
	}
	part := inactive(t, v.src)
	if err := applyFile(payload, v.src, part, &v.srcDesc, v.dstDesc); err != nil {
		t.Fatal(err)
	}
}
