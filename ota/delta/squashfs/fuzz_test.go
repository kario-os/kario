package squashfs

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// FuzzImage is a bundle's image as the applier finds it: whatever it
// holds, finding and streaming a file fails or stays inside it.
func FuzzImage(f *testing.F) {
	if _, err := exec.LookPath("mksquashfs"); err == nil {
		dir, out := f.TempDir(), filepath.Join(f.TempDir(), "img")
		_ = os.WriteFile(filepath.Join(dir, "rootfs.kdelta"), bytes.Repeat([]byte("KDLT payload "), 20000), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "delta-hook"), []byte("#!/bin/sh\n"), 0o644)
		if exec.Command("mksquashfs", dir, out, "-all-root", "-noappend", "-no-progress", "-no-xattrs", "-b", "4096").Run() == nil {
			if b, err := os.ReadFile(out); err == nil {
				f.Add(b)
			}
		}
	}
	f.Add(make([]byte, 96))
	f.Fuzz(func(t *testing.T, img []byte) {
		fs, err := Open(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			return
		}
		file, err := fs.Lookup("rootfs.kdelta")
		if err != nil || file.Size > 1<<20 {
			return
		}
		s, err := NewStream(fs, file, bytes.NewReader(img), bytes.NewReader(make([]byte, file.Size)), make([]byte, Align))
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, struct{ io.Reader }{s})
		if n > file.Size {
			t.Fatalf("streamed %d bytes of a %d-byte file", n, file.Size)
		}
	})
}
