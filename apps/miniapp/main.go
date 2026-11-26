// Miniapp is a super-app-style host for JS mini-programs, shaped like WeChat's
// split between a logic layer (a JS engine, no DOM) and a declarative view:
// a page's render() returns phone.Ui nodes, which the system renders. No
// browser is involved. Each program runs on kario-jsrt (QuickJS-ng, the
// engine of TypeScript apps) in a process of its own (runtime.go); platform
// capabilities go through the host's phone.* bridge, which checks the
// program's own declared permissions and attributes requests to it (origin).
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

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
	// Lang is the language of Name and Desc (i18n.DefaultLang when it
	// names none); Locales gives the two in other languages, as an app
	// manifest's locales do.
	Lang    string                  `json:"lang"`
	Locales map[string]i18n.AppText `json:"locales"`
}

// text is the program's name and description in the device's language, by
// its fallback chain (i18n.PickText). Its identity, under which the system
// keeps its grants and keys and its dialogs name it, is Name as written.
func (m meta) text() (name, desc string) {
	lang := i18n.Lang()
	name = i18n.PickText(lang, m.Lang, m.Locales, m.Name, func(t i18n.AppText) string { return t.Name })
	desc = i18n.PickText(lang, m.Lang, m.Locales, m.Desc, func(t i18n.AppText) string { return t.Description })
	return name, desc
}

func (m meta) check() error {
	for l, t := range m.Locales {
		if len(t.Settings)+len(t.Purposes)+len(t.Commands)+len(t.Intents)+len(t.NotifyActions) > 0 {
			return fmt.Errorf("locales.%s: a mini-program's locales give its name and description; its purposes are translated by locale/%s.json", l, l)
		}
	}
	if err := i18n.CheckAppText(m.Lang, m.Locales, i18n.DefaultAppTextRules()); err != nil {
		return err
	}
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
		out[perm] = lookup(p.msgs, why)
	}
	return out
}

type program struct {
	meta meta
	// dir: where an installed (signed) program lives; "" for built-in ones.
	dir    string
	source string
	msgs   map[string]string // its translations (catalog.go)
	// run is its runtime while it has pages open or is kept warm, nil
	// otherwise.
	run *child
	// warm: closed, its runtime kept (warm.go), its pages on shelf
	// (bottom first) since closedAt.
	warm     bool
	shelf    []string
	closedAt time.Time
}

// page is a page a program's runtime opened, by its screen ID; shelved
// while its program is closed but kept warm.
type page struct {
	prog    *program
	screen  string
	shelved bool
}

type host struct {
	app      *sdk.App
	progs    []*program
	rejected []string // installed programs whose files fail verification
	added    []string // what the last "add mini-program" did
	pages    map[string]*page
	// stack: the host's screens above home, as the Shell holds them.
	stack []string
	seq   int
	root  *keystore.Roots
	// jsrt is kario-jsrt; sandbox the launcher that confines each runtime
	// further, "" for none (runtime.go).
	jsrt, sandbox string
	// budget bounds one turn of a program's JavaScript; hang is how long
	// a runtime may leave a ping unanswered before the host ends it.
	budget, hang time.Duration
	// How long, how many and in how much memory closed programs are kept
	// warm (warm.go).
	warmFor    time.Duration
	warmMax    int
	warmMemory int64
	// wg: the goroutines of runtimes, which post to the dispatch goroutine.
	wg sync.WaitGroup
}

