package shell

import (
	"path/filepath"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

// TypeScript apps (29-app-development.md) carry a JavaScript module instead
// of a program: the manifest says "runtime": "js" and names it in "main",
// and the app starts as kario-jsrt with the module, in the app's sandbox
// like any app (the module sits in the app's directory, which the sandbox
// lets it read). The runtime gives the module nothing but the kario
// interfaces and the app's own native modules (lib/<arch>/, declared in
// "native"); everything else still goes through the broker.

// jsRuntime is kario-jsrt among the system's app programs.
func (c *Core) jsRuntime() string { return filepath.Join(c.cfg.AppBinDir, jsrt.Program) }

// execArgs are the arguments the app's program starts with: a JavaScript
// app's module, a native app's manifest args.
func (c *Core) execArgs(m *Manifest) []string {
	if m.Runtime == "" {
		return m.Args
	}
	return []string{filepath.Join(m.dir, filepath.Base(jsrt.Main(m.Main)))}
}

// checkRuntime is the installer's check of the runtime fields (jsrt.Check):
// a JavaScript app brings its module and no program of its own; its native
// modules, if any, are libraries built for this device.
func checkRuntime(m *Manifest, p *pkg.Package) error {
	return jsrt.Check(jsrt.App{Runtime: m.Runtime, Main: m.Main, Exec: m.Exec, Native: m.Native}, p.Files, jsrt.FromData(p.Data), jsrt.HostArch())
}
