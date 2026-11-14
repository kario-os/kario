// Miniapp is a super-app-style host for JS mini-programs, shaped like WeChat's
// split between a logic layer (a JS engine, no DOM) and a declarative view:
// a page's render() returns phone.Ui nodes, which the system renders. No
// browser is involved. Each program gets its own JS runtime; platform
// capabilities go through a phone.* bridge that checks the program's own
// declared permissions and attributes requests to it (origin).
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

//go:embed programs
var programsFS embed.FS

//go:embed prelude.js
var prelude string

// A runaway callback is interrupted so a program cannot freeze its host.
const callBudget = 300 * time.Millisecond

type meta struct {
	ID          string   `json:"-"`
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Desc        string   `json:"description"`
	Permissions []string `json:"permissions"`
	// Purposes: why it asks for each runtime permission (by the system's
	// name, e.g. "camera" for scan), in English, translated by its catalog.
	Purposes map[string]string `json:"purposes"`
}

func (m meta) check() error {
	for perm, why := range m.Purposes {
		if _, ok := ui.RuntimePerms[perm]; !ok {
			return fmt.Errorf("purpose for %q, which is not a runtime permission", perm)
		}
		if n := len([]rune(strings.TrimSpace(why))); n == 0 || n > ui.MaxPurpose {
			return fmt.Errorf("purpose for %q must be 1–%d characters", perm, ui.MaxPurpose)
		}
	}
	return nil
}

// purposes are the program's purposes in the current language.
func (p *program) purposes() map[string]string {
	out := map[string]string{}
	for perm, why := range p.meta.Purposes {
		out[perm] = translate(p.msgs, why, nil)
	}
	return out
}

type program struct {
	meta meta
	// dir: where an installed (signed) program lives; "" for built-in ones.
	dir     string
	source  string
	msgs    map[string]string // its translations (catalog.go)
	rt      *goja.Runtime
	pages   map[string]*goja.Object
	storage map[string]any
}

type page struct {
	prog   *program
	name   string
	screen string
	obj    *goja.Object
	shown  bool
}

type host struct {
	app      *sdk.App
	progs    []*program
	rejected []string // installed programs whose files fail verification
	added    []string // what the last "add mini-program" did
	pages    map[string]*page
	stack    []string
	seq      int
	root     *keystore.Roots
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	h := &host{app: app, pages: map[string]*page{}}
	h.root, _ = keystore.LoadRootPub(os.Getenv(ui.EnvTrustRoot))
	if err := h.load(); err != nil {
		log.Fatal(err)
	}
	app.OnStart = func(string) { app.Reset(h.home()) }
	app.OnAction = h.action
	app.OnPopped = h.popped
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

func (h *host) load() error {
	dirs, err := fs.ReadDir(programsFS, "programs")
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		raw, err := programsFS.ReadFile("programs/" + d.Name() + "/app.json")
		if err != nil {
			return err
		}
		var m meta
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s: %w", d.Name(), err)
		}
		m.ID = d.Name()
		if err := m.check(); err != nil {
			return fmt.Errorf("%s: %w", d.Name(), err)
		}
		src, err := loadSource(d.Name())
		if err != nil {
			return err
		}
		files, _ := fs.Sub(programsFS, "programs/"+d.Name())
		msgs, err := readCatalog(files, i18n.Lang())
		if err != nil {
			return fmt.Errorf("%s: %w", d.Name(), err)
		}
		h.progs = append(h.progs, &program{meta: m, source: src, msgs: msgs})
	}
	h.loadInstalled()
	sort.Slice(h.progs, func(i, j int) bool { return h.progs[i].meta.ID < h.progs[j].meta.ID })
	for _, p := range h.progs {
		h.app.SetPurposes(p.meta.Name, p.purposes())
	}
	return nil
}

func (h *host) home() ui.Screen {
	s := ui.Screen{ID: "home", Title: tr.T("Mini-programs"), Actions: []ui.Action{
		{ID: "add", Label: tr.T("Add mini-program"), Key: "a"}, {ID: "about", Label: tr.T("About the mini-program framework"), Key: "?"}}}
	s.Body = append(s.Body, sdk.Section(tr.T("My mini-programs")))
	for _, p := range h.progs {
		sub := p.meta.Desc
		if p.dir != "" {
			sub = tr.T("%s · signed and installed", sub)
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "p:" + p.meta.ID, Icon: p.meta.Icon, Title: p.meta.Name,
			Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open")), {ID: "info", Label: tr.T("Permissions and info"), Key: "i"}}})
	}
	for _, r := range h.rejected {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("✗ %s: files failed verification, not loaded", r)))
	}
	for _, a := range h.added {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, a))
	}
	return s
}

