package shell

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/subproc"
)

// Store apps' bytecode (29-app-development.md §5.1). A package carries
// JavaScript only (jsrt.Check); after installing a TypeScript app the
// system precompiles its module into StateDir/jscache/<id>/, which the
// system owns and the app may only read. The compiler, kario-jsrt
// --compile, parses code nobody reviewed: it runs as the app would (its
// uid, Landlock, seccomp, no data directory, no network) and hands the
// bytecode over a pipe, so at worst it yields bytecode as hostile as the
// app's own source, run by the app's own runtime in the app's sandbox. The
// record beside it names the module's digest in the package's signature,
// the engine and the bytecode's own digest; before every start the Shell
// checks all three, and a stale or changed cache is ignored (the app runs
// its source) and built again. An update or an uninstall removes it.

const (
	jsCacheDir    = "jscache"
	jsCacheFile   = "main" + jsrt.BytecodeExt
	jsCacheRecord = "main.json"
	// maxBytecode bounds what the compiler may hand over (bytecode is about
	// twice its source; a package is at most pkg.MaxSize).
	maxBytecode = 2 * pkg.MaxSize
)

type jsCache struct {
	// engine is kario-jsrt --version, read once; probed says it was tried.
	engine string
	probed bool
	// building: the module digest each compile under way works from.
	building map[string]string
	// source: apps the selftest runs from their source (jscache <id> source).
	source map[string]bool
	// built counts the compiles that finished, stored or not.
	built int
}

type jsCacheRec struct {
	Source   string `json:"source"`
	Engine   string `json:"engine"`
	Bytecode string `json:"bytecode"`
	Size     int64  `json:"size"`
	At       int64  `json:"compiled_at"`
	// Positions: compiled with every statement's position, for an app a
	// debugger may attach to (attachable).
	Positions bool `json:"positions,omitempty"`
}

func (c *Core) jsCachePath(id string) string { return filepath.Join(c.cfg.StateDir, jsCacheDir, id) }

// jsEngine is the runtime's identity ("" without a kario-jsrt here).
func (c *Core) jsEngine() string {
	if !c.jsc.probed {
		c.jsc.probed = true
		out, err := exec.Command(c.jsRuntime(), "--version").Output()
		if err != nil {
			c.log.Printf("store apps' bytecode: off: %s --version: %v", c.jsRuntime(), err)
			return ""
		}
		c.jsc.engine = strings.TrimSpace(string(out))
	}
	return c.jsc.engine
}

// signedSum is a file's SHA-256 as the installed package's signature lists
// it (VerifyDir has checked the files against it since boot).
func signedSum(dir, file string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(pkg.SigPath)))
	if err != nil {
		return "", err
	}
	var sig pkg.Signature
	if err := json.Unmarshal(raw, &sig); err != nil {
		return "", err
	}
	for _, f := range sig.Files {
		if f.Path == file {
			return f.SHA256, nil
		}
	}
	return "", fmt.Errorf("%s is not in the package's signature", file)
}

func cachedApp(a *App) bool { return a != nil && a.M.installed && a.M.Runtime == jsrt.Runtime }

// withBytecode adds the app's cached bytecode to its runtime's arguments,
// with the file its sandbox has to let it read; a cache that does not
// check out is built again and the app runs its source meanwhile.
func (c *Core) withBytecode(a *App, args []string) ([]string, []string) {
	if !cachedApp(a) || c.jsc.source[a.M.ID] {
		return args, nil
	}
	path, err := c.checkJSCache(a)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.log.Printf("%s: bytecode ignored, running the source: %v", a.M.ID, err)
		}
		c.precompile(a)
		return args, nil
	}
	return append([]string{jsrt.BytecodeFlag, path}, args...), []string{path}
}

// checkJSCache is the cached bytecode's path if its record matches the
// installed package and the engine, and the file its record.
func (c *Core) checkJSCache(a *App) (string, error) {
	dir := c.jsCachePath(a.M.ID)
	raw, err := os.ReadFile(filepath.Join(dir, jsCacheRecord))
	if err != nil {
		return "", err
	}
	var rec jsCacheRec
	if err := json.Unmarshal(raw, &rec); err != nil {
		return "", fmt.Errorf("its record: %w", err)
	}
	src, err := signedSum(a.M.dir, jsrt.Main(a.M.Main))
	switch {
	case err != nil:
		return "", err
	case rec.Source != src:
		return "", errors.New("compiled from another version of the module")
	case rec.Engine != c.jsEngine():
		return "", fmt.Errorf("compiled by %q, the runtime is %q", rec.Engine, c.jsEngine())
	case rec.Positions != attachable(a):
		return "", fmt.Errorf("compiled with every statement's position %v, wanted %v", rec.Positions, attachable(a))
	}
	path := filepath.Join(dir, jsCacheFile)
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o022 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return "", errors.New("not a file only the system writes")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if sum := sha256.Sum256(body); int64(len(body)) != rec.Size || hex.EncodeToString(sum[:]) != rec.Bytecode {
		return "", errors.New("changed since it was compiled")
	}
	return path, nil
}

