package ext4

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tree writes files of every shape into dir: empty, one block, many
// blocks, a sparse file with many extents (an extent tree below the inode),
// a hard link, symlinks short and long, a directory with many entries.
func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	r := rand.New(rand.NewSource(1))
	files := map[string][]byte{}
	put := func(name string, data []byte) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		files["/"+name] = data
	}
	random := func(n int) []byte {
		b := make([]byte, n)
		r.Read(b)
		return b
	}
	put("empty", nil)
	put("small.txt", []byte("hello"))
	put("one-block", random(4096))
	put("usr/bin/program", random(300000))
	put("usr/lib/phone/bin/app", random(70000))
	for i := 0; i < 150; i++ {
		put(fmt.Sprintf("usr/share/many/entry-with-a-long-name-%03d", i), random(r.Intn(9000)))
	}
	sparse := filepath.Join(dir, "sparse")
	f, err := os.Create(sparse)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 40*8192+100)
	for i := 0; i < 40; i++ {
		b := random(100)
		copy(want[i*8192:], b)
		if _, err := f.WriteAt(b, int64(i*8192)); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Truncate(int64(len(want)))
	f.Close()
	files["/sparse"] = want
	if err := os.Link(filepath.Join(dir, "usr/bin/program"), filepath.Join(dir, "usr/bin/hardlink")); err != nil {
		t.Fatal(err)
	}
	files["/usr/bin/hardlink"] = files["/usr/bin/program"]
	_ = os.Symlink("program", filepath.Join(dir, "usr/bin/short-link"))
	_ = os.Symlink(strings.Repeat("long/", 30)+"target", filepath.Join(dir, "usr/bin/long-link"))
	return files
}

func mkfs(t *testing.T, dir, img string, opts ...string) {
	t.Helper()
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("no mkfs.ext4")
	}
	args := append([]string{"-q", "-F", "-b", "4096", "-d", dir}, opts...)
	if out, err := exec.Command("mkfs.ext4", append(args, img, "24M")...).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}
}

// debugfsBlocks: the physical blocks of a file as debugfs lists them.
func debugfsBlocks(t *testing.T, img, p string) []uint64 {
	out, err := exec.Command("debugfs", "-R", "blocks "+p, img).Output()
	if err != nil {
		t.Skipf("debugfs: %v", err)
	}
	var blocks []uint64
	for _, w := range strings.Fields(string(out)) {
		n, err := strconv.ParseUint(w, 10, 64)
		if err == nil {
			blocks = append(blocks, n)
		}
	}
	return blocks
}

func check(t *testing.T, img string, files map[string][]byte) {
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fs, err := Open(f)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := f.Stat(); fs.BlockSize != 4096 || fs.Blocks*4096 != uint64(st.Size()) {
		t.Fatalf("block size %d, %d blocks", fs.BlockSize, fs.Blocks)
	}
	seen := map[string]File{}
	if err := fs.Walk(func(file File) error {
		seen[file.Path] = file
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		file, ok := seen[p]
		if !ok || !file.IsRegular() {
			t.Fatalf("%s not walked: %+v", p, file)
		}
		got, err := fs.Read(file)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v, %d bytes of %d", p, err, len(got), len(want))
		}
		var blocks []uint64
		for _, e := range file.Extents {
			for i := uint64(0); i < e.Len; i++ {
				blocks = append(blocks, e.Physical+i)
			}
		}
		// debugfs also lists the extent index and indirect blocks: the data
		// blocks are the rest, in the same order.
		mine := map[uint64]bool{}
		for _, b := range blocks {
			mine[b] = true
		}
		var data []uint64
		dbg := debugfsBlocks(t, img, p)
		for _, b := range dbg {
			if mine[b] {
				data = append(data, b)
			}
		}
		if fmt.Sprint(data) != fmt.Sprint(blocks) || len(dbg)-len(data) > len(blocks)/1000+3 {
			t.Errorf("%s: blocks %v, debugfs says %v", p, blocks, dbg)
		}
	}
	if len(seen["/sparse"].Extents) < 5 {
		t.Errorf("the sparse file has %d extents: no extent tree below the inode", len(seen["/sparse"].Extents))
	}
	if !seen["/usr"].IsDir() || seen["/usr/bin/hardlink"].Inode != seen["/usr/bin/program"].Inode {
		t.Error("directories or hard links")
	}
	if data, err := fs.ReadFile("/usr/share/many/entry-with-a-long-name-007"); err != nil || !bytes.Equal(data, files["/usr/share/many/entry-with-a-long-name-007"]) {
		t.Errorf("ReadFile: %v", err)
	}
	if _, err := fs.ReadFile("/usr/none"); err == nil {
		t.Error("read a file that is not there")
	}
	if _, err := fs.ReadFile("/usr"); err == nil {
		t.Error("read a directory as a file")
	}
}

func TestExtents(t *testing.T) {
	dir, img := t.TempDir(), filepath.Join(t.TempDir(), "fs.img")
	files := tree(t, dir)
	mkfs(t, dir, img)
	check(t, img, files)
}

// Without extents: the ext2/ext3 block map, with indirect blocks.
func TestBlockMap(t *testing.T) {
	dir, img := t.TempDir(), filepath.Join(t.TempDir(), "fs.img")
	files := tree(t, dir)
	big := make([]byte, 5<<20)
	rand.New(rand.NewSource(2)).Read(big)
	_ = os.WriteFile(filepath.Join(dir, "big"), big, 0o644)
	files["/big"] = big
	mkfs(t, dir, img, "-O", "^extents,^64bit,^flex_bg")
	check(t, img, files)
}

func TestNotExt4(t *testing.T) {
	if _, err := Open(bytes.NewReader(make([]byte, 8192))); err == nil {
		t.Fatal("opened zeros")
	}
}