func aboutFramework() string {
	return strings.Join([]string{
		"# " + tr.T("Mini-program framework"),
		"",
		tr.T("Mini-programs are carried by a **host app**, in two layers:"),
		"",
		"- " + tr.T("**Logic layer**: each mini-program has a JS runtime of its own (goja), without a DOM"),
		"- " + tr.T("**View layer**: `render()` returns phone.Ui nodes, which the **system** renders"),
		"",
		tr.T("Capabilities go through the host's `phone.*` bridge: it checks the permissions the mini-program declared, then asks for the platform capability as the host, and the system dialogs name the mini-program."),
		"",
		"| " + tr.T("Layer") + " | " + tr.T("This prototype") + " | " + tr.T("Compared with") + " |",
		"|---|---|---|",
		"| " + tr.T("Logic") + " | goja | " + tr.T("WeChat: %s", "V8 / JavaScriptCore") + " |",
		"| " + tr.T("View") + " | phone.Ui | " + tr.T("WeChat: %s", "WebView / Skyline") + " |",
	}, "\n")
}

func (h *host) infoScreen(p *program) ui.Screen {
	perms := tr.T("None")
	if len(p.meta.Permissions) > 0 {
		perms = strings.Join(p.meta.Permissions, tr.T(", "))
	}
	md := strings.Join([]string{
		"# " + p.meta.Name,
		"",
		p.meta.Desc,
		"",
		"- " + tr.T("Declared permissions: %s", perms),
	}, "\n")
	whys := p.purposes()
	for _, perm := range slices.Sorted(maps.Keys(whys)) {
		md += "\n- " + tr.T("Why it asks for %s: %s", perm, whys[perm])
	}
	md += "\n" + strings.Join([]string{
		"- " + tr.T("Runs in the host \"Mini-programs\"; the platform permissions are the host's"),
		"- " + tr.T("A callback running over %d ms is interrupted", callBudget.Milliseconds()),
	}, "\n")
	return ui.Screen{ID: "info", Title: tr.T("Mini-program info"), Body: []ui.Node{{Kind: ui.KindDoc, Text: md}}}
}

func (h *host) progByID(id string) *program {
	for _, p := range h.progs {
		if p.meta.ID == id {
			return p
		}
	}
	return nil
}

func (p *program) allows(perm string) bool {
	for _, x := range p.meta.Permissions {
		if x == perm {
			return true
		}
	}
	return false
}

func (h *host) start(p *program) error {
	if p.rt == nil {
		if err := h.boot(p); err != nil {
			return err
		}
	}
	if _, ok := p.pages["index"]; !ok {
		return fmt.Errorf("%s has no index page", p.meta.ID)
	}
	h.navigate(p, "index", map[string]any{})
	return nil
}

// storageQuota bounds what one mini-program keeps (WeChat allows 10 MB; a
// small phone, less).
const storageQuota = 1 << 20

// storePath: each mini-program's data in a directory of its own, apart from
// the host's and the other programs'.
func (h *host) storePath(p *program) string {
	return filepath.Join(h.app.DataDir, "mp-data", p.meta.ID, "storage.json")
}

func (h *host) boot(p *program) error {
	rt := goja.New()
	p.rt, p.pages, p.storage = rt, map[string]*goja.Object{}, map[string]any{}
	if raw, err := os.ReadFile(h.storePath(p)); err == nil {
		_ = json.Unmarshal(raw, &p.storage)
	}
	_ = rt.Set("Page", func(call goja.FunctionCall) goja.Value {
		p.pages[call.Argument(0).String()] = call.Argument(1).ToObject(rt)
		return goja.Undefined()
	})
	bridge := rt.NewObject()
	_ = bridge.Set("t", func(call goja.FunctionCall) goja.Value {
		var args []goja.Value
		if len(call.Arguments) > 1 {
			args = call.Arguments[1:]
		}
		return rt.ToValue(translate(p.msgs, call.Argument(0).String(), args))
	})
	_ = bridge.Set("toast", func(s string) { h.app.Toast(s) })
	_ = bridge.Set("navigateTo", func(name string, q goja.Value) {
		query := map[string]any{}
		if q != nil && !goja.IsUndefined(q) && !goja.IsNull(q) {
			if m, ok := q.Export().(map[string]any); ok {
				query = m
			}
		}
		h.app.Post(func() { h.navigate(p, name, query) })
	})
	_ = bridge.Set("navigateBack", func(delta goja.Value) {
		n := 1
		if delta != nil && !goja.IsUndefined(delta) {
			n = max(1, int(delta.ToInteger()))
		}
		for i := 0; i < n; i++ {
			h.app.Pop()
		}
	})
	_ = bridge.Set("getStorage", func(key string) goja.Value { return rt.ToValue(p.storage[key]) })
	_ = bridge.Set("setStorage", func(key string, v goja.Value) {
		if !p.allows("storage") {
			panic(rt.NewTypeError("permission not declared by mini-program: storage"))
		}
		old, had := p.storage[key]
		p.storage[key] = v.Export()
		raw, _ := json.Marshal(p.storage)
		if len(raw) > storageQuota {
			if had {
				p.storage[key] = old
			} else {
				delete(p.storage, key)
			}
			panic(rt.NewTypeError(fmt.Sprintf("storage quota exceeded (%d KB per mini-program)", storageQuota>>10)))
		}
		path := h.storePath(p)
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		_ = os.WriteFile(path, raw, 0o600)
	})
	_ = bridge.Set("scanCode", func(opts *goja.Object) { h.scan(p, opts) })
	_ = bridge.Set("requestPayment", func(opts *goja.Object) { h.pay(p, opts) })
	_ = rt.Set("phone", bridge)
	if _, err := rt.RunString(prelude); err != nil {
		return err
	}
	if _, err := h.guard(p, func() (goja.Value, error) { return rt.RunScript(p.meta.ID+"/app.js", p.source) }); err != nil {
		return fmt.Errorf("%s: %w", p.meta.ID, err)
	}
	return nil
}

