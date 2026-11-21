// Package jsrt is what the system knows about TypeScript apps, which run on
// kario-jsrt (the C program in src/, 29-app-development.md): the manifest
// fields that pick it and the rules a package of such an app keeps. The
// installer, the store's review and the kario command apply the same Check.
package jsrt

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/kario-os/kario/pkg"
)

const (
	// Runtime is the manifest's "runtime" of an app kario-jsrt runs.
	Runtime = "js"
	// Program is kario-jsrt's file among the system's app programs.
	Program = "kario-jsrt"
	// DefaultMain is the module kario build writes, and a manifest's
	// "main" when it names none.
	DefaultMain = "main.js"
	// BytecodeExt names precompiled bytecode beside a module (main.jsc).
	BytecodeExt = ".jsc"
	// BytecodeFlag names bytecode elsewhere (a store app's, the Shell's
	// cache): kario-jsrt --bytecode FILE APP/main.js.
	BytecodeFlag = "--bytecode"
	// MaxMain bounds the module's name.
	MaxMain = 64
	// DebugFDEnv names the debugger's socket (src/debug.c), which the Shell
	// gives an app installed in developer mode that the computer debugs.
	DebugFDEnv = "KARIO_JSRT_DEBUG_FD"
	// DebugAttachFDEnv names a socket on which the Shell may hand such a
	// socket to the running app later: a debugger attaching without a
	// restart (SCM_RIGHTS, one byte with the file).
	DebugAttachFDEnv = "KARIO_JSRT_DEBUG_ATTACH_FD"
	// PositionsFlag precompiles with every statement's position, for an
	// app a debugger may attach to: kario-jsrt --positions --compile.
	PositionsFlag = "--positions"
)

// Main is the module a manifest names, DefaultMain when it names none.
func Main(main string) string {
	if main == "" {
		return DefaultMain
	}
	return main
}

// Bytecode is where a module's precompiled bytecode sits (main.jsc).
func Bytecode(main string) string { return strings.TrimSuffix(main, ".js") + BytecodeExt }

// validMain: a .js file at the package root.
func validMain(main string) bool {
	return len(main) <= MaxMain && strings.HasSuffix(main, ".js") && len(main) > len(".js") &&
		path.Base(main) == main && !strings.HasPrefix(main, ".") && !strings.ContainsAny(main, "\\\x00")
}

// App is what a manifest says about how the app runs.
type App struct {
	Runtime string   `json:"runtime"`
	Main    string   `json:"main"`
	Exec    string   `json:"exec"`
	Native  []string `json:"native"`
}

// Check applies an app's runtime fields to its package's files. Runtime ""
// is a native program (exec); "js" is a module (main) kario-jsrt runs. Such
// an app carries no program of its own (no exec, no executable file, no
// machine code but the modules it declares in "native", in lib/<arch>/),
// so review reads JavaScript and the manifest, and the modules as a native
// app's code; and no bytecode either: QuickJS's bytecode format does not
// resist a hostile writer, so only the system precompiles (its own build,
// and a store app's module after installation, outside the package).
// read gives a file's contents; every module has to be built for the
// architectures in need (the device's at installation, DeviceArch in the
// store's review).
func Check(a App, files []pkg.File, read func(path string) ([]byte, error), need ...string) error {
	switch a.Runtime {
	case "":
		if a.Main != "" {
			return fmt.Errorf(`"main" goes with "runtime": %q`, Runtime)
		}
		if len(a.Native) > 0 {
			return fmt.Errorf(`"native" goes with "runtime": %q (a native program links its libraries itself)`, Runtime)
		}
		return nil
	case Runtime:
	default:
		return fmt.Errorf("unknown runtime %q (there is %q)", a.Runtime, Runtime)
	}
	if a.Exec != "" {
		return errors.New(`a JavaScript app has no "exec": kario-jsrt runs its "main"`)
	}
	main := Main(a.Main)
	if !validMain(main) {
		return fmt.Errorf(`"main" %q is not a .js file at the package root`, main)
	}
	if err := CheckNativeNames(a.Native); err != nil {
		return err
	}
	found := false
	built := map[string][]string{}
	for _, f := range files {
		arch, name, isModule := nativeFile(f.Path)
		inLib := strings.HasPrefix(f.Path, NativeDir+"/")
		switch {
		case inLib && f.Mode&0o111 != 0:
			return fmt.Errorf("%s: a native module is a library, not an executable file (mode 0644)", f.Path)
		case f.Mode&0o111 != 0:
			return fmt.Errorf("%s: a JavaScript app carries no executable file", f.Path)
		case strings.HasSuffix(f.Path, BytecodeExt):
			return fmt.Errorf("%s: a package carries JavaScript, not bytecode", f.Path)
		case f.Path == main:
			found = true
		case inLib && (!isModule || !slices.Contains(Arches, arch)):
			return fmt.Errorf("%s: lib/ holds native modules only, as lib/<%s>/<name>%s", f.Path, strings.Join(Arches, "|"), NativeExt)
		case isModule && !slices.Contains(a.Native, name):
			return fmt.Errorf(`%s: a native module the manifest does not declare ("native")`, f.Path)
		}
		raw, err := read(f.Path)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		if !inLib {
			if isELF(raw) {
				return fmt.Errorf(`%s: machine code goes into lib/<arch>/ as a native module the manifest declares ("native")`, f.Path)
			}
			continue
		}
		if err := CheckModule(raw, arch); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		built[name] = append(built[name], arch)
	}
	if !found {
		return fmt.Errorf(`"main" %q is not in the package`, main)
	}
	for _, n := range a.Native {
		if len(built[n]) == 0 {
			return fmt.Errorf("native module %q: no %s in the package", n, NativePath("<arch>", n))
		}
		for _, arch := range need {
			switch {
			case arch == "":
				return fmt.Errorf("native module %q: this machine's architecture loads no native modules", n)
			case !slices.Contains(built[n], arch):
				return fmt.Errorf("native module %q is not built for %s (no %s)", n, arch, NativePath(arch, n))
			}
		}
	}
	return nil
}

// FromData reads a verified package's files, for Check.
func FromData(data map[string][]byte) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		raw, ok := data[p]
		if !ok {
			return nil, errors.New("not in the package")
		}
		return raw, nil
	}
}
