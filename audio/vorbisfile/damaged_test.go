package vorbisfile

import (
	"os"
	"path/filepath"
	"testing"
)

// The decoder panics on this page as it looks for the last granule
// position; Open says the file is damaged instead.
func TestDamagedFileIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "damaged.ogg")
	if err := os.WriteFile(p, []byte("OggS\x00\x04D\xac\x00\x00\x00\x00\x00\x00~\xa47_\x02\x00\x00\x00_\x80\x00\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err := Open(p); err == nil {
		f.Close()
		t.Fatal("a damaged file opened")
	}
}
