// kario is the developer's command for TypeScript apps (29-app-development.md):
// a new project, a build (esbuild, built in: no Node.js needed), running it
// in the developer machine's Shell (under Node.js, to debug it), its tests
// against a fake Shell, a signed package (as phone-pkg makes them) and its
// submission to the store.
//
//	kario new [-id com.example.app] [-name NAME] DIR
//	kario build [-o DIR] [-bytecode] [-jsrt PATH] [-minify=false] [-sourcemap] [-q] DIR
//	kario run [-engine node|jsrt] [-inspect ADDR [-brk]] [-device ID] [-preview ADDR [-gui PATH] [-watch]] [-lang LANG] [-shell PATH] [-node PATH] DIR
//	kario test [-run REGEXP] [-lang LANG] [-inspect] [-node PATH] DIR
//	kario package [-o FILE] [-key dev.key -cert dev.cert | -devstore DIR [-name NAME]] DIR
//	kario publish -store URL [-rollout PERCENT] FILE
//
// A project is a directory with manifest.json ("runtime": "js"), src/main.ts
// (KarioUI: @kario/ui, bundled in), its translations in
// src/locale/<language>.json and its tests in test/*.test.ts; build writes
// the app directory (manifest.json, main.js, locale/, the icon) into
// DIR/build. -bytecode adds main.jsc, precompiled by kario-jsrt, for the
// system's own build: packages for the store carry JavaScript only. run and
// test need Node.js 22.15 or newer; build and package do not.
package main

import (
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
  kario build [-o DIR] [-bytecode] [-jsrt PATH] [-minify=false] [-sourcemap] [-q] DIR
  kario run [-engine node|jsrt] [-inspect ADDR [-brk]] [-device ID] [-preview ADDR [-gui PATH] [-watch]] [-lang LANG] [-shell PATH] [-node PATH] DIR
  kario test [-run REGEXP] [-lang LANG] [-inspect] [-node PATH] DIR
  kario package [-o FILE] [-key dev.key -cert dev.cert | -devstore DIR [-name NAME]] DIR
  kario publish -store URL [-rollout PERCENT] FILE`)
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
		fs.StringVar(&o.preview, "preview", "", "the pixel UI on a local web page at this address (127.0.0.1:8790) instead of the terminal")
		fs.StringVar(&o.gui, "gui", "", "the pixel renderer for -preview (default: $KARIO_RENDERER, the shell's, or phone-paint)")
		fs.BoolVar(&o.watch, "watch", false, "with -preview: build again when the sources change and restart the app on its pages")
		fs.StringVar(&o.lang, "lang", "", "the system's language (zh-Hans, zh-Hant, en, es)")
		fs.StringVar(&o.shell, "shell", "", "phone-shell (default: $KARIO_SHELL, beside kario, or on PATH)")
		fs.StringVar(&o.node, "node", "", "Node.js (default: $KARIO_NODE, or node on PATH)")
		err = runApp(one(), o)
	case "test":
		var o testOptions
		fs.StringVar(&o.run, "run", "", "only the tests whose name matches")
		fs.StringVar(&o.lang, "lang", "en", "the language apps start in")
		fs.BoolVar(&o.inspect, "inspect", false, "wait for a debugger (Node.js's inspector, 127.0.0.1:9229)")
		fs.StringVar(&o.node, "node", "", "Node.js (default: $KARIO_NODE, or node on PATH)")
		err = testApp(one(), o)
	case "package":
		var o packageOptions
		fs.StringVar(&o.out, "o", "app.phoneapp", "the package to write")
		fs.StringVar(&o.key, "key", "dev.key", "the developer's signing key (phone-pkg keygen)")
		fs.StringVar(&o.cert, "cert", "dev.cert", "the developer's certificate from the store (phone-pkg register)")
		fs.StringVar(&o.devStore, "devstore", "", "sign with a development store kept in this directory instead (developer mode)")
		fs.StringVar(&o.name, "name", "Example Developer", "-devstore: the developer's name")
		dir := one()
		var size int
		if size, err = packageApp(dir, o); err == nil {
			fmt.Printf("%s: %d bytes\n", o.out, size)
		}
	case "publish":
		var o publishOptions
		fs.StringVar(&o.store, "store", os.Getenv("KARIO_STORE"), "the store backend (default $KARIO_STORE)")
		fs.IntVar(&o.rollout, "rollout", 100, "an update goes to this percentage of devices first")
		file := one()
		var s *submission
		if s, err = publish(file, o); err == nil {
			fmt.Printf("%s %s: %s\n", s.Entry.ID, s.Entry.Version, s.Status)
		}
	default:
		err = errors.New("unknown command " + os.Args[1])
	}
	if err != nil {
		die(err)
	}
}
