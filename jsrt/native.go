package jsrt

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kario-os/kario/pkg"
)

// Native modules (22-developer-api.md §9.3): the C, C++ or Rust libraries a
// TypeScript app brings, named in the manifest's "native" and imported as
// "native:<name>". kario-jsrt loads lib/<arch>/<name>.so of the app's own
// directory (src/native.c); these are the package's rules, which the
// installer, the store's review and the kario command apply alike.
const (
	NativeScheme = "native:"
	NativeDir    = pkg.LibDir
	NativeExt    = ".so"
	// MaxNative bounds the modules an app declares, MaxNativeName a name.
	MaxNative     = 32
	MaxNativeName = 32
	// NativeABI and NativeABIMinor are sdk/c/include/kario_native.h's version
	// (KARIO_NATIVE_ABI_MAJOR, _MINOR): kario-jsrt loads that major
	// version up to that minor one.
	NativeABI      = 1
	NativeABIMinor = 0

	ArchARM64  = pkg.ArchARM64
	ArchX86_64 = pkg.ArchX86_64
	// DeviceArch is the phone's: the store takes no package without it.
	DeviceArch = ArchARM64
)

// Arches are the architectures modules are built for: the phone's, and a
// developer machine's (kario run, the selftest on the host). A package may
// carry several; a device gets its own only (pkg.Thin).
var Arches = pkg.Arches

var machines = map[string]elf.Machine{ArchARM64: elf.EM_AARCH64, ArchX86_64: elf.EM_X86_64}

// HostArch is this machine's architecture among Arches, "" for another.
func HostArch() string { return pkg.HostArch() }

// NativePath is where a module for arch sits in a package.
func NativePath(arch, name string) string { return NativeDir + "/" + arch + "/" + name + NativeExt }

// ValidNative: [a-z][a-z0-9_]*, at most MaxNativeName (a file name and an
// import, as kario-jsrt checks it).
func ValidNative(name string) bool {
	if name == "" || len(name) > MaxNativeName || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// CheckNativeNames is a manifest's "native": valid names, each once.
func CheckNativeNames(native []string) error {
	if len(native) > MaxNative {
		return fmt.Errorf(`"native": at most %d modules`, MaxNative)
	}
	for i, n := range native {
		if !ValidNative(n) {
			return fmt.Errorf(`"native": %q is not a module name ([a-z][a-z0-9_]*, at most %d characters)`, n, MaxNativeName)
		}
		if slices.Contains(native[:i], n) {
			return fmt.Errorf(`"native": %q twice`, n)
		}
	}
	return nil
}

// CheckModule says whether raw is a native module for arch: a 64-bit
// little-endian shared library of that machine with the entry points of
// kario_native.h, built for the interface kario-jsrt loads.
func CheckModule(raw []byte, arch string) error {
	f, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return errors.New("not an ELF library")
	}
	defer f.Close()
	want, ok := machines[arch]
	switch {
	case !ok:
		return fmt.Errorf("%q is not an architecture modules are built for (%s)", arch, strings.Join(Arches, ", "))
	case f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != want:
		return fmt.Errorf("a library for %s, not %s", archName(f), arch)
	case f.Type != elf.ET_DYN:
		return errors.New("not a shared library (link it with -shared)")
	case execStack(f):
		return errors.New("a library that asks for an executable stack (link it with -z noexecstack): kario-jsrt seals executable memory once modules are loaded, and glibc would give every later thread such a stack")
	}
	syms, _ := f.DynamicSymbols()
	var abi *elf.Symbol
	init := false
	for i, s := range syms {
		switch {
		case s.Name == "kario_native_abi" && s.Section != elf.SHN_UNDEF:
			abi = &syms[i]
		case s.Name == "kario_native_init" && s.Section != elf.SHN_UNDEF && elf.ST_TYPE(s.Info) == elf.STT_FUNC:
			init = true
		}
	}
	if abi == nil || !init {
		return errors.New("not a native module (no kario_native_abi or kario_native_init: KARIO_NATIVE_MODULE in kario_native.h)")
	}
	v, ok := symbolValue(f, abi)
	if !ok {
		return errors.New("kario_native_abi is not a constant")
	}
	if v>>16 != NativeABI || v&0xffff > NativeABIMinor {
		return fmt.Errorf("built for the native interface %d.%d, the system has %d.%d (rebuild it with the SDK's kario_native.h)", v>>16, v&0xffff, NativeABI, NativeABIMinor)
	}
	return nil
}

// execStack: PT_GNU_STACK asks for an executable stack, or is missing
// (which glibc takes as a request on x86_64).
func execStack(f *elf.File) bool {
	for _, p := range f.Progs {
		if p.Type == elf.PT_GNU_STACK {
			return p.Flags&elf.PF_X != 0
		}
	}
	return true
}

// symbolValue reads a 32-bit constant the library defines.
func symbolValue(f *elf.File, s *elf.Symbol) (uint32, bool) {
	if int(s.Section) >= len(f.Sections) || s.Size != 4 {
		return 0, false
	}
	sec := f.Sections[s.Section]
	if sec.Type == elf.SHT_NOBITS || s.Value < sec.Addr || s.Value+4 > sec.Addr+sec.Size {
		return 0, false
	}
	b := make([]byte, 4)
	if _, err := sec.ReadAt(b, int64(s.Value-sec.Addr)); err != nil {
		return 0, false
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24, true
}

func archName(f *elf.File) string {
	if f.Class == elf.ELFCLASS64 && f.Data == elf.ELFDATA2LSB {
		for a, m := range machines {
			if m == f.Machine {
				return a
			}
		}
	}
	return f.Class.String() + " " + f.Machine.String()
}

// isELF: code a package carries outside its declared modules.
func isELF(head []byte) bool { return bytes.HasPrefix(head, []byte(elf.ELFMAG)) }

// nativeFile splits a path under lib/ into its architecture and module.
func nativeFile(p string) (arch, name string, ok bool) {
	rest, ok := strings.CutPrefix(p, NativeDir+"/")
	if !ok {
		return "", "", false
	}
	arch, file, ok := strings.Cut(rest, "/")
	if !ok || strings.Contains(file, "/") {
		return "", "", false
	}
	name, ok = strings.CutSuffix(file, NativeExt)
	return arch, name, ok
}