func newHost(app *sdk.App) *host {
	return &host{app: app, pages: map[string]*page{}, budget: callBudget, hang: hangLimit,
		warmFor: warmFor, warmMax: warmMax, warmMemory: warmMemory}
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	closeOnExec()
	h := newHost(app)
	h.root, _ = keystore.LoadRootPub(os.Getenv(ui.EnvTrustRoot))
	h.jsrt, h.sandbox = runtimePrograms()
	if err := h.load(); err != nil {
		log.Fatal(err)
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	go h.terminated(term)
	if err := h.run(); err != nil {
		log.Print(err)
	}
}

// terminated ends the host when the Shell stops it (SIGTERM): its runtimes
// first, each given stopGrace to finish a storage write, which ending at
// once would cut short (Pdeathsig kills them with the host).
func (h *host) terminated(sig <-chan os.Signal) {
	<-sig
	h.app.Post(h.stopAll)
	h.waitRuntimes()
	os.Exit(0)
}

func (h *host) run() error {
	h.app.OnStart = func(string) { h.app.Reset(h.home()) }
	h.app.OnAction = h.action
	h.app.OnPopped = h.popped
	defer func() {
		h.stopAll()
		h.waitRuntimes()
	}()
	return h.app.Run()
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
	h.announce()
	return nil
}

// announce gives the Shell's dialogs each program's purposes.
func (h *host) announce() {
	for _, p := range h.progs {
		h.app.SetPurposes(p.meta.Name, p.purposes())
	}
}

func (h *host) home() ui.Screen {
	s := ui.Screen{ID: "home", Title: tr.T("Mini-programs"), Actions: []ui.Action{
		{ID: "add", Label: tr.T("Add mini-program"), Key: "a"}, {ID: "about", Label: tr.T("About the mini-program framework"), Key: "?"}}}
	s.Body = append(s.Body, sdk.Section(tr.T("My mini-programs")))
	for _, p := range h.progs {
		name, sub := p.meta.text()
		if p.dir != "" {
			sub = tr.T("%s · signed and installed", sub)
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "p:" + p.meta.ID, Icon: p.meta.Icon, Title: name,
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
		"- " + tr.T("**Logic layer**: each mini-program runs on QuickJS (kario-jsrt, the engine of TypeScript apps) in a process of its own, without a DOM"),
		"- " + tr.T("**View layer**: `render()` returns phone.Ui nodes, which the **system** renders"),
		"",
		tr.T("Capabilities go through the host's `phone.*` bridge: it checks the permissions the mini-program declared, then asks for the platform capability as the host, and the system dialogs name the mini-program."),
		"",
		"| " + tr.T("Layer") + " | " + tr.T("This prototype") + " | " + tr.T("Compared with") + " |",
		"|---|---|---|",
		"| " + tr.T("Logic") + " | QuickJS-ng | " + tr.T("WeChat: %s", "V8 / JavaScriptCore") + " |",
		"| " + tr.T("View") + " | phone.Ui | " + tr.T("WeChat: %s", "WebView / Skyline") + " |",
	}, "\n")
}

func (h *host) infoScreen(p *program) ui.Screen {
	perms := tr.T("None")
	if len(p.meta.Permissions) > 0 {
		perms = strings.Join(p.meta.Permissions, tr.T(", "))
	}
	name, desc := p.meta.text()
	md := strings.Join([]string{
		"# " + name,
		"",
		desc,
		"",
		"- " + tr.T("Declared permissions: %s", perms),
	}, "\n")
	whys := p.purposes()
	for _, perm := range slices.Sorted(maps.Keys(whys)) {
		md += "\n- " + tr.T("Why it asks for %s: %s", perm, whys[perm])
	}
	md += "\n" + strings.Join([]string{
		"- " + tr.T("Runs in the host \"Mini-programs\"; the platform permissions are the host's"),
		"- " + tr.T("A callback running over %d ms is interrupted", h.budget.Milliseconds()),
		"- " + tr.T("Its JavaScript runs in a process of its own, with at most %d MB of memory", runtimeMemory>>20),
		"- " + tr.T("Closed, it keeps running up to %d minutes and opens again where it was", int(h.warmFor/time.Minute)),
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

// storageQuota bounds what one mini-program keeps (WeChat allows 10 MB; a
// small phone, less).
const storageQuota = 1 << 20

// storeDir: each mini-program's data in a directory of its own, apart from
// the host's and the other programs'.
func (h *host) storeDir(p *program) string {
	return filepath.Join(h.app.DataDir, "mp-data", p.meta.ID)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// push shows a screen of the host's own above the others.
func (h *host) push(s ui.Screen) {
	h.stack = append(h.stack, s.ID)
	h.app.Push(s)
}

func (h *host) action(e sdk.ActionEvent) {
	switch e.Screen {
	case "home":
		if e.Action == "about" {
			h.push(ui.Screen{ID: "about", Title: tr.T("About"), Body: []ui.Node{{Kind: ui.KindDoc, Text: aboutFramework()}}})
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
			h.push(h.infoScreen(p))
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
		h.push(h.infoScreen(pg.prog))
	case "mp.exit":
		h.close(pg.prog)
	default:
		if c := pg.prog.run; c != nil {
			c.send(ui.Msg{T: ui.MsgAction, ScreenID: e.Screen, Node: e.Node, Action: e.Action, Value: e.Value, Fields: e.Fields})
		}
	}
}

// exit closes the program's pages on top of the stack.
func (h *host) exit(p *program) {
	for len(h.stack) > 0 {
		top := h.pages[h.stack[len(h.stack)-1]]
		if top == nil || top.prog != p {
			return
		}
		h.pop()
	}
}

// pop closes the top screen; the Shell says popped when it has.
func (h *host) pop() {
	h.stack = h.stack[:len(h.stack)-1]
	h.app.Pop()
}

func (h *host) popped(id string) {
	if i := slices.Index(h.stack, id); i >= 0 {
		h.stack = slices.Delete(h.stack, i, i+1)
	}
	pg := h.pages[id]
	if pg == nil || pg.shelved {
		return
	}
	// Back on its last page closes the program as its exit action does.
	if p := pg.prog; h.pageCount(p) == 1 && h.keepable(p) {
		pg.shelved = true
		h.hide(p, []string{id})
		return
	}
	delete(h.pages, id)
	if c := pg.prog.run; c != nil {
		c.send(ui.Msg{T: ui.MsgPopped, ScreenID: id})
		if h.pageCount(pg.prog) == 0 {
			c.stop()
		}
	}
}

func (h *host) pageCount(p *program) int {
	n := 0
	for _, pg := range h.pages {
		if pg.prog == p {
			n++
		}
	}
	return n
}

func yuan(cents int64) string { return fmt.Sprintf("¥%d.%02d", cents/100, cents%100) }