// precompile compiles an installed TypeScript app's module in the
// background, unless a compile of the same module is under way.
func (c *Core) precompile(a *App) {
	if !cachedApp(a) || c.stopping.Load() {
		return
	}
	engine := c.jsEngine()
	if engine == "" {
		return
	}
	id, dir, main := a.M.ID, a.M.dir, jsrt.Main(a.M.Main)
	src, err := signedSum(dir, main)
	if err != nil {
		c.log.Printf("%s: no bytecode: %v", id, err)
		return
	}
	if c.jsc.building == nil {
		c.jsc.building = map[string]string{}
	}
	if c.jsc.building[id] == src {
		return
	}
	positions := attachable(a)
	cmd, err := c.compileCommand(a, filepath.Join(dir, filepath.Base(main)), positions)
	if err != nil {
		c.log.Printf("%s: no bytecode: %v", id, err)
		return
	}
	c.jsc.building[id] = src
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitWriter{w: &out, n: maxBytecode}, &limitWriter{w: &stderr, n: 4096}
	t0 := time.Now()
	// Not from the process thread: a compile under way when the Shell
	// restarts in place is of no use to the new one.
	if err := subproc.Start(cmd); err != nil {
		delete(c.jsc.building, id)
		c.log.Printf("%s: no bytecode: %v", id, err)
		return
	}
	gone := make(chan struct{})
	c.helpers.add(helperProc{done: gone, stop: func() { _ = cmd.Process.Kill() }, kill: cmd.Process.Kill})
	go func() {
		err := cmd.Wait()
		close(gone)
		if err != nil {
			err = fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
		}
		c.Post(func() {
			if c.jsc.building[id] == src {
				delete(c.jsc.building, id)
			}
			c.jsc.built++
			c.storeJSCache(id, dir, src, engine, positions, out.Bytes(), time.Since(t0), err)
		})
	}()
}

// storeJSCache keeps what a compile handed over if the app is still the
// version it compiled; on the loop.
func (c *Core) storeJSCache(id, dir, src, engine string, positions bool, bc []byte, took time.Duration, err error) {
	a := c.appByID(id)
	now := ""
	if cachedApp(a) && a.M.dir == dir {
		now, _ = signedSum(dir, jsrt.Main(a.M.Main))
	}
	switch {
	case now != src:
		if now != "" {
			c.precompile(a)
		}
		return
	case err != nil:
		c.log.Printf("%s: no bytecode: the compiler failed: %v", id, err)
		return
	case !bytes.HasPrefix(bc, []byte("KJSC")):
		c.log.Printf("%s: no bytecode: the compiler handed over no bytecode (%d bytes)", id, len(bc))
		return
	}
	sum := sha256.Sum256(bc)
	rec, _ := json.MarshalIndent(jsCacheRec{Source: src, Engine: engine, Bytecode: hex.EncodeToString(sum[:]),
		Size: int64(len(bc)), At: c.now().Unix(), Positions: positions}, "", "  ")
	cache := c.jsCachePath(id)
	if err := writeJSCache(cache, bc, rec); err != nil {
		_ = os.RemoveAll(cache)
		c.log.Printf("%s: no bytecode: %v", id, err)
		return
	}
	c.log.Printf("%s: precompiled to bytecode in %s (%d bytes)", id, took.Round(time.Millisecond), len(bc))
}

// writeJSCache replaces the cache: the record goes last, so a cache cut
// short has none and is not used.
func writeJSCache(dir string, bc, rec []byte) error {
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.MkdirAll(d, 0o711); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o711); err != nil {
			return err
		}
	}
	_ = os.Remove(filepath.Join(dir, jsCacheRecord))
	for _, f := range []struct {
		name string
		data []byte
	}{{jsCacheFile, bc}, {jsCacheRecord, rec}} {
		if err := statefile.Write(filepath.Join(dir, f.name), f.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// dropJSCache removes an app's bytecode (an update, an uninstall).
func (c *Core) dropJSCache(id string) { _ = os.RemoveAll(c.jsCachePath(id)) }

// compileCommand is kario-jsrt --compile for an app's module, confined as
// the app is, writing to standard output.
func (c *Core) compileCommand(a *App, main string, positions bool) (*exec.Cmd, error) {
	exe := c.jsRuntime()
	args := []string{"--compile", main, "-"}
	if positions {
		args = append([]string{jsrt.PositionsFlag}, args...)
	}
	launcher := filepath.Join(c.cfg.AppBinDir, "phone-sandbox")
	if _, err := os.Stat(launcher); err != nil {
		if c.Sandboxed() {
			return nil, fmt.Errorf("refusing to compile %s unconfined: %w", a.M.ID, err)
		}
		cmd := subproc.Command(exe, args...)
		cmd.Env = compileEnv()
		return cmd, nil
	}
	p := sandbox.Policy{Code: []string{"/usr", "/lib", "/lib64", filepath.Dir(exe)}, ReadOnly: []string{a.M.dir}, Helper: true}
	attr := &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if c.Sandboxed() {
		uid, err := c.appUID(a.M.ID)
		if err != nil {
			return nil, err
		}
		// The compiler reaches the package through the state directory as
		// the app's uid, maybe before any app has run (ownDataDir).
		if err := os.Chmod(c.cfg.StateDir, 0o711); err != nil {
			return nil, err
		}
		p.UID, p.Namespaces = uid, true
		attr.Cloneflags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNET
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(launcher, append([]string{string(raw), exe}, args...)...)
	cmd.SysProcAttr, cmd.Env = attr, compileEnv()
	return cmd, nil
}

func compileEnv() []string { return []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"} }

// limitWriter keeps the first n bytes and fails past them.
type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		return 0, errors.New("too much output")
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}
