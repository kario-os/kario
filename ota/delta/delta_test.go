package delta

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/kario-os/kario/ota/delta/xz"
	"github.com/kario-os/kario/ota/verity"
)

// program stands for a Go program of the slot: the bytes of this test
// binary, cut to size.
func program(t *testing.T, n int) []byte {
	b, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for len(b) < n {
		b = append(b, b...)
	}
	return append([]byte{}, b[:n]...)
}

// rebuild changes a program as relinking does: the code after a point
// moves, and addresses and a few strings change all over.
func rebuild(b []byte, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	at := len(b) / 5
	out := append(append(append([]byte{}, b[:at]...), []byte("a function the new version added")...), b[at:]...)
	for i := 0; i < 400; i++ {
		p := r.Intn(len(out) - 8)
		binary.LittleEndian.PutUint32(out[p:], binary.LittleEndian.Uint32(out[p:])+uint32(r.Intn(64)))
	}
	return out
}

func writeTree(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// slotImage makes a slot image as phone-verity.bbclass does: ext4 with 4
// KiB blocks from the tree, its hash tree appended, and its descriptor.
func slotImage(t *testing.T, files map[string][]byte, size string) (string, verity.Descriptor) {
	t.Helper()
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("no mkfs.ext4")
	}
	dir, out := t.TempDir(), filepath.Join(t.TempDir(), "slot.ext4.verity")
	writeTree(t, dir, files)
	cmd := exec.Command("mkfs.ext4", "-q", "-F", "-b", "4096", "-d", dir, "-E", "hash_seed=6f3c1d2e-0000-4000-8000-000000000001", out, size)
	cmd.Env = append(os.Environ(), "E2FSPROGS_FAKE_TIME=1767225600")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	salt := sha256.Sum256([]byte(out))
	d := verity.Descriptor{DataBlocks: uint64(len(data)) / BlockSize, HashOffset: uint64(len(data)), Salt: salt[:]}
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

type versions struct {
	src, dst         string
	srcDesc, dstDesc verity.Descriptor
}

// twoVersions: a system and its next build: a program relinked, one
// changed a little, a file added, one removed, a config edited, the rest
// as it was.
func twoVersions(t *testing.T) versions {
	prog := program(t, 1<<20)
	lib := program(t, 300000)[100000:]
	r := rand.New(rand.NewSource(1))
	data := make([]byte, 700000)
	r.Read(data)
	old := map[string][]byte{
		"usr/bin/kario-system":             prog,
		"usr/lib/libfoo.so.1":              lib,
		"usr/share/phone/data.bin":         data,
		"etc/phone.conf":                   []byte("rollout=1\nserver=https://update.example\n"),
		"usr/lib/phone/bin/phone-app-old":  program(t, 200000),
		"usr/lib/phone/bin/phone-app-calc": rebuild(prog[:400000], 9),
	}
	new := map[string][]byte{
		"usr/bin/kario-system":             rebuild(prog, 2),
		"usr/lib/libfoo.so.1":              append(append([]byte{}, lib[:1000]...), append([]byte("patched"), lib[1000:]...)...),
		"usr/share/phone/data.bin":         data,
		"etc/phone.conf":                   []byte("rollout=10\nserver=https://update.example\n"),
		"usr/lib/phone/bin/phone-app-calc": rebuild(prog[:400000], 10),
		// New: a program like the ones beside it.
		"usr/lib/phone/bin/phone-app-snake": rebuild(prog[:380000], 11),
		"usr/share/phone/new.txt":           []byte(strings.Repeat("a new file\n", 500)),
	}
	src, sd := slotImage(t, old, "24M")
	dst, dd := slotImage(t, new, "24M")
	return versions{src, dst, sd, dd}
}

func compressor(t *testing.T) func([]byte) ([]byte, error) {
	if _, err := exec.LookPath("xz"); err == nil {
		return xz.Compress
	}
	t.Log("no xz program: data stored")
	return func(b []byte) ([]byte, error) { return b, nil }
}

func generate(t *testing.T, v versions) ([]byte, *Report) {
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
	rep, err := Generate(GenerateConfig{Source: src, Target: dst, SourceVersion: "0.7.0", TargetVersion: "0.8.0", Compress: compressor(t)}, &out)
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), rep
}

