package pkg

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
)

// Machine code a package carries for one architecture sits in
// lib/<arch>/ (a TypeScript app's native modules, package jsrt). A package
// may carry several architectures and a device needs only its own, so a
// store serves each device the package thinned for it (Thin), and the
// device verifies that against the developer's signature as it is
// (OpenFor): a file under another architecture's lib/<arch>/ may be
// missing; nothing else may, and what is there must match.

const (
	LibDir     = "lib"
	ArchARM64  = "arm64"
	ArchX86_64 = "x86_64"
)

// Arches are the architectures lib/<arch>/ is for: the phone's and a
// developer machine's.
var Arches = []string{ArchARM64, ArchX86_64}

// HostArch is this machine's architecture among Arches, "" for another.
func HostArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return ArchARM64
	case "amd64":
		return ArchX86_64
	}
	return ""
}

// LibArch is the architecture whose lib/<arch>/ holds p, "" when p is not
// in one.
func LibArch(p string) string {
	rest, ok := strings.CutPrefix(p, LibDir+"/")
	if !ok {
		return ""
	}
	arch, file, ok := strings.Cut(rest, "/")
	if !ok || file == "" || !slices.Contains(Arches, arch) {
		return ""
	}
	return arch
}

// Thinnable: a device of arch does without p, another architecture's
// file. A device of no architecture in Arches ("") does without nothing.
func Thinnable(p, arch string) bool {
	a := LibArch(p)
	return a != "" && a != arch && slices.Contains(Arches, arch)
}

// Thin is a package, or a delta, as a device of arch gets it: the same
// entries in the same order, other architectures' files left out. Thinning
// the same package gives the same bytes, so a catalogue can carry their
// digest (Thinned).
func Thin(raw []byte, arch string) ([]byte, error) {
	if len(raw) > MaxSize {
		return nil, fmt.Errorf("package is over %d MB", MaxSize>>20)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	var entries []tarEntry
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not a package: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !validPath(hdr.Name) || len(entries) >= MaxFiles+2 {
			return nil, fmt.Errorf("%q: not allowed in a package", hdr.Name)
		}
		body, err := readBody(tr, &total)
		if err != nil {
			return nil, err
		}
		if !Thinnable(hdr.Name, arch) {
			entries = append(entries, tarEntry{hdr.Name, hdr.Mode, body})
		}
	}
	return writeTar(entries)
}

// Thinned is a package as a store serves it to devices of one
// architecture.
type Thinned struct {
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

// ThinnedFor is, for each architecture whose devices get less than the
// whole package, what they get; nil when every device gets all of it.
func ThinnedFor(raw []byte) (map[string]Thinned, error) {
	var out map[string]Thinned
	for _, arch := range Arches {
		t, err := Thin(raw, arch)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(t, raw) {
			continue
		}
		if out == nil {
			out = map[string]Thinned{}
		}
		sum := sha256.Sum256(t)
		out[arch] = Thinned{Size: len(t), SHA256: hex.EncodeToString(sum[:])}
	}
	return out, nil
}
