// Package pkg is the app package format (.phoneapp): a tar of the app's files
// plus META/signature.json, which lists every file with its digest and is
// signed by the developer. The developer's key is certified by a store root
// the device trusts, so a package says who made it and that nothing was
// changed since; updates must come from the same developer key.
package pkg

import (
	"archive/tar"
	"bytes"
	"crypto"
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

	"github.com/kario-os/kario/keyset"
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

// DevCert binds a developer name to a key, signed by a store root; Key is
// the ID of the root's key that signed (keyset.ID; none from a store
// before key IDs).
type DevCert struct {
	Name  string `json:"name"`
	Pub   string `json:"pub"`
	Store string `json:"store"`
	Sig   string `json:"sig"`
	Key   string `json:"key,omitempty"`
}

type Signature struct {
	Format    string  `json:"format"`
	App       string  `json:"app"`
	Version   string  `json:"version"`
	Files     []File  `json:"files"`
	Developer DevCert `json:"developer"`
	Sig       string  `json:"sig"`
}

// signedBytes is what the developer signs: the signature file without Sig
// and without the certificate's key ID, which a system from before key
// IDs drops when it reads the file.
func (s Signature) signedBytes() []byte {
	s.Sig, s.Developer.Key = "", ""
	raw, _ := json.Marshal(s)
	return append([]byte(Format+"\n"), raw...)
}

func certMsg(store, name, pub string) []byte {
	return []byte(certLabel + store + "\n" + name + "\n" + pub)
}

// Store is a store's signing root (the prototype keeps a development store
// on the device; production keys live in the store backend). Key signs:
// an ed25519.PrivateKey, or a crypto.Signer holding an Ed25519 key (one in
// an HSM). Trusted are the store's other keys while its root is rotated
// (11-platform-architecture.md §15.1): the next one, which devices are
// given before it signs, and the one it replaced, whose developer
// certificates are still about.
type Store struct {
	Name    string
	Key     crypto.Signer
	Trusted keyset.Set
}

// Public is the key that signs, nil when Key holds no Ed25519 key.
func (s *Store) Public() ed25519.PublicKey {
	pub, _ := keyset.Public(s.Key)
	return pub
}

// Keys are the store's keys devices trust: the one that signs, then Trusted.
func (s *Store) Keys() keyset.Set {
	var out keyset.Set
	if pub := s.Public(); pub != nil {
		out = keyset.Set{pub}
	}
	return out.Add(s.Trusted...)
}

// Roots is the store as Open takes it.
func (s *Store) Roots() Roots { return Roots{s.Name: s.Keys()} }

// sign is the store's signature over msg and its key's ID.
func (s *Store) sign(msg []byte) (string, string, error) {
	sig, id, err := keyset.Sign(s.Key, msg)
	if err != nil {
		return "", "", fmt.Errorf("store %s: %w", s.Name, err)
	}
	return b64.EncodeToString(sig), id, nil
}

// Certify issues a developer certificate.
func (s *Store) Certify(name string, dev ed25519.PublicKey) (DevCert, error) {
	pub := b64.EncodeToString(dev)
	sig, id, err := s.sign(certMsg(s.Name, name, pub))
	if err != nil {
		return DevCert{}, err
	}
	return DevCert{Name: name, Pub: pub, Store: s.Name, Sig: sig, Key: id}, nil
}

// Issued: the key that signs now made c (not a key rotated out of the
// store): a store that took a new key certifies its developers again.
func (s *Store) Issued(c DevCert) bool {
	pub := s.Public()
	sig, err := b64.DecodeString(c.Sig)
	return pub != nil && err == nil && c.Store == s.Name && (c.Key == "" || c.Key == keyset.ID(pub)) &&
		ed25519.Verify(pub, certMsg(c.Store, c.Name, c.Pub), sig)
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
func Build(dir string, dev Developer) ([]byte, error) { return BuildArches(dir, dev, nil) }

// BuildArches is Build with the libraries of the architectures in arches
// only (lib/<arch>/ of others left out); nil: all of them.
func BuildArches(dir string, dev Developer, arches []string) ([]byte, error) {
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
		case arches != nil && LibArch(rel) != "" && !slices.Contains(arches, LibArch(rel)):
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
	// Files and Data: what the package is on this device (OpenFor leaves
	// other architectures' libraries out).
	Files []File
	Data  map[string][]byte
	// sig is META/signature.json as signed, kept with the installed files
	// so they can be checked again (VerifyDir).
	sig []byte
}

// validPath accepts relative, clean, slash-separated paths inside the package.
func validPath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "." &&
		!strings.HasPrefix(p, "../") && p != ".." && !strings.ContainsAny(p, "\\\x00")
}

// Roots are the store roots a device trusts: each store's name and its keys.
type Roots map[string]keyset.Set

// Open verifies a package against the trusted store roots: every file the
// developer signed, none else.
func Open(raw []byte, roots Roots) (*Package, error) {
	return OpenFor(raw, roots, "")
}

// OpenFor verifies a package as a device of arch keeps it: other
// architectures' libraries (Thinnable) may be missing, as the store thins
// them out, and are left out of Files and Data when present (they still
// have to match the signature); everything else is as Open has it.
func OpenFor(raw []byte, roots Roots, arch string) (*Package, error) {
	if len(raw) > MaxSize {
		return nil, fmt.Errorf("package is over %d MB", MaxSize>>20)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	data := map[string][]byte{}
	var sigRaw []byte
	var total int64
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
		body, err := readBody(tr, &total)
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
	listed := map[string]bool{}
	for _, f := range sig.Files {
		if listed[f.Path] {
			return nil, fmt.Errorf("%s: twice in the signed list", f.Path)
		}
		listed[f.Path] = true
	}
	for name := range data {
		if !listed[name] {
			return nil, errors.New("files differ from the signed list")
		}
	}
	var files []File
	for _, f := range sig.Files {
		body, ok := data[f.Path]
		if !ok && Thinnable(f.Path, arch) {
			continue
		}
		sum := sha256.Sum256(body)
		if !ok || int64(len(body)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 || (f.Mode != 0o644 && f.Mode != 0o755) {
			return nil, fmt.Errorf("%s: differs from the signed list", f.Path)
		}
		if Thinnable(f.Path, arch) {
			delete(data, f.Path)
			continue
		}
		files = append(files, f)
	}
	var head manifestHead
	if err := json.Unmarshal(data["manifest.json"], &head); err != nil {
		return nil, errors.New("manifest.json is missing or invalid")
	}
	if head.ID != sig.App || head.Version != sig.Version {
		return nil, errors.New("manifest does not match the signature")
	}
	return &Package{ID: sig.App, Version: sig.Version, Developer: sig.Developer, Manifest: data["manifest.json"],
		Files: files, Data: data, sig: sigRaw}, nil
}

// readBody reads a file of the package, counting it into total: sparse
// files (GNU's in PAX headers) unpack to more than the tar holds, so the
// package's size is checked again on what comes out.
func readBody(tr *tar.Reader, total *int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(tr, MaxSize-*total+1))
	if err != nil {
		return nil, err
	}
	if *total += int64(len(body)); *total > MaxSize {
		return nil, fmt.Errorf("package is over %d MB unpacked", MaxSize>>20)
	}
	return body, nil
}

// ErrUntrustedStore: the developer certificate comes from a store the device
// does not trust (now: a development store outside developer mode).
var ErrUntrustedStore = errors.New("developer certificate from an untrusted store")

// verifySig checks the developer certificate against the store roots and the
// developer's signature over the file list.
func verifySig(raw []byte, roots Roots) (*Signature, error) {
	var sig Signature
	if err := json.Unmarshal(raw, &sig); err != nil || sig.Format != Format {
		return nil, fmt.Errorf("unknown package format (want %s)", Format)
	}
	keys := roots[sig.Developer.Store]
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w %q", ErrUntrustedStore, sig.Developer.Store)
	}
	certSig, err1 := b64.DecodeString(sig.Developer.Sig)
	devPub, err2 := b64.DecodeString(sig.Developer.Pub)
	if err1 != nil || err2 != nil || len(devPub) != ed25519.PublicKeySize {
		return nil, errors.New("developer certificate is not signed by the store")
	}
	if err := keys.Verify(sig.Developer.Key, certMsg(sig.Developer.Store, sig.Developer.Name, sig.Developer.Pub), certSig); err != nil {
		return nil, fmt.Errorf("developer certificate is not signed by the store: %w", err)
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
func VerifyDir(dir string, roots Roots, extra ...string) error {
	return VerifyDirFor(dir, "", roots, extra...)
}

// VerifyDirFor is VerifyDir for a package OpenFor opened for arch: other
// architectures' libraries may be missing.
func VerifyDirFor(dir, arch string, roots Roots, extra ...string) error {
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
	seen := map[string]bool{}
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
		seen[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	for _, f := range sig.Files {
		if !seen[f.Path] && !Thinnable(f.Path, arch) {
			return errors.New("files of the signed package are missing")
		}
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
	return s, os.WriteFile(filepath.Join(trustDir, s.Name+".pub"), []byte(s.Keys().String()), 0o644)
}

// DevDeveloper is a developer key in dir certified by the store.
func DevDeveloper(dir, name string, s *Store) (Developer, error) {
	k, err := loadOrCreateKey(filepath.Join(dir, name+".key"))
	if err != nil {
		return Developer{}, err
	}
	cert, err := s.Certify(name, k.Public().(ed25519.PublicKey))
	return Developer{Key: k, Cert: cert}, err
}

// LoadRoots reads <store>.pub files from dirs: a store's keys, one base64
// Ed25519 key a line (keyset.Parse); a file that does not parse is left
// out. A store's keys in several of the directories add up.
func LoadRoots(dirs ...string) Roots {
	out := Roots{}
	for _, d := range dirs {
		paths, _ := filepath.Glob(filepath.Join(d, "*.pub"))
		for _, p := range paths {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if keys, err := keyset.Parse(raw); err == nil {
				name := strings.TrimSuffix(filepath.Base(p), ".pub")
				out[name] = out[name].Add(keys...)
			}
		}
	}
	return out
}
