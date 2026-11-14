package pkg

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (dir string, dev Developer, roots map[string]ed25519.PublicKey) {
	t.Helper()
	state := t.TempDir()
	store, err := DevStore(filepath.Join(state, "store"), filepath.Join(state, "trust"))
	if err != nil {
		t.Fatal(err)
	}
	if dev, err = DevDeveloper(filepath.Join(state, "dev"), "example", store); err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "com.example.notes", "version": "1.0", "exec": "notes"}`), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "notes"), []byte("#!/bin/sh\n"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "res"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "res", "icon.png"), []byte("png"), 0o644))
	return dir, dev, LoadRoots(filepath.Join(state, "trust"))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// rewrite copies a package tar, letting edit change or drop entries and add
// more at the end.
func rewrite(t *testing.T, raw []byte, edit func(h *tar.Header, body []byte) []byte, extra ...func(*tar.Writer)) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		body, _ := io.ReadAll(tr)
		if body = edit(h, body); body == nil {
			continue
		}
		h.Size = int64(len(body))
		must(t, tw.WriteHeader(h))
		_, _ = tw.Write(body)
	}
	for _, f := range extra {
		f(tw)
	}
	must(t, tw.Close())
	return out.Bytes()
}

func keep(_ *tar.Header, b []byte) []byte { return b }

func TestBuildOpen(t *testing.T) {
	dir, dev, roots := setup(t)
	raw, err := Build(dir, dev)
	must(t, err)
	p, err := Open(raw, roots)
	must(t, err)
	if p.ID != "com.example.notes" || p.Version != "1.0" || p.Developer.Name != "example" || len(p.Files) != 3 {
		t.Fatalf("package %+v", p)
	}
	out := filepath.Join(t.TempDir(), "x")
	must(t, p.Extract(out))
	if st, err := os.Stat(filepath.Join(out, "notes")); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("exec mode: %v %v", st.Mode(), err)
	}
	if st, err := os.Stat(filepath.Join(out, "res", "icon.png")); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode: %v %v", st.Mode(), err)
	}
}

func TestOpenRefuses(t *testing.T) {
	dir, dev, roots := setup(t)
	raw, err := Build(dir, dev)
	must(t, err)
	file := func(name string, body []byte, typ byte) func(*tar.Writer) {
		return func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: typ, Linkname: "/etc/passwd"})
			_, _ = tw.Write(body)
		}
	}
	_, otherStore, _ := ed25519.GenerateKey(nil)
	forged := dev
	forged.Cert = (&Store{Name: "dev", Key: otherStore}).Certify("example", dev.Key.Public().(ed25519.PublicKey))
	forgedRaw, err := Build(dir, forged)
	must(t, err)
	for _, tc := range []struct {
		name, want string
		raw        []byte
		roots      map[string]ed25519.PublicKey
	}{
		{"changed file", "differs from the signed list", rewrite(t, raw, func(h *tar.Header, b []byte) []byte {
			if h.Name == "notes" {
				return []byte("#!/bin/sh\nrm -rf /\n")
			}
			return b
		}), roots},
		{"extra file", "differ from the signed list", rewrite(t, raw, keep, file("evil", []byte("x"), tar.TypeReg)), roots},
		{"path escape", "outside the package", rewrite(t, raw, keep, file("../evil", []byte("x"), tar.TypeReg)), roots},
		{"symlink", "only regular files", rewrite(t, raw, keep, file("link", nil, tar.TypeSymlink)), roots},
		{"unsigned", "not signed", rewrite(t, raw, func(h *tar.Header, b []byte) []byte {
			if h.Name == SigPath {
				return nil
			}
			return b
		}), roots},
		{"untrusted store", "untrusted store", raw, map[string]ed25519.PublicKey{}},
		{"forged certificate", "not signed by the store", forgedRaw, roots},
		{"not a tar", "not a package", []byte(strings.Repeat("x", 1024)), roots},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Open(tc.raw, tc.roots)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyDir(t *testing.T) {
	dir, dev, roots := setup(t)
	raw, err := Build(dir, dev)
	must(t, err)
	p, err := Open(raw, roots)
	must(t, err)
	installed := filepath.Join(t.TempDir(), "app")
	must(t, p.Extract(installed))
	must(t, os.WriteFile(filepath.Join(installed, ".install.json"), []byte("{}"), 0o644))
	must(t, VerifyDir(installed, roots, ".install.json"))
	if err := VerifyDir(installed, map[string]ed25519.PublicKey{}, ".install.json"); !errors.Is(err, ErrUntrustedStore) {
		t.Fatalf("untrusted store: %v", err)
	}
	for name, change := range map[string]func(){
		"changed file": func() { must(t, os.WriteFile(filepath.Join(installed, "notes"), []byte("#!/bin/sh\nevil\n"), 0o755)) },
		"changed mode": func() { must(t, os.Chmod(filepath.Join(installed, "res", "icon.png"), 0o755)) },
		"planted file": func() { must(t, os.WriteFile(filepath.Join(installed, "res", "lib.so"), []byte("x"), 0o644)) },
		"missing file": func() { must(t, os.Remove(filepath.Join(installed, "res", "icon.png"))) },
	} {
		fresh := filepath.Join(t.TempDir(), "app")
		must(t, p.Extract(fresh))
		installed = fresh
		change()
		if VerifyDir(installed, roots) == nil {
			t.Errorf("%s: verified", name)
		}
	}
}
