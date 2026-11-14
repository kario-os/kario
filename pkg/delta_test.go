package pkg

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDelta(t *testing.T) {
	dir, dev, roots := setup(t)
	must(t, os.WriteFile(filepath.Join(dir, "big.dat"), bytes.Repeat([]byte("unchanged "), 20000), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "old.txt"), []byte("dropped in 1.1"), 0o644))
	v1, err := Build(dir, dev)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "com.example.notes", "version": "1.1", "exec": "notes"}`), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "notes"), []byte("#!/bin/sh\necho 1.1\n"), 0o755))
	must(t, os.Remove(filepath.Join(dir, "old.txt")))
	must(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("added in 1.1"), 0o644))
	v2, err := Build(dir, dev)
	must(t, err)

	delta, err := MakeDelta(v1, v2)
	must(t, err)
	if !IsDelta(delta) || IsDelta(v2) {
		t.Fatal("IsDelta is wrong")
	}
	if len(delta) > len(v2)/4 {
		t.Fatalf("delta is %d bytes for a %d-byte package", len(delta), len(v2))
	}
	if app, from, to, err := DeltaInfo(delta); err != nil || app != "com.example.notes" || from != "1.0" || to != "1.1" {
		t.Fatalf("DeltaInfo: %s %s %s %v", app, from, to, err)
	}
	p1, err := Open(v1, roots)
	must(t, err)
	installed := filepath.Join(t.TempDir(), "notes")
	must(t, p1.Extract(installed))

	full, err := ApplyDelta(delta, installed)
	must(t, err)
	p2, err := Open(full, roots)
	if err != nil {
		t.Fatalf("rebuilt package: %v", err)
	}
	if p2.Version != "1.1" || string(p2.Data["new.txt"]) != "added in 1.1" || p2.Data["old.txt"] != nil || len(p2.Data["big.dat"]) != 200000 {
		t.Fatalf("rebuilt package has the wrong files: %v", p2.Files)
	}

	// The installed copy is not the version the delta starts from.
	must(t, os.WriteFile(filepath.Join(installed, "big.dat"), []byte("changed by someone"), 0o644))
	if _, err := ApplyDelta(delta, installed); err == nil {
		t.Fatal("applied onto a modified installation")
	}
	must(t, os.WriteFile(filepath.Join(installed, "big.dat"), bytes.Repeat([]byte("unchanged "), 20000), 0o644))

	// A file changed in transit: the rebuilt package fails verification.
	tampered := rewrite(t, delta, func(h *tar.Header, body []byte) []byte {
		if h.Name == "notes" {
			return []byte("#!/bin/sh\nrm -rf /\n")
		}
		return body
	})
	if full, err := ApplyDelta(tampered, installed); err == nil {
		if _, err := Open(full, roots); err == nil {
			t.Fatal("a tampered delta verified")
		}
	}
	// A file the new version does not list.
	extra := rewrite(t, delta, keep, func(tw *tar.Writer) {
		must(t, tw.WriteHeader(&tar.Header{Name: "planted.sh", Mode: 0o755, Size: 1, Typeflag: tar.TypeReg}))
		_, _ = tw.Write([]byte("x"))
	})
	if _, err := ApplyDelta(extra, installed); err == nil {
		t.Fatal("a delta with an unsigned file applied")
	}
}
