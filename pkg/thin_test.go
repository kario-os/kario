package pkg

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fatPackage is setup's app with libraries for both architectures.
func fatPackage(t *testing.T, version string) (dir string, dev Developer, roots Roots, raw []byte) {
	t.Helper()
	dir, dev, roots = setup(t)
	for _, a := range Arches {
		must(t, os.MkdirAll(filepath.Join(dir, LibDir, a), 0o755))
		must(t, os.WriteFile(filepath.Join(dir, LibDir, a, "codec.so"), []byte("ELF for "+a+" "+version), 0o644))
	}
	raw, err := Build(dir, dev)
	must(t, err)
	return dir, dev, roots, raw
}

func paths(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestLibArch(t *testing.T) {
	for p, want := range map[string]string{
		"lib/arm64/codec.so": ArchARM64, "lib/x86_64/a/b.so": ArchX86_64,
		"lib/riscv64/codec.so": "", "lib/arm64": "", "lib/arm64/": "", "res/lib/arm64/x.so": "", "main.js": "",
	} {
		if got := LibArch(p); got != want {
			t.Errorf("LibArch(%q) = %q, want %q", p, got, want)
		}
	}
	if !Thinnable("lib/x86_64/codec.so", ArchARM64) || Thinnable("lib/arm64/codec.so", ArchARM64) || Thinnable("lib/x86_64/codec.so", "") ||
		Thinnable("lib/x86_64/codec.so", "riscv64") || Thinnable("main.js", ArchARM64) {
		t.Fatal("Thinnable")
	}
}

// A package thinned for a device verifies against the developer's
// signature on that device and keeps only that architecture's libraries;
// it is the same bytes each time, with the digest ThinnedFor gives. A
// missing library of the device's own architecture, or any other missing
// or changed file, still fails, and so does a thinned package where all
// of it is required (Open).
func TestThin(t *testing.T) {
	t.Run("verifies", func(t *testing.T) {
		_, _, roots, raw := fatPackage(t, "1.0")
		thin, err := Thin(raw, ArchARM64)
		must(t, err)
		again, _ := Thin(raw, ArchARM64)
		if len(thin) >= len(raw) || !bytes.Equal(thin, again) {
			t.Fatalf("thinned %d of %d bytes; deterministic %v", len(thin), len(raw), bytes.Equal(thin, again))
		}
		p, err := OpenFor(thin, roots, ArchARM64)
		must(t, err)
		if got := paths(p.Files); !slices.Contains(got, "lib/arm64/codec.so") || slices.Contains(got, "lib/x86_64/codec.so") {
			t.Fatalf("files %v", got)
		}
		if _, err := Open(thin, roots); err == nil || !strings.Contains(err.Error(), "lib/x86_64/codec.so: differs from the signed list") {
			t.Fatalf("Open of a thinned package: %v", err)
		}
		// The whole package on a device: the other architecture is checked
		// and left out.
		p, err = OpenFor(raw, roots, ArchARM64)
		must(t, err)
		if slices.Contains(paths(p.Files), "lib/x86_64/codec.so") || p.Data["lib/x86_64/codec.so"] != nil {
			t.Fatalf("files %v", paths(p.Files))
		}
		th, err := ThinnedFor(raw)
		must(t, err)
		sum := sha256.Sum256(thin)
		if th[ArchARM64] != (Thinned{len(thin), hex.EncodeToString(sum[:])}) || th[ArchX86_64].Size == 0 {
			t.Fatalf("ThinnedFor %+v", th)
		}
		installed := filepath.Join(t.TempDir(), "app")
		must(t, p.Extract(installed))
		must(t, VerifyDirFor(installed, ArchARM64, roots))
		if err := VerifyDir(installed, roots); err == nil {
			t.Fatal("VerifyDir took a thinned installation as whole")
		}
		must(t, os.Remove(filepath.Join(installed, "lib", "arm64", "codec.so")))
		if err := VerifyDirFor(installed, ArchARM64, roots); err == nil {
			t.Fatal("verified without the device's own library")
		}
	})
	t.Run("refuses", func(t *testing.T) {
		_, _, roots, raw := fatPackage(t, "1.0")
		drop := func(name string) []byte {
			return rewrite(t, raw, func(h *tar.Header, b []byte) []byte {
				if h.Name == name {
					return nil
				}
				return b
			})
		}
		for _, c := range []struct {
			name string
			raw  []byte
			arch string
		}{
			{"own architecture's library missing", drop("lib/arm64/codec.so"), ArchARM64},
			{"another file missing", drop("res/icon.png"), ArchARM64},
			{"the manifest missing", drop("manifest.json"), ArchARM64},
			{"another architecture's library changed", rewrite(t, raw, func(h *tar.Header, b []byte) []byte {
				if h.Name == "lib/x86_64/codec.so" {
					return []byte("ELF for x86_64 1.0!")
				}
				return b
			}), ArchARM64},
			{"no architecture: nothing may be missing", drop("lib/x86_64/codec.so"), ""},
			{"an unknown architecture: nothing may be missing", drop("lib/x86_64/codec.so"), "riscv64"},
		} {
			if _, err := OpenFor(c.raw, roots, c.arch); err == nil {
				t.Errorf("%s: verified", c.name)
			}
		}
		if p, err := OpenFor(drop("lib/x86_64/codec.so"), roots, ArchARM64); err != nil || len(p.Files) != 4 {
			t.Fatalf("thinned by hand: %v", err)
		}
	})
	t.Run("delta", func(t *testing.T) {
		dir, dev, roots, v1 := fatPackage(t, "1.0")
		must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "com.example.notes", "version": "1.1", "exec": "notes"}`), 0o644))
		for _, a := range Arches {
			must(t, os.WriteFile(filepath.Join(dir, LibDir, a, "codec.so"), []byte("ELF for "+a+" 1.1"), 0o644))
		}
		v2, err := Build(dir, dev)
		must(t, err)
		p1, err := OpenFor(mustThin(t, v1, ArchARM64), roots, ArchARM64)
		must(t, err)
		installed := filepath.Join(t.TempDir(), "app")
		must(t, p1.Extract(installed))
		delta, err := MakeDelta(v1, v2)
		must(t, err)
		want := mustThin(t, v2, ArchARM64)
		// The store's thinned delta and a whole one give the same package.
		for name, d := range map[string][]byte{"thinned": mustThin(t, delta, ArchARM64), "whole": delta} {
			if !IsDelta(d) {
				t.Fatalf("%s: not a delta", name)
			}
			got, err := ApplyDeltaFor(d, installed, ArchARM64)
			must(t, err)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s delta: not the thinned package", name)
			}
			if _, err := OpenFor(got, roots, ArchARM64); err != nil {
				t.Fatalf("%s delta: %v", name, err)
			}
		}
		if _, err := ApplyDelta(mustThin(t, delta, ArchARM64), installed); err == nil {
			t.Fatal("a whole package rebuilt from a thinned installation")
		}
	})
}

// A signed list that names a file twice cannot make room for a file it
// does not name.
func TestOpenSignedTwice(t *testing.T) {
	dir, dev, roots := setup(t)
	raw, err := Build(dir, dev)
	must(t, err)
	var sig Signature
	raw = rewrite(t, raw, func(h *tar.Header, b []byte) []byte {
		if h.Name != SigPath {
			return b
		}
		must(t, json.Unmarshal(b, &sig))
		sig.Files = append(sig.Files, sig.Files[0])
		sig.Sig = b64.EncodeToString(ed25519.Sign(dev.Key, sig.signedBytes()))
		out, _ := json.Marshal(sig)
		return out
	}, func(tw *tar.Writer) {
		must(t, tw.WriteHeader(&tar.Header{Name: "unlisted.sh", Mode: 0o755, Size: 1, Typeflag: tar.TypeReg}))
		_, _ = tw.Write([]byte("x"))
	})
	if _, err := Open(raw, roots); err == nil || !strings.Contains(err.Error(), "twice in the signed list") {
		t.Fatalf("got %v", err)
	}
}

func mustThin(t *testing.T, raw []byte, arch string) []byte {
	t.Helper()
	out, err := Thin(raw, arch)
	must(t, err)
	return out
}
