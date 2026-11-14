// Package pkg is the app package format (.phoneapp): a tar of the app's files
// plus META/signature.json, which lists every file with its digest and is
// signed by the developer. The developer's key is certified by a store root
// the device trusts, so a package says who made it and that nothing was
// changed since; updates must come from the same developer key.
package pkg

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	Format    = "phoneapp/1"
	SigPath   = "META/signature.json"
	MaxSize   = 64 << 20 // whole package
	MaxFiles  = 1000
	certLabel = "phoneapp-dev/1\n"
)

var b64 = base64.StdEncoding

type File struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"` // 0644 or 0755
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// DevCert binds a developer name to a key, signed by a store root.
type DevCert struct {
	Name  string `json:"name"`
	Pub   string `json:"pub"`
	Store string `json:"store"`
	Sig   string `json:"sig"`
}

type Signature struct {
	Format    string  `json:"format"`
	App       string  `json:"app"`
	Version   string  `json:"version"`
	Files     []File  `json:"files"`
	Developer DevCert `json:"developer"`
	Sig       string  `json:"sig"`
}

// signedBytes is what the developer signs: the signature file without Sig.
func (s Signature) signedBytes() []byte {
	s.Sig = ""
	raw, _ := json.Marshal(s)
	return append([]byte(Format+"\n"), raw...)
}

func certMsg(store, name, pub string) []byte {
	return []byte(certLabel + store + "\n" + name + "\n" + pub)
}

// Store is a store's signing root (the prototype keeps a development store
// on the device; production keys live in the store backend).
type Store struct {
	Name string
	Key  ed25519.PrivateKey
}

func (s *Store) Public() ed25519.PublicKey { return s.Key.Public().(ed25519.PublicKey) }

// Certify issues a developer certificate.
func (s *Store) Certify(name string, dev ed25519.PublicKey) DevCert {
	pub := b64.EncodeToString(dev)
	return DevCert{Name: name, Pub: pub, Store: s.Name, Sig: b64.EncodeToString(ed25519.Sign(s.Key, certMsg(s.Name, name, pub)))}
}

// Developer signs packages.
type Developer struct {
	Key  ed25519.PrivateKey
	Cert DevCert
}

type manifestHead struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Exec    string `json:"exec"`
}

// Build packs dir (manifest.json at its root; regular files only).
func Build(dir string, dev Developer) ([]byte, error) {
	var files []File
	data := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s: only regular files go into a package", rel)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		st, _ := d.Info()
		mode := uint32(0o644)
		if st.Mode()&0o111 != 0 {
			mode = 0o755
		}
		sum := sha256.Sum256(raw)
		files = append(files, File{Path: rel, Mode: mode, Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])})
		data[rel] = raw
		return nil
	})
	if err != nil {
		return nil, err
	}
	var head manifestHead
	if err := json.Unmarshal(data["manifest.json"], &head); err != nil || head.ID == "" {
		return nil, errors.New("manifest.json with an id is required")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sig := Signature{Format: Format, App: head.ID, Version: head.Version, Files: files, Developer: dev.Cert}
	sig.Sig = b64.EncodeToString(ed25519.Sign(dev.Key, sig.signedBytes()))
	sigRaw, _ := json.MarshalIndent(sig, "", "  ")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	put := func(name string, mode int64, raw []byte) error {
		hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(raw)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(raw)
		return err
	}
	if err := put(SigPath, 0o644, sigRaw); err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := put(f.Path, int64(f.Mode), data[f.Path]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Package is a verified package.
type Package struct {
	ID, Version string
	Developer   DevCert
	Manifest    []byte
	Files       []File
	Data        map[string][]byte
	// sig is META/signature.json as signed, kept with the installed files
	// so they can be checked again (VerifyDir).
	sig []byte
}

// validPath accepts relative, clean, slash-separated paths inside the package.
func validPath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "." &&
		!strings.HasPrefix(p, "../") && p != ".." && !strings.ContainsAny(p, "\\\x00")
}

// Open verifies a package against the trusted store roots (name → key).
func Open(raw []byte, roots map[string]ed25519.PublicKey) (*Package, error) {
	if len(raw) > MaxSize {
		return nil, fmt.Errorf("package is over %d MB", MaxSize>>20)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	data := map[string][]byte{}
	var sigRaw []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not a package: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%s: only regular files are allowed", hdr.Name)
		}
		if !validPath(hdr.Name) {
			return nil, fmt.Errorf("%q: path outside the package", hdr.Name)
		}
		if _, dup := data[hdr.Name]; dup || (hdr.Name == SigPath && sigRaw != nil) {
			return nil, fmt.Errorf("%s: twice in the package", hdr.Name)
		}
		if len(data) >= MaxFiles {
			return nil, fmt.Errorf("more than %d files", MaxFiles)
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxSize+1))
		if err != nil {
			return nil, err
		}
		if hdr.Name == SigPath {
			sigRaw = body
			continue
		}
		if strings.HasPrefix(hdr.Name, "META/") {
			return nil, fmt.Errorf("%s: META/ is reserved", hdr.Name)
		}
		data[hdr.Name] = body
	}
	if sigRaw == nil {
		return nil, errors.New("package is not signed")
	}
	sig, err := verifySig(sigRaw, roots)
	if err != nil {
		return nil, err
	}
	if len(sig.Files) != len(data) {
		return nil, errors.New("files differ from the signed list")
	}
	for _, f := range sig.Files {
		body, ok := data[f.Path]
		sum := sha256.Sum256(body)
		if !ok || int64(len(body)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 || (f.Mode != 0o644 && f.Mode != 0o755) {
			return nil, fmt.Errorf("%s: differs from the signed list", f.Path)
		}
	}
	var head manifestHead
	if err := json.Unmarshal(data["manifest.json"], &head); err != nil {
		return nil, errors.New("manifest.json is missing or invalid")
	}
	if head.ID != sig.App || head.Version != sig.Version {
		return nil, errors.New("manifest does not match the signature")
	}
	return &Package{ID: sig.App, Version: sig.Version, Developer: sig.Developer, Manifest: data["manifest.json"],
		Files: sig.Files, Data: data, sig: sigRaw}, nil
}

