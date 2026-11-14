package pkg

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// An update can travel as a delta: the new version's signature file (the
// full, signed file list) and only the files whose digest changed. The
// device fills in the rest from the installed copy, checking each against
// the signed digest, and the result is an ordinary package that Open
// verifies in full. A delta therefore needs no signature of its own and
// cannot carry anything the developer did not sign.

const (
	DeltaFormat = "phoneapp-delta/1"
	DeltaPath   = "META/delta.json"
)

type deltaHead struct {
	Format string `json:"format"`
	App    string `json:"app"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// readTar returns a package's (or delta's) signature file, delta header and
// other files, checking paths and limits but not signatures.
func readTar(raw []byte) (sigRaw, head []byte, data map[string][]byte, err error) {
	if len(raw) > MaxSize {
		return nil, nil, nil, fmt.Errorf("package is over %d MB", MaxSize>>20)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	data = map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return sigRaw, head, data, nil
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("not a package: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !validPath(hdr.Name) || len(data) >= MaxFiles {
			return nil, nil, nil, fmt.Errorf("%q: not allowed in a package", hdr.Name)
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxSize+1))
		if err != nil {
			return nil, nil, nil, err
		}
		switch hdr.Name {
		case SigPath:
			sigRaw = body
		case DeltaPath:
			head = body
		default:
			if _, dup := data[hdr.Name]; dup {
				return nil, nil, nil, fmt.Errorf("%s: twice in the package", hdr.Name)
			}
			data[hdr.Name] = body
		}
	}
}

func writeTar(entries []tarEntry) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type tarEntry struct {
	name string
	mode int64
	body []byte
}

// MakeDelta is the store's side: the delta from an old package of an app to
// a new one (both already verified at submission).
func MakeDelta(oldRaw, newRaw []byte) ([]byte, error) {
	oldSigRaw, _, _, err := readTar(oldRaw)
	if err != nil {
		return nil, err
	}
	newSigRaw, _, newData, err := readTar(newRaw)
	if err != nil {
		return nil, err
	}
	var oldSig, newSig Signature
	if json.Unmarshal(oldSigRaw, &oldSig) != nil || json.Unmarshal(newSigRaw, &newSig) != nil || oldSig.App != newSig.App {
		return nil, errors.New("not two versions of one app")
	}
	had := map[string]File{}
	for _, f := range oldSig.Files {
		had[f.Path] = f
	}
	head, _ := json.Marshal(deltaHead{Format: DeltaFormat, App: newSig.App, From: oldSig.Version, To: newSig.Version})
	entries := []tarEntry{{DeltaPath, 0o644, head}, {SigPath, 0o644, newSigRaw}}
	for _, f := range newSig.Files {
		if o, ok := had[f.Path]; ok && o.SHA256 == f.SHA256 && o.Mode == f.Mode {
			continue
		}
		entries = append(entries, tarEntry{f.Path, int64(f.Mode), newData[f.Path]})
	}
	return writeTar(entries)
}

// IsDelta: raw is a delta rather than a whole package.
func IsDelta(raw []byte) bool {
	_, head, _, err := readTar(raw)
	return err == nil && head != nil
}

// DeltaInfo is what a delta updates: the app and the versions.
func DeltaInfo(raw []byte) (app, from, to string, err error) {
	_, head, _, err := readTar(raw)
	var h deltaHead
	if err == nil && (head == nil || json.Unmarshal(head, &h) != nil || h.Format != DeltaFormat) {
		err = errors.New("not a delta")
	}
	return h.App, h.From, h.To, err
}

// ApplyDelta rebuilds the whole new package from a delta and the installed
// files of the version it starts from (dir, as Extract wrote it). The
// caller verifies the result with Open.
func ApplyDelta(delta []byte, dir string) ([]byte, error) {
	sigRaw, head, data, err := readTar(delta)
	if err != nil {
		return nil, err
	}
	var h deltaHead
	var sig Signature
	if head == nil || json.Unmarshal(head, &h) != nil || h.Format != DeltaFormat || sigRaw == nil || json.Unmarshal(sigRaw, &sig) != nil {
		return nil, errors.New("not a delta")
	}
	listed := map[string]bool{}
	for _, f := range sig.Files {
		listed[f.Path] = true
	}
	for name := range data {
		if !listed[name] {
			return nil, fmt.Errorf("%s: in the delta but not in the new version", name)
		}
	}
	entries := []tarEntry{{SigPath, 0o644, sigRaw}}
	for _, f := range sig.Files {
		body, ok := data[f.Path]
		if !ok {
			body, err = os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
			sum := sha256.Sum256(body)
			if err != nil || hex.EncodeToString(sum[:]) != f.SHA256 {
				return nil, fmt.Errorf("%s: the installed copy is not the one the delta starts from", f.Path)
			}
		}
		entries = append(entries, tarEntry{f.Path, int64(f.Mode), body})
	}
	return writeTar(entries)
}