// inactive is the inactive slot: it starts as a copy of the source image
// (as on a phone after the factory), with room for the target.
func inactive(t *testing.T, src string) string {
	img, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "rootfs_b")
	if err := os.WriteFile(p, append(img, make([]byte, 1<<20)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func applyFile(payload []byte, src, part string, srcDesc *verity.Descriptor, dstDesc verity.Descriptor) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := os.OpenFile(part, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	ss, _ := s.Stat()
	ds, _ := d.Stat()
	_, err = Apply(bytes.NewReader(payload), ApplyConfig{Source: s, SourceSize: ss.Size(), SourceDesc: srcDesc, Target: d, TargetSize: ds.Size(), TargetDesc: dstDesc})
	return err
}

func TestGenerateApply(t *testing.T) {
	v := twoVersions(t)
	payload, rep := generate(t, v)
	raw, _ := json.MarshalIndent(rep.Ops, "", " ")
	t.Logf("payload %d bytes for a %d-byte image; adaptive about %d; %s", rep.Payload, rep.Target.Size, rep.Adaptive.Estimate, raw)
	for _, f := range rep.Files {
		t.Logf("  %-8s %7d  %s (%d) %s", f.Method, f.Bytes, f.Path, f.Size, f.From)
	}
	methods := map[string]string{}
	for _, f := range rep.Files {
		methods[f.Path] = f.Method + " " + f.From
	}
	if !strings.HasPrefix(methods["/usr/bin/kario-system"], "bsdiff /usr/bin/kario-system") ||
		!strings.HasPrefix(methods["/usr/lib/phone/bin/phone-app-snake"], "bsdiff /usr/lib/phone/bin/") {
		t.Errorf("not diffed: %v", methods)
	}
	if _, ok := methods["/usr/share/phone/data.bin"]; ok || rep.Ops[SourceCopy] == nil || rep.Ops[Zero] == nil {
		t.Errorf("an unchanged file sent, or no copies and zeros: %v", rep.Ops)
	}
	if _, err := exec.LookPath("xz"); err == nil && rep.Payload*4 > rep.Adaptive.Estimate {
		t.Errorf("payload %d bytes, adaptive about %d", rep.Payload, rep.Adaptive.Estimate)
	}

	part := inactive(t, v.src)
	if err := applyFile(payload, v.src, part, &v.srcDesc, v.dstDesc); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(part)
	want, _ := os.ReadFile(v.dst)
	if !bytes.Equal(got[:len(want)], want) {
		t.Fatal("the slot written is not the target slot image (data and hash tree)")
	}
	// Without the running slot's descriptor the source is hashed whole.
	part2 := inactive(t, v.src)
	if err := applyFile(payload, v.src, part2, nil, v.dstDesc); err != nil {
		t.Fatal(err)
	}
}

// A payload applies only to its source, only as made, and only for the
// target descriptor it was made for.
func TestRefused(t *testing.T) {
	v := twoVersions(t)
	payload, _ := generate(t, v)
	m, err := ReadManifest(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	header := len(payload) - int(m.DataSize())
	part := inactive(t, v.src)
	before, _ := os.ReadFile(part)
	unchanged := func(what string) {
		if now, _ := os.ReadFile(part); !bytes.Equal(now, before) {
			t.Fatalf("%s: the partition was written", what)
		}
	}

	// The wrong source: the running slot is another version.
	if err := applyFile(payload, v.dst, part, &v.dstDesc, v.dstDesc); err == nil || !strings.Contains(err.Error(), "another system version") {
		t.Fatalf("applied to another source: %v", err)
	}
	unchanged("another source")
	if err := applyFile(payload, v.dst, part, nil, v.dstDesc); err == nil {
		t.Fatal("applied to another source, hashed whole")
	}
	unchanged("another source, hashed")
	// The source's root hash agrees but its data does not (as if read
	// without dm-verity): the operations' source hashes catch it.
	bad := inactive(t, v.src)
	img, _ := os.ReadFile(bad)
	img[5*BlockSize+7] ^= 1
	for i := 0; i < len(img)/BlockSize; i++ {
		img[i*BlockSize+100] ^= 0x55
	}
	_ = os.WriteFile(bad, img, 0o644)
	if err := applyFile(payload, bad, part, &v.srcDesc, v.dstDesc); err == nil {
		t.Fatal("applied from a damaged source")
	}

	// Another target descriptor (salt or root hash).
	other := v.dstDesc
	other.RootHash[0] ^= 1
	if err := applyFile(payload, v.src, part, &v.srcDesc, other); err == nil || !strings.Contains(err.Error(), "signed descriptor") {
		t.Fatalf("applied for another descriptor: %v", err)
	}
	unchanged("another descriptor")

	// Damaged data: each operation's data is checked before it is used.
	for _, at := range []int{header, header + int(m.DataSize())/2, len(payload) - 1} {
		damaged := append([]byte{}, payload...)
		damaged[at] ^= 0x40
		if err := applyFile(damaged, v.src, inactive(t, v.src), &v.srcDesc, v.dstDesc); err == nil || !strings.Contains(err.Error(), "hash") {
			t.Fatalf("damaged data at %d applied: %v", at, err)
		}
	}
	if err := applyFile(payload[:len(payload)-10], v.src, inactive(t, v.src), &v.srcDesc, v.dstDesc); err == nil {
		t.Fatal("a truncated payload applied")
	}
	// A manifest changed in place: an operation's result hash.
	js := payload[16:header]
	var mm Manifest
	_ = json.Unmarshal(js, &mm)
	for i := range mm.Ops {
		if mm.Ops[i].Type == SourceBsdiff {
			mm.Ops[i].DstSHA256 = strings.Repeat("0", 64)
			break
		}
	}
	var forged bytes.Buffer
	_ = WriteHeader(&forged, &mm)
	forged.Write(payload[header:])
	if err := applyFile(forged.Bytes(), v.src, inactive(t, v.src), &v.srcDesc, v.dstDesc); err == nil || !strings.Contains(err.Error(), "result does not match") {
		t.Fatalf("a forged result hash applied: %v", err)
	}
	// A manifest whose operations rebuild something else entirely (every
	// hash its own, consistent) still stops at the signed root hash.
	mm2 := *m
	mm2.Target.SHA256 = strings.Repeat("1", 64)
	var forged2 bytes.Buffer
	_ = WriteHeader(&forged2, &mm2)
	forged2.Write(payload[header:])
	if err := applyFile(forged2.Bytes(), v.src, inactive(t, v.src), &v.srcDesc, v.dstDesc); err == nil {
		t.Fatal("a payload for another target image applied")
	}
}

func TestCheck(t *testing.T) {
	d := verity.Descriptor{DataBlocks: 4, HashOffset: 4 * BlockSize, Salt: []byte{1}}
	h := strings.Repeat("ab", 32)
	base := func() *Manifest {
		return &Manifest{Format: Format, BlockSize: BlockSize, Source: Image{Size: 4 * BlockSize, SHA256: h}, Target: Image{Size: 4 * BlockSize, SHA256: h}, Verity: VerityOf(d),
			Ops: []Op{
				{Type: Zero, Dst: []Extent{{0, 1}}, DstSHA256: h},
				{Type: SourceCopy, Src: []Extent{{2, 2}}, Dst: []Extent{{1, 2}}, SrcSHA256: h, DstSHA256: h},
				{Type: Replace, Dst: []Extent{{3, 1}}, Compression: CompressNone, DataLength: BlockSize, DataSHA256: h, DstSHA256: h},
			}}
	}
	if err := base().Check(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(m *Manifest){
		"gap":           func(m *Manifest) { m.Ops = m.Ops[1:] },
		"overlap":       func(m *Manifest) { m.Ops[0].Dst = []Extent{{0, 2}} },
		"outside":       func(m *Manifest) { m.Ops[2].Dst = []Extent{{4, 1}} },
		"source out":    func(m *Manifest) { m.Ops[1].Src = []Extent{{3, 2}} },
		"copy length":   func(m *Manifest) { m.Ops[1].Src = []Extent{{2, 1}} },
		"no hash":       func(m *Manifest) { m.Ops[1].SrcSHA256 = "" },
		"data offset":   func(m *Manifest) { m.Ops[2].DataOffset = 1 },
		"stored length": func(m *Manifest) { m.Ops[2].DataLength = 10 },
		"zero data":     func(m *Manifest) { m.Ops[0].DataLength = 10 },
		"unknown":       func(m *Manifest) { m.Ops[0].Type = "MOVE" },
		"compression":   func(m *Manifest) { m.Ops[2].Compression = "zstd" },
		"target size":   func(m *Manifest) { m.Target.Size = 8 * BlockSize },
		"format":        func(m *Manifest) { m.Format = 2 },
		"empty extent":  func(m *Manifest) { m.Ops[0].Dst = []Extent{{0, 1}, {1, 0}} },
	} {
		m := base()
		change(m)
		if m.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// gptDisk lays out an A/B disk as phone-ab.wks does: a state partition
// holding the slot descriptors, then the slots.
func gptDisk(t *testing.T, slot string, desc verity.Descriptor) string {
	stateDir, state := t.TempDir(), filepath.Join(t.TempDir(), "state.img")
	writeTree(t, stateDir, map[string][]byte{"rootfs_a.verity": desc.Marshal(), "rootfs_b.verity": desc.Marshal(), "boot": []byte("slot=a\n")})
	if b, err := exec.Command("mkfs.ext4", "-q", "-F", "-b", "1024", "-d", stateDir, state, "4M").CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, b)
	}
	stateData, _ := os.ReadFile(state)
	slotData, _ := os.ReadFile(slot)
	slotLen := (int64(len(slotData)) + 2<<20) &^ (1<<20 - 1)
	parts := []struct {
		name  string
		start int64
		data  []byte
		len   int64
	}{
		{"state", 1 << 20, stateData, 4 << 20},
		{"rootfs_a", 5 << 20, slotData, slotLen},
		{"rootfs_b", 5<<20 + slotLen, nil, slotLen},
	}
	disk := make([]byte, 5<<20+2*slotLen+1<<20)
	h := disk[512:]
	copy(h, "EFI PART")
	binary.LittleEndian.PutUint64(h[72:], 2)
	binary.LittleEndian.PutUint32(h[80:], 128)
	binary.LittleEndian.PutUint32(h[84:], 128)
	for i, p := range parts {
		e := disk[1024+128*i:]
		e[0] = 1
		binary.LittleEndian.PutUint64(e[32:], uint64(p.start/512))
		binary.LittleEndian.PutUint64(e[40:], uint64((p.start+p.len)/512-1))
		for j, c := range utf16.Encode([]rune(p.name)) {
			binary.LittleEndian.PutUint16(e[56+2*j:], c)
		}
		copy(disk[p.start:], p.data)
	}
	path := filepath.Join(t.TempDir(), "disk.wic")
	_ = os.WriteFile(path, disk, 0o644)
	return path
}

// An earlier release's A/B disk, as kept with its release (gzip-compressed),
// is a source: the slot and its descriptor from the state partition.
func TestLoadDisk(t *testing.T) {
	v := twoVersions(t)
	disk := gptDisk(t, v.src, v.srcDesc)
	want, err := LoadSlot(v.src, "", "a")
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	raw, _ := os.ReadFile(disk)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	_ = os.WriteFile(disk+".gz", gz.Bytes(), 0o644)
	for _, p := range []string{disk, disk + ".gz"} {
		got, err := LoadSlot(p, "", "a")
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !bytes.Equal(got.Data, want.Data) || got.Desc == nil || !got.Desc.Equal(v.srcDesc) {
			t.Fatalf("%s: another slot (%d bytes)", p, len(got.Data))
		}
	}
	if _, err := LoadSlot(disk, "", "c"); err == nil {
		t.Fatal("a slot the disk does not have")
	}
	// A slot image without a descriptor: the file system's size.
	plain := filepath.Join(t.TempDir(), "plain.ext4")
	img, _ := os.ReadFile(v.src)
	_ = os.WriteFile(plain, img[:v.srcDesc.HashOffset], 0o644)
	got, err := LoadSlot(plain, "", "a")
	if err != nil || got.Desc != nil || !bytes.Equal(got.Data, want.Data) {
		t.Fatalf("plain image: %v", err)
	}
}
