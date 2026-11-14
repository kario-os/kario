// Package jsrt is what the system knows about TypeScript apps, which run on
// kario-jsrt (the C program in src/, 29-app-development.md): the manifest
// fields that pick it and the rules a package of such an app keeps. The
// installer, the store's review and the kario command apply the same Check.
package jsrt

import (
	"errors"
	"fmt"
	"path"
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
	// MaxMain bounds the module's name.
	MaxMain = 64
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

// Check applies an app's runtime fields to its package's files. Runtime ""
// is a native program (exec); "js" is a module (main) kario-jsrt runs. Such
// an app carries no program of its own (no exec, no executable file), so
// review reads JavaScript and the manifest only; and no bytecode either:
// QuickJS's bytecode format does not resist a hostile writer, so only the
// system's own build precompiles.
func Check(runtime, main, exec string, files []pkg.File) error {
	switch runtime {
	case "":
		if main != "" {
			return fmt.Errorf(`"main" goes with "runtime": %q`, Runtime)
		}
		return nil
	case Runtime:
	default:
		return fmt.Errorf("unknown runtime %q (there is %q)", runtime, Runtime)
	}
	if exec != "" {
		return errors.New(`a JavaScript app has no "exec": kario-jsrt runs its "main"`)
	}
	main = Main(main)
	if !validMain(main) {
		return fmt.Errorf(`"main" %q is not a .js file at the package root`, main)
	}
	found := false
	for _, f := range files {
		switch {
		case f.Mode&0o111 != 0:
			return fmt.Errorf("%s: a JavaScript app carries no executable file", f.Path)
		case strings.HasSuffix(f.Path, BytecodeExt):
			return fmt.Errorf("%s: a package carries JavaScript, not bytecode", f.Path)
		case f.Path == main:
			found = true
		}
	}
	if !found {
		return fmt.Errorf(`"main" %q is not in the package`, main)
	}
	return nil
}
