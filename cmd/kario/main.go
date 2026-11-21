// kario is the developer's command for TypeScript apps (29-app-development.md):
// a new project, a build (esbuild, built in: no Node.js needed), running it
// in the developer machine's Shell (under Node.js, to debug it; on a SKU's
// screen, keypad and memory tier; with a performance timeline), previews of
// its pages drawn without running it, its manifest checked by the
// installer's rules, its tests against a fake Shell, a signed package (as
// phone-pkg makes them) and its submission to the store.
//
//	kario new [-id com.example.app] [-name NAME] DIR
//	kario build [-o DIR] [-bytecode] [-jsrt PATH] [-minify=false] [-sourcemap] [-native DIR] [-q] DIR
//	kario run [-engine node|jsrt] [-inspect ADDR [-brk]] [-device ID | -sku ID] [-preview ADDR [-gui PATH] [-watch]] [-profile FILE] [-lang LANG] [-shell PATH] [-node PATH] [-native DIR] DIR
//	kario test [-run REGEXP] [-lang LANG] [-inspect] [-node PATH] [-native DIR] DIR
//	kario package [-o FILE] [-key dev.key -cert dev.cert | -devstore DIR [-name NAME]] [-native DIR] [-arch LIST] [-symbols FILE] [-native-symbols DIR] DIR
//	kario preview [-o DIR] [-device LIST] [-sku LIST] [-lang LANG] [-skin] [-json] [-shell PATH] [-gui PATH] [-node PATH] DIR
//	kario manifest check [-json] DIR|FILE|-
//	kario skus [-json]
//	kario publish -store URL [-rollout PERCENT] [-symbols FILE] [-key dev.key] FILE
//	kario symbolize [-so FILE|DIR …] [-map FILE | -symbols FILE] [CRASH]
//	kario device [-ssh CMD] [-to USER@HOST] install|logs|reload|restart|uninstall|shell|push|pull|screenshot|key|type|syslog|forward|reverse|reboot|update|bugreport ... (device.go)
//	kario dap        the debug adapter of Kario Studio's phone debugging (dap.go)
//
// A project is a directory with manifest.json ("runtime": "js"), src/main.ts
// (KarioUI: @kario/ui, bundled in), its translations in
// src/locale/<language>.json and its tests in test/*.test.ts; build writes
// the app directory (manifest.json, main.js, locale/, the icon) into
// DIR/build. -bytecode adds main.jsc, precompiled by kario-jsrt, for the
// system's own build: packages for the store carry JavaScript only. The
// native modules the manifest declares ("native") are libraries the
// developer builds, native/<arch>/<name>.so (arm64 for the phone, x86_64
// for this machine; -native names another directory): build copies them
// into lib/<arch>/, package needs the arm64 ones. run and test need Node.js
// 22.15 or newer (and a C compiler for an app with native modules, or run
// -engine jsrt); build and package do not. run also takes an app written in
// C, C++, Rust or Go, a manifest with "exec" and its program built beside
// it, and runs it as it is. package writes the module's source map into a
// symbol file beside the package (app.phonesym), never into it; publish
// uploads it after the package, and the store symbolizes the app's crash
// reports with it.
package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"os"
)