// guard runs JS with a time budget.
func (h *host) guard(p *program, f func() (goja.Value, error)) (goja.Value, error) {
	t := time.AfterFunc(callBudget, func() { p.rt.Interrupt("mini-program exceeded its time budget") })
	defer func() {
		t.Stop()
		p.rt.ClearInterrupt()
	}()
	return f()
}

func (h *host) invoke(p *program, this *goja.Object, fnVal goja.Value, args ...any) goja.Value {
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return nil
	}
	vals := make([]goja.Value, len(args))
	for i, a := range args {
		vals[i] = p.rt.ToValue(a)
	}
	var thisVal goja.Value = goja.Undefined()
	if this != nil {
		thisVal = this
	}
	v, err := h.guard(p, func() (goja.Value, error) { return fn(thisVal, vals...) })
	if err != nil {
		log.Printf("%s: %v", p.meta.ID, err)
		h.app.Toast(tr.T("Mini-program error: %s", firstLine(err.Error())))
		return nil
	}
	return v
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

func (h *host) navigate(p *program, name string, query map[string]any) {
	def := p.pages[name]
	if def == nil {
		h.app.Toast(tr.T("No such page: %s", name))
		return
	}
	h.seq++
	pg := &page{prog: p, name: name, screen: fmt.Sprintf("mp:%s:%s:%d", p.meta.ID, name, h.seq)}
	obj := p.rt.NewObject()
	for _, k := range def.Keys() {
		_ = obj.Set(k, def.Get(k))
	}
	data := p.rt.NewObject()
	if d := def.Get("data"); d != nil && !goja.IsUndefined(d) {
		raw, _ := json.Marshal(d.Export())
		var copied map[string]any
		_ = json.Unmarshal(raw, &copied)
		for k, v := range copied {
			_ = data.Set(k, v)
		}
	}
	_ = obj.Set("data", data)
	_ = obj.Set("setData", func(patch *goja.Object) {
		cur := obj.Get("data").ToObject(p.rt)
		for _, k := range patch.Keys() {
			_ = cur.Set(k, patch.Get(k))
		}
		if pg.shown {
			h.app.Update(h.screen(pg))
		}
	})
	pg.obj = obj
	h.pages[pg.screen] = pg
	h.invoke(p, obj, obj.Get("onLoad"), query)
	pg.shown = true
	h.stack = append(h.stack, pg.screen)
	h.app.Push(h.screen(pg))
}

// screen calls the page's render() and turns its result into phone.Ui.
func (h *host) screen(pg *page) ui.Screen {
	title := pg.prog.meta.Name
	if t := pg.obj.Get("title"); t != nil && !goja.IsUndefined(t) {
		title = t.String()
	}
	s := ui.Screen{ID: pg.screen, Title: title}
	if v := h.invoke(pg.prog, pg.obj, pg.obj.Get("render")); v != nil && !goja.IsUndefined(v) {
		raw, _ := json.Marshal(v.Export())
		if err := json.Unmarshal(raw, &s.Body); err != nil {
			s.Body = []ui.Node{sdk.Styled(ui.StyleWarn, tr.T("render() did not return phone.Ui nodes: %s", err.Error()))}
		}
	}
	if a := pg.obj.Get("actions"); a != nil && !goja.IsUndefined(a) {
		raw, _ := json.Marshal(a.Export())
		_ = json.Unmarshal(raw, &s.Actions)
	}
	s.Actions = append(s.Actions,
		ui.Action{ID: "mp.info", Label: tr.T("About this mini-program"), Key: "?"},
		ui.Action{ID: "mp.exit", Label: tr.T("Exit the mini-program"), Key: "Q"})
	return s
}

