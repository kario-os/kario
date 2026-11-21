package pkg

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fuzzKeys are a store and a developer with fixed keys, so that seeds
// signed once stay signed.
func fuzzKeys() (*Store, Developer) {
	store := &Store{Name: "dev", Key: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize))
	cert, _ := store.Certify("example", key.Public().(ed25519.PublicKey))
	return store, Developer{Key: key, Cert: cert}
}

func fuzzPackage(f *testing.F, version string, files map[string]string) []byte {
	_, dev := fuzzKeys()
	dir := f.TempDir()
	files["manifest.json"] = fmt.Sprintf(`{"id": "com.example.notes", "version": %q, "exec": "notes"}`, version)
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			f.Fatal(err)
		}
	}
	raw, err := Build(dir, dev)
	if err != nil {
		f.Fatal(err)
	}
	return raw
}

// sparseBomb is a tar of one sparse file: a few blocks that unpack to a
// gigabyte of zeros.
func sparseBomb(name string) []byte {
	block := func(name string, typ byte, size int) []byte {
		b := make([]byte, 512)
		copy(b, name)
		copy(b[100:], "0000644\x00")
		copy(b[108:], "0000000\x00")
		copy(b[116:], "0000000\x00")
		copy(b[124:], fmt.Sprintf("%011o\x00", size))
		copy(b[136:], "00000000000\x00")
		b[156] = typ
		copy(b[257:], "ustar\x0000")
		copy(b[148:], "        ")
		sum := 0
		for _, c := range b {
			sum += int(c)
		}
		copy(b[148:], fmt.Sprintf("%06o\x00 ", sum))
		return b
	}
	pad := func(b []byte) []byte { return append(b, make([]byte, (512-len(b)%512)%512)...) }
	var recs string
	for _, kv := range [][2]string{{"GNU.sparse.major", "1"}, {"GNU.sparse.minor", "0"}, {"GNU.sparse.name", name}, {"GNU.sparse.realsize", "1073741824"}} {
		l := len(kv[0]) + len(kv[1]) + 3
		for len(fmt.Sprintf("%d %s=%s\n", l, kv[0], kv[1])) != l {
			l = len(fmt.Sprintf("%d %s=%s\n", l, kv[0], kv[1]))
		}
		recs += fmt.Sprintf("%d %s=%s\n", l, kv[0], kv[1])
	}
	m := pad([]byte("1\n0\n0\n"))
	out := append(block("PaxHeaders/"+name, 'x', len(recs)), pad([]byte(recs))...)
	out = append(append(out, block(name, '0', len(m))...), m...)
	return out
}

// A package's files are counted as they unpack: sparse files that fill
// gigabytes from a few blocks are refused before they do.
func TestSparseFilesAreCounted(t *testing.T) {
	var raw []byte
	for i := range 4 {
		raw = append(raw, sparseBomb(fmt.Sprintf("f%d.js", i))...)
	}
	raw = append(raw, make([]byte, 1024)...)
	if _, err := Open(raw, nil); err == nil || !strings.Contains(err.Error(), "unpacked") {
		t.Fatalf("open: %v", err)
	}
	if _, _, _, err := readTar(raw); err == nil || !strings.Contains(err.Error(), "unpacked") {
		t.Fatalf("readTar: %v", err)
	}
}

// FuzzOpen is a package from the store or sideloaded: everything before
// the signatures check out is parsed from what anyone can make.
func FuzzOpen(f *testing.F) {
	store, _ := fuzzKeys()
	roots := store.Roots()
	v1 := fuzzPackage(f, "1.0", map[string]string{"notes": "#!/bin/sh\n", "res/icon.png": "png"})
	v2 := fuzzPackage(f, "1.1", map[string]string{"notes": "#!/bin/sh\necho 2\n", "res/icon.png": "png"})
	delta, err := MakeDelta(v1, v2)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(v1)
	f.Add(delta)
	f.Add(append(sparseBomb("notes"), make([]byte, 1024)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		if p, err := Open(data, roots); err == nil {
			for _, fl := range p.Files {
				if !validPath(fl.Path) || strings.HasPrefix(fl.Path, "META/") {
					t.Fatalf("a package with the file %q", fl.Path)
				}
			}
		}
		if IsDelta(data) {
			DeltaInfo(data)
			ApplyDelta(data, t.TempDir())
		}
	})
}

// FuzzSignedLists are the store's catalog and revocation list: both are
// JSON parsed before their signatures are checked.
func FuzzSignedLists(f *testing.F) {
	store, _ := fuzzKeys()
	roots := store.Roots()
	now := time.Unix(1_800_000_000, 0)
	apps := []map[string]any{
		{"id": "com.example.notes", "version": "1.1", "rollout": 50, "previous": Release{Version: "1.0", Size: 10, Privacy: json.RawMessage(`{"collects":[]}`)}},
		{"id": "com.example.chat", "version": "2.0"},
	}
	cat, _ := store.SignCatalog(apps, now)
	raw, _ := json.Marshal(cat)
	f.Add(raw)
	revs, _ := store.SignRevocations(Revocations{Issued: 2, Revoked: []Revoked{{App: "com.example.bad", Reason: "malware", At: 1}}})
	rv, _ := json.Marshal(revs)
	f.Add(rv)
	f.Fuzz(func(t *testing.T, data []byte) {
		if c, err := VerifyCatalog(data, roots, now); err == nil {
			OfferedApps(c.Apps, 1, func(id string, percent int) bool { return InRollout(42, id, percent) })
		}
		OfferedApps(data, 1, func(string, int) bool { return false })
		VerifyRevocations(data, roots)
	})
}

// FuzzIDs are the resident ID and credit code a developer files with.
func FuzzIDs(f *testing.F) {
	f.Add("11010519491231002X")
	f.Add("91350100M000100Y43")
	f.Fuzz(func(t *testing.T, s string) {
		ValidResidentID(s)
		ValidCreditCode(s)
	})
}