func die(err error) {
	fmt.Fprintln(os.Stderr, "kario:", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  kario new [-id com.example.app] [-name NAME] DIR
  kario build [-o DIR] [-bytecode] [-jsrt PATH] [-minify=false] [-sourcemap] [-native DIR] [-q] DIR
  kario run [-engine node|jsrt] [-inspect ADDR [-brk]] [-device ID | -sku ID] [-preview ADDR [-gui PATH] [-watch]] [-profile FILE] [-lang LANG] [-shell PATH] [-node PATH] [-native DIR] DIR
  kario test [-run REGEXP] [-lang LANG] [-inspect] [-node PATH] [-native DIR] DIR
  kario package [-o FILE] [-key dev.key -cert dev.cert | -devstore DIR [-name NAME]] [-native DIR] [-arch LIST] [-symbols FILE] [-native-symbols DIR] DIR
  kario preview [-o DIR] [-device LIST] [-sku LIST] [-lang LANG] [-skin] [-json] [-shell PATH] [-gui PATH] [-node PATH] DIR
  kario manifest check [-json] DIR|FILE|-
  kario skus [-json]
  kario publish -store URL [-rollout PERCENT] [-symbols FILE] [-key dev.key] FILE
  kario symbolize [-so FILE|DIR …] [-map FILE | -symbols FILE] [CRASH]
  kario device [-ssh CMD] [-to USER@HOST] install [-devstore DIR] [-open] FILE|DIR | logs [-f] APP | reload|restart|uninstall APP | shell [COMMAND...]
        | push LOCAL REMOTE | pull REMOTE LOCAL | screenshot [-o FILE] | key NAME... | type TEXT... | syslog [-f] [-u UNIT] [-n N]
        | forward LOCALPORT REMOTEPORT | reverse REMOTEPORT LOCALPORT | reboot [bootloader|recovery] | update FILE.raucb | bugreport [-o FILE]
  kario dap`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	fs := flag.NewFlagSet("kario "+os.Args[1], flag.ExitOnError)
	one := func() string {
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			usage()
		}
		return fs.Arg(0)
	}
	var err error
	switch os.Args[1] {
	case "new":
		id := fs.String("id", "", "the app's id (default com.example.<directory>)")
		name := fs.String("name", "", "the app's name (default the directory's)")
		err = newProject(one(), *id, *name)
	case "build":
		var o buildOptions
		fs.StringVar(&o.out, "o", "", "the app directory to write (default DIR/build)")
		fs.BoolVar(&o.bytecode, "bytecode", false, "also write main.jsc, precompiled by kario-jsrt")
		fs.StringVar(&o.jsrt, "jsrt", "", "kario-jsrt for -bytecode (default: $KARIO_JSRT, beside kario, or on PATH)")
		fs.BoolVar(&o.minify, "minify", true, "minify main.js")
		fs.BoolVar(&o.sourcemap, "sourcemap", false, "write main.js.map, to debug the TypeScript")
		fs.BoolVar(&o.quiet, "q", false, "print nothing")
		fs.StringVar(&o.native, "native", "", "the prebuilt native modules, <arch>/<name>.so (default DIR/native)")
		dir := one()
		var r *buildResult
		if r, err = build(dir, o); err == nil && !o.quiet {
			fmt.Println(r.summary())
		}
	case "run":
		var o runOptions
		fs.StringVar(&o.engine, "engine", "node", "node (Node.js: the debugger can attach) or jsrt (kario-jsrt, as on the device)")
		fs.StringVar(&o.inspect, "inspect", "", "Node.js's inspector at this address (127.0.0.1:9229) for a debugger")
		fs.BoolVar(&o.brk, "brk", false, "with -inspect: wait for the debugger before the app starts")
		fs.StringVar(&o.device, "device", "", "the form factor (phone-shell --device: bar-t9, bar-qwerty, communicator)")
		fs.StringVar(&o.sku, "sku", "", "a SKU to simulate (kario skus): its form factor, keypad, memory tier and region")
		fs.StringVar(&o.profile, "profile", "", "write the app's timeline here when the run ends (Chrome Trace Event JSON for ui.perfetto.dev); under Node.js also its CPU profiles beside it")
		fs.StringVar(&o.preview, "preview", "", "the pixel UI on a local web page at this address (127.0.0.1:8790) instead of the terminal")
		fs.StringVar(&o.gui, "gui", "", "the pixel renderer for -preview (default: $KARIO_RENDERER, the shell's, or phone-paint)")
		fs.BoolVar(&o.watch, "watch", false, "with -preview: build again when the sources change and restart the app on its pages")
		fs.StringVar(&o.lang, "lang", "", "the system's language (zh-Hans, zh-Hant, en, es)")
		fs.StringVar(&o.shell, "shell", "", "phone-shell (default: $KARIO_SHELL, beside kario, or on PATH)")
		fs.StringVar(&o.node, "node", "", "Node.js (default: $KARIO_NODE, or node on PATH)")
		fs.StringVar(&o.native, "native", "", "the prebuilt native modules, <arch>/<name>.so (default DIR/native)")
		err = runApp(one(), o)
	case "test":
		var o testOptions
		fs.StringVar(&o.run, "run", "", "only the tests whose name matches")
		fs.StringVar(&o.lang, "lang", "en", "the language apps start in")
		fs.BoolVar(&o.inspect, "inspect", false, "wait for a debugger (Node.js's inspector, 127.0.0.1:9229)")
		fs.StringVar(&o.node, "node", "", "Node.js (default: $KARIO_NODE, or node on PATH)")
		fs.StringVar(&o.native, "native", "", "the prebuilt native modules, <arch>/<name>.so (default DIR/native)")
		err = testApp(one(), o)
	case "package":
		var o packageOptions
		fs.StringVar(&o.out, "o", "app.phoneapp", "the package to write")
		fs.StringVar(&o.key, "key", "dev.key", "the developer's signing key (phone-pkg keygen)")
		fs.StringVar(&o.cert, "cert", "dev.cert", "the developer's certificate from the store (phone-pkg register)")
		fs.StringVar(&o.devStore, "devstore", "", "sign with a development store kept in this directory instead (developer mode)")
		fs.StringVar(&o.name, "name", "Example Developer", "-devstore: the developer's name")
		fs.StringVar(&o.native, "native", "", "the prebuilt native modules, <arch>/<name>.so (default DIR/native)")
		fs.StringVar(&o.arches, "arch", "", "the architectures whose native modules go in, each required (arm64,x86_64; default every one built)")
		fs.StringVar(&o.symbols, "symbols", "", "the symbol file to write (the source map, not for phones; default beside -o, .phonesym; - none)")
		fs.StringVar(&o.nativeSymbols, "native-symbols", "", "the unstripped native modules, <arch>/<name>.so, for the symbol file (the packaged ones may be stripped)")
		dir := one()
		var size int
		if size, err = packageApp(dir, o); err == nil {
			fmt.Printf("%s: %d bytes\n", o.out, size)
			if sym := cmp.Or(o.symbols, symbolsPath(o.out)); o.symbols != "-" {
				if _, serr := os.Stat(sym); serr == nil {
					fmt.Printf("%s: the symbol file (keep it; kario publish uploads it to the store, never to phones)\n", sym)
				}
			}
		}
	case "preview":
		var o previewOptions
		var devices, skus string
		fs.StringVar(&o.out, "o", "", "where to write the pictures, <device or SKU>/<preview>.png (default DIR/.kario/preview)")
		fs.StringVar(&devices, "device", "", "form factors to draw on, comma-separated (default: bar-t9, bar-qwerty, communicator, unless -sku)")
		fs.StringVar(&skus, "sku", "", "SKUs to draw on, comma-separated (kario skus)")
		fs.StringVar(&o.lang, "lang", "en", "the system's language")
		fs.BoolVar(&o.skin, "skin", false, "also draw each inside the phone's body (<preview>.skin.png)")
		asJSON := fs.Bool("json", false, "list the pictures as JSON (Kario Studio's preview panel)")
		fs.StringVar(&o.shell, "shell", "", "phone-shell (default: $KARIO_SHELL, beside kario, or on PATH)")
		fs.StringVar(&o.gui, "gui", "", "the pixel renderer (default: $KARIO_RENDERER, the shell's, or phone-paint)")
		fs.StringVar(&o.node, "node", "", "Node.js (default: $KARIO_NODE, or node on PATH)")
		fs.StringVar(&o.native, "native", "", "the prebuilt native modules, <arch>/<name>.so (default DIR/native)")
		dir := one()
		o.devices, o.skus = commaList(devices), commaList(skus)
		var images []previewImage
		if images, err = previewApp(dir, o); err == nil {
			err = printImages(images, *asJSON)
		}
	case "manifest":
		if len(os.Args) < 3 || os.Args[2] != "check" {
			usage()
		}
		fs = flag.NewFlagSet("kario manifest check", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "the problems by field and the rules' tables, as JSON (Kario Studio's manifest editor)")
		_ = fs.Parse(os.Args[3:])
		if fs.NArg() != 1 {
			usage()
		}
		err = manifestCommand(fs.Arg(0), *asJSON, os.Stdin, os.Stdout)
	case "skus":
		asJSON := fs.Bool("json", false, "as JSON")
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() != 0 {
			usage()
		}
		err = listSKUs(*asJSON)
	case "device":
		err = deviceCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	case "publish":
		var o publishOptions
		fs.StringVar(&o.store, "store", os.Getenv("KARIO_STORE"), "the store backend (default $KARIO_STORE)")
		fs.IntVar(&o.rollout, "rollout", 100, "an update goes to this percentage of devices first")
		fs.StringVar(&o.symbols, "symbols", "", "the package's symbol file (default the .phonesym beside it, if any; - none)")
		fs.StringVar(&o.key, "key", "dev.key", "the developer's signing key, which signs the symbol file's upload")
		file := one()
		var s *submission
		if s, err = publish(file, o); err == nil {
			fmt.Printf("%s %s: %s\n", s.Entry.ID, s.Entry.Version, s.Status)
			if s.Warning != "" {
				fmt.Fprintln(os.Stderr, "kario: warning: store:", s.Warning)
			}
			if s.Symbols != "" {
				fmt.Printf("symbol file: %s\n", s.Symbols)
			}
		}
	case "symbolize":
		var so libraries
		fs.Var(&so, "so", "an unstripped library, or a directory of them, to read symbols from (again for more)")
		mapFile := fs.String("map", "", "the module's source map (kario build -sourcemap), for JavaScript stacks")
		symFile := fs.String("symbols", "", "the package's symbol file (kario package): its source map and libraries")
		_ = fs.Parse(os.Args[2:])
		err = symbolizeFile(fs.Args(), so, *mapFile, *symFile)
	case "dap":
		if len(os.Args) != 2 {
			usage()
		}
		err = serveDAP(os.Stdin, os.Stdout, os.Stderr)
	default:
		err = errors.New("unknown command " + os.Args[1])
	}
	if errors.Is(err, errSilent) {
		os.Exit(1)
	}
	if err != nil {
		die(err)
	}
}