func (h *host) action(e sdk.ActionEvent) {
	switch e.Screen {
	case "home":
		if e.Action == "about" {
			h.app.Push(ui.Screen{ID: "about", Title: tr.T("About"), Body: []ui.Node{{Kind: ui.KindDoc, Text: aboutFramework()}}})
			return
		}
		if e.Action == "add" {
			h.added = h.addDownloaded()
			h.app.Update(h.home())
			return
		}
		p := h.progByID(strings.TrimPrefix(e.Node, "p:"))
		if p == nil {
			return
		}
		if e.Action == "info" {
			h.app.Push(h.infoScreen(p))
			return
		}
		if err := h.start(p); err != nil {
			h.app.Toast(tr.T("Cannot start the mini-program: %s", firstLine(err.Error())))
		}
		return
	}
	pg := h.pages[e.Screen]
	if pg == nil {
		return
	}
	switch e.Action {
	case "mp.info":
		h.app.Push(h.infoScreen(pg.prog))
	case "mp.exit":
		for i := len(h.stack) - 1; i >= 0 && h.pages[h.stack[i]] != nil && h.pages[h.stack[i]].prog == pg.prog; i-- {
			h.app.Pop()
		}
	default:
		fields := map[string]any{}
		for k, v := range e.Fields {
			fields[k] = v
		}
		h.invoke(pg.prog, pg.obj, pg.obj.Get("onAction"), map[string]any{
			"action": e.Action, "node": e.Node, "value": e.Value, "fields": fields,
		})
	}
}

func (h *host) popped(id string) {
	delete(h.pages, id)
	for i, s := range h.stack {
		if s == id {
			h.stack = append(h.stack[:i], h.stack[i+1:]...)
			break
		}
	}
}

func callback(p *program, opts *goja.Object, name string) goja.Value {
	if opts == nil {
		return nil
	}
	return opts.Get(name)
}

func (h *host) fail(p *program, opts *goja.Object, msg string) {
	h.invoke(p, nil, callback(p, opts, "fail"), map[string]any{"errMsg": msg})
}

func (h *host) scan(p *program, opts *goja.Object) {
	if !p.allows("scan") {
		h.fail(p, opts, "permission not declared by mini-program: scan")
		return
	}
	var res ui.ScanResult
	h.app.GoAs(p.meta.Name, ui.CapScanQR, nil, &res, func(err error) {
		if err != nil {
			h.fail(p, opts, err.Error())
			return
		}
		h.invoke(p, nil, callback(p, opts, "success"), map[string]any{"result": res.Text})
	})
}

func yuan(cents int64) string { return fmt.Sprintf("¥%d.%02d", cents/100, cents%100) }

// pay runs the host's payment: the system trusted UI signs a prompt that
// names the mini-program, and the host's server verifies before success.
func (h *host) pay(p *program, opts *goja.Object) {
	if !p.allows("payment") {
		h.fail(p, opts, "permission not declared by mini-program: payment")
		return
	}
	merchant := opts.Get("merchant").String()
	amount := opts.Get("amount").ToInteger()
	if amount <= 0 {
		h.fail(p, opts, "invalid amount")
		return
	}
	h.seq++
	order := fmt.Sprintf("MP%d%03d", time.Now().Unix(), h.seq)
	prompt := tr.T("Mini-program \"%s\" pays \"%s\" %s", p.meta.Name, merchant, yuan(amount))
	raw, _ := json.Marshal(map[string]any{"program": p.meta.ID, "merchant": merchant, "amount": amount, "order": order})
	payload := string(raw)
	var proof ui.KeyProof
	h.app.GoAs(p.meta.Name, ui.CapAuthConfirm, ui.AuthConfirmArgs{Key: "mp-pay", Prompt: prompt, Payload: payload}, &proof, func(err error) {
		if err == nil && h.root != nil {
			// The key is the mini-program's own ("host/origin"), so the
			// merchant's server learns which mini-program the user paid in.
			err = keystore.VerifyConfirm(h.root, &proof, h.app.ID+"/"+p.meta.Name, "mp-pay", prompt, payload)
		}
		if err != nil {
			h.fail(p, opts, err.Error())
			return
		}
		h.invoke(p, nil, callback(p, opts, "success"), map[string]any{"orderId": order, "verified": true})
	})
}
