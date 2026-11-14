package shell

import (
	"os/exec"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/ui"
)

// The developer's app (kario run, 29-app-development.md §3): phone-shell
// --dev-app loads an app directory kario build wrote, after the system's
// apps (taking the place of one with the same id, to work on a system app),
// and opens it at start. --dev-runner starts it under another program,
// Node.js with its inspector, outside the sandbox, where a debugger can
// reach it; phone-shell takes it on the developer machine only, never with
// the device's services.

func (c *Core) devManifest() []string {
	if c.cfg.DevApp == "" {
		return nil
	}
	return []string{filepath.Join(c.cfg.DevApp, "manifest.json")}
}

func (c *Core) isDev(a *App) bool {
	return c.cfg.DevApp != "" && !a.M.installed && a.M.dir == filepath.Clean(c.cfg.DevApp)
}

// shadowDev leaves out the app the developer's app takes the place of.
func (c *Core) shadowDev() {
	var dev *App
	for _, a := range c.apps {
		if c.isDev(a) {
			dev = a
		}
	}
	if dev == nil {
		return
	}
	out := c.apps[:0]
	for _, a := range c.apps {
		if a != dev && a.M.ID == dev.M.ID {
			c.log.Printf("%s: the developer's app (%s) takes its place", a.M.ID, dev.M.dir)
			continue
		}
		out = append(out, a)
	}
	c.apps = out
}

// devCommand is the developer's app under its runner: the runner, then the
// app's module (a TypeScript app) or its program and arguments.
func (c *Core) devCommand(a *App) *exec.Cmd {
	if len(c.cfg.DevRunner) == 0 || !c.isDev(a) {
		return nil
	}
	argv := append([]string{}, c.cfg.DevRunner...)
	if a.M.Runtime == "" {
		argv = append(argv, c.resolveExec(a.M))
	}
	argv = append(argv, c.execArgs(a.M)...)
	c.log.Printf("%s: the developer's app, started by %v outside the sandbox", a.M.ID, argv)
	return exec.Command(argv[0], argv[1:]...)
}

// LaunchDev opens the developer's app.
func (c *Core) LaunchDev() {
	for _, a := range c.apps {
		if c.isDev(a) {
			c.Launch(a, "")
			return
		}
	}
}

// ReloadDev starts a new build of the developer's app (kario run -watch)
// as a language change restarts apps: on the screen it hands over its state
// and comes back on its pages; in the background it ends with a tombstone;
// not running (it crashed), it opens. A changed manifest is not read again.
func (c *Core) ReloadDev() {
	for _, a := range c.apps {
		switch {
		case !c.isDev(a) || a.ending:
		case !a.running || a.proc == nil:
			c.log.Printf("%s: a new build, opening it", a.M.ID)
			c.Launch(a, "")
		case a != c.fg:
			c.log.Printf("%s: a new build, ending the old one in the background", a.M.ID)
			a.ending = true
			c.kill(a)
		default:
			c.log.Printf("%s: a new build, restarting it on its pages", a.M.ID)
			a.relaunch = true
			a.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
			time.AfterFunc(langSaveWait, func() { c.Post(func() { c.endForRelaunch(a) }) })
		}
	}
}