// ErrUntrustedStore: the developer certificate comes from a store the device
// does not trust (now: a development store outside developer mode).
var ErrUntrustedStore = errors.New("developer certificate from an untrusted store")

// verifySig checks the developer certificate against the store roots and the
// developer's signature over the file list.
func verifySig(raw []byte, roots map[string]ed25519.PublicKey) (*Signature, error) {
	var sig Signature
	if err := json.Unmarshal(raw, &sig); err != nil || sig.Format != Format {
		return nil, fmt.Errorf("unknown package format (want %s)", Format)
	}
	root, ok := roots[sig.Developer.Store]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUntrustedStore, sig.Developer.Store)
	}
	certSig, err1 := b64.DecodeString(sig.Developer.Sig)
	devPub, err2 := b64.DecodeString(sig.Developer.Pub)
	if err1 != nil || err2 != nil || len(devPub) != ed25519.PublicKeySize ||
		!ed25519.Verify(root, certMsg(sig.Developer.Store, sig.Developer.Name, sig.Developer.Pub), certSig) {
		return nil, errors.New("developer certificate is not signed by the store")
	}
	pkgSig, err := b64.DecodeString(sig.Sig)
	if err != nil || !ed25519.Verify(devPub, sig.signedBytes(), pkgSig) {
		return nil, errors.New("package signature is invalid")
	}
	return &sig, nil
}

// VerifyDir checks an installed package again before it runs: the signature
// kept in dir/META against the roots trusted now, and every file against
// it. Files the package did not sign are refused too (anything planted next
// to the code), except names in extra (the installer's own record).
func VerifyDir(dir string, roots map[string]ed25519.PublicKey, extra ...string) error {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(SigPath)))
	if err != nil {
		return errors.New("installed package has no signature")
	}
	sig, err := verifySig(raw, roots)
	if err != nil {
		return err
	}
	want := map[string]File{}
	for _, f := range sig.Files {
		want[f.Path] = f
	}
	seen := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() || rel == SigPath || slices.Contains(extra, rel) {
			return nil
		}
		f, ok := want[rel]
		if !ok || !d.Type().IsRegular() {
			return fmt.Errorf("%s: not part of the signed package", rel)
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		if int64(len(body)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 || uint32(st.Mode().Perm()) != f.Mode {
			return fmt.Errorf("%s: differs from the signed package", rel)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(sig.Files) {
		return errors.New("files of the signed package are missing")
	}
	return nil
}

// Extract writes the files under dir (which must not exist yet).
func (p *Package) Extract(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	for _, f := range p.Files {
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, p.Data[f.Path], fs.FileMode(f.Mode)); err != nil {
			return err
		}
		if err := os.Chmod(dst, fs.FileMode(f.Mode)); err != nil {
			return err
		}
	}
	sig := filepath.Join(dir, filepath.FromSlash(SigPath))
	if err := os.MkdirAll(filepath.Dir(sig), 0o755); err != nil {
		return err
	}
	return os.WriteFile(sig, p.sig, 0o644)
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := b64.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: bad key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// loadOrCreateKey keeps a key's seed in a file (0600).
func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	if k, err := loadKey(path); err == nil {
		return k, nil
	}
	_, k, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return k, os.WriteFile(path, []byte(b64.EncodeToString(k.Seed())+"\n"), 0o600)
}

// DevStore is the development store: a store root kept in dir, standing in
// for the store backend (like phone-pushgw for the push gateway). Its public
// key goes into trustDir, which is what developer mode on a device means.
func DevStore(dir, trustDir string) (*Store, error) {
	k, err := loadOrCreateKey(filepath.Join(dir, "store.key"))
	if err != nil {
		return nil, err
	}
	s := &Store{Name: "dev", Key: k}
	if err := os.MkdirAll(trustDir, 0o755); err != nil {
		return nil, err
	}
	return s, os.WriteFile(filepath.Join(trustDir, s.Name+".pub"), []byte(b64.EncodeToString(s.Public())+"\n"), 0o644)
}

// DevDeveloper is a developer key in dir certified by the store.
func DevDeveloper(dir, name string, s *Store) (Developer, error) {
	k, err := loadOrCreateKey(filepath.Join(dir, name+".key"))
	if err != nil {
		return Developer{}, err
	}
	return Developer{Key: k, Cert: s.Certify(name, k.Public().(ed25519.PublicKey))}, nil
}

// LoadRoots reads <store>.pub files (base64 Ed25519 keys) from dirs.
func LoadRoots(dirs ...string) map[string]ed25519.PublicKey {
	out := map[string]ed25519.PublicKey{}
	for _, d := range dirs {
		paths, _ := filepath.Glob(filepath.Join(d, "*.pub"))
		for _, p := range paths {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			k, err := b64.DecodeString(strings.TrimSpace(string(raw)))
			if err == nil && len(k) == ed25519.PublicKeySize {
				out[strings.TrimSuffix(filepath.Base(p), ".pub")] = k
			}
		}
	}
	return out
}
