package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/ui"
)

// What a browser needs of web nodes beyond showing a page (14 §5): the page's
// state as an event to the host app, the page controls (web.control), find
// in page, downloads into Files, file fields filled from Files, prompt() as a
// system screen, and the content blocker as an app setting.

// webEventFor tells the host app what its page is showing now: the "web"
// event, {screen, node, url, title, loading, can_back, can_forward, zoom}.
func (c *Core) webEventFor(wv *webView) {
	a := wv.proc.app
	parts := strings.SplitN(wv.key, "/", 3)
	if len(parts) != 3 || !a.running {
		return
	}
	raw, _ := json.Marshal(ui.WebState{Screen: parts[1], Node: parts[2], URL: wv.url, Title: wv.title, Loading: wv.loading,
		CanBack: wv.canBack, CanForward: wv.canForward, Zoom: wv.zoom})
	a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventWeb, Value: string(raw)})
}

var webOps = map[string]bool{ui.WebReload: true, ui.WebForward: true, ui.WebBack: true, ui.WebStop: true, ui.WebZoom: true,
	ui.WebFind: true, ui.WebFindNext: true, ui.WebFindPrev: true, ui.WebFindEnd: true, ui.WebClearData: true, ui.WebSites: true}

// webControl runs a page control for the app on one of its own web nodes.
func (c *Core) webControl(a *App, args ui.WebControlArgs) error {
	if !webOps[args.Op] {
		return fmt.Errorf("unknown web control %q", args.Op)
	}
	wv := c.webviews[a.M.ID+"/"+args.Screen+"/"+args.Node]
	if wv == nil && args.Op == ui.WebClearData {
		return c.clearWebData(a)
	}
	if wv == nil {
		return errors.New("no such web view")
	}
	if args.Op == ui.WebBack {
		wv.send(map[string]any{"t": ui.WebBack})
		return nil
	}
	wv.send(map[string]any{"t": "control", "op": args.Op, "text": args.Text, "value": args.Value})
	return nil
}

func (c *Core) webFound(n int) {
	if n == 0 {
		c.showToast(tr.T("Not found on the page"))
		return
	}
	c.showToast(tr.T("Found %d matches", n))
}

// webDownload takes a finished download from the web process's directory
// into Files, granted to the host app, and says so.
func (c *Core) webDownload(wv *webView, m webMsg) {
	a := wv.proc.app
	name, err := cleanFileName(m.Name)
	if err != nil || !m.OK {
		c.notifyAs(a, ui.Notification{ID: "download:" + m.Name, Title: tr.T("Download failed"), Body: m.Name})
		return
	}
	src := filepath.Join(c.webData(a), "downloads", name)
	defer os.Remove(src)
	raw, err := readLimited(src, maxFileBytes)
	if err != nil {
		c.log.Printf("web view %s: download %s: %v", wv.key, name, err)
		c.notifyAs(a, ui.Notification{ID: "download:" + name, Title: tr.T("Download failed"), Body: tr.T("%s: %s", name, err.Error())})
		return
	}
	f, err := c.files.add(name, raw, c.now())
	if err != nil {
		c.notifyAs(a, ui.Notification{ID: "download:" + name, Title: tr.T("Download failed"), Body: tr.T("%s: %s", name, err.Error())})
		return
	}
	c.files.grant(permKey(a, webOrigin(wv.url)), f)
	c.log.Printf("web view %s: downloaded %s as %s (%d bytes)", wv.key, name, f.ID, f.Size)
	c.notifyAs(a, ui.Notification{ID: "download:" + f.ID, Title: tr.T("Downloaded to Files"), Body: name + " · " + sizeLabel(f.Size)})
	c.showToast(tr.T("Downloaded to Files: %s", name))
	if playableVideo(name) {
		c.offerVideo(f)
	}
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("files are at most %d MB", max>>20)
	}
	return raw, nil
}

// webChooseFile fills a page's file field: the user's files of the types it
// takes, in a system menu naming the page; the one picked is copied where
// the web process can read it, and granted to the page's origin.
func (c *Core) webChooseFile(wv *webView, m webMsg) {
	a := wv.proc.app
	done := func(names []string) {
		wv.send(map[string]any{"t": "files", "id": m.ID, "names": names})
	}
	origin := webOrigin(wv.url)
	var es []menuEntry
	for i := len(c.files.Items) - 1; i >= 0; i-- {
		f := c.files.Items[i]
		if !matchesTypes(f, m.Types) {
			continue
		}
		es = append(es, menuEntry{group: tr.T("Files"), label: f.Name + " · " + sizeLabel(f.Size), find: f.Name, run: func() {
			dir := filepath.Join(c.webData(a), "uploads")
			raw, err := os.ReadFile(c.files.path(f))
			if err == nil {
				err = os.MkdirAll(dir, 0o700)
			}
			if err == nil {
				err = os.WriteFile(filepath.Join(dir, f.Name), raw, 0o600)
			}
			if err != nil {
				c.log.Printf("web view %s: upload %s: %v", wv.key, f.ID, err)
				done(nil)
				return
			}
			c.files.grant(permKey(a, origin), f)
			c.log.Printf("web view %s: picked file %s for the page", wv.key, f.ID)
			done([]string{f.Name})
		}})
	}
	if len(es) == 0 {
		c.showToast(tr.T("There is no file in “Files” to choose"))
		done(nil)
		return
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Choose a file · for %s", wv.who()), entries: es, onCancel: func() { done(nil) }})
}

// webSite is one site with data in an app's web process ("sites").
type webSite struct {
	Name  string   `json:"name"`
	Types []string `json:"types"`
}

// siteDataNames are English, translated when shown.
var siteDataNames = map[string]string{"cookies": "Cookie", "storage": "Website storage", "cache": "Cache"}

// webSites shows the sites that keep data for the app, on the system's
// screen; picking one deletes its data after the user confirms.
func (c *Core) webSites(wv *webView, sites []webSite) {
	if len(sites) == 0 {
		c.showToast(tr.T("No site keeps data"))
		return
	}
	var es []menuEntry
	for _, s := range sites {
		var kinds []string
		for _, t := range s.Types {
			if n := siteDataNames[t]; n != "" {
				kinds = append(kinds, tr.T(n))
			}
		}
		name := s.Name
		es = append(es, menuEntry{group: tr.T("Sites keeping data"), label: name + " · " + strings.Join(kinds, tr.T(", ")), find: name, run: func() {
			c.pushOverlay(&confirmOverlay{text: tr.T("Delete the cookies and website data “%s” keeps?\nYou will be signed out of this site", name),
				yes: tr.T("Delete"), onYes: func() { wv.send(map[string]any{"t": "control", "op": webForgetSite, "text": name}) },
				onNo: func() {}})
		}})
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Website data · %s", wv.who()), entries: es})
}

// webForgetSite is the web process's op that deletes one site's data
// (the system's, after the user confirms; apps cannot ask for it).
const webForgetSite = "forget_site"

func (c *Core) webData(a *App) string { return filepath.Join(c.cfg.StateDir, "web", a.M.ID) }

// clearWebData removes the app's cookies, cache and website storage with
// no page open: the web process (if any) ends first, the next one starts
// clean.
func (c *Core) clearWebData(a *App) error {
	if p := c.webprocs[a.M.ID]; p != nil {
		c.stopWebProc(p)
	}
	for _, d := range []string{"cache", "data", "config", "downloads", "uploads"} {
		if err := os.RemoveAll(filepath.Join(c.webData(a), d)); err != nil {
			return err
		}
	}
	c.log.Printf("%s: website data cleared", a.M.ID)
	c.showToast(tr.T("Website data cleared"))
	return nil
}

// webPromptState is a page's prompt() waiting on the system screen.
type webPromptState struct {
	wv  *webView
	id  int
	app *App
}

// webPrompt asks for a page's prompt() text on a system screen naming the
// page: an input with the system input method. The page waits.
func (c *Core) webPrompt(wv *webView, m webMsg) {
	if c.prompt != nil {
		c.endPrompt(false, "")
	}
	c.prompt = &webPromptState{wv: wv, id: m.ID, app: wv.proc.app}
	s := ui.Screen{ID: "sys.prompt", Title: tr.T("The page asks"), Focus: "text", Right: "prompt_cancel",
		Actions: []ui.Action{{ID: "prompt_ok", Label: tr.T("OK")}, {ID: "prompt_cancel", Label: tr.T("Cancel")}},
		Body: []ui.Node{
			{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T("%s asks:", wv.who())},
			{Kind: ui.KindText, Text: m.Message},
			{Kind: ui.KindInput, ID: "text", Value: m.Default, Submit: "prompt_ok"},
		}}
	c.setForeground(nil)
	c.sys.stack = append(c.sys.stack, newView(s))
}

// endPrompt answers the waiting prompt and goes back to the page.
func (c *Core) endPrompt(ok bool, text string) {
	p := c.prompt
	if p == nil {
		return
	}
	c.prompt = nil
	p.wv.send(map[string]any{"t": "answer", "id": p.id, "ok": ok, "text": text})
	if top := c.sys.top(); top != nil && top.S.ID == "sys.prompt" && len(c.sys.stack) > 1 {
		c.sys.stack = c.sys.stack[:len(c.sys.stack)-1]
	}
	if p.app.running {
		c.setForeground(p.app)
	}
}

// webBlocking: the host app turns the content blocker on with its setting
// "content_blocking" (the browser's Block ads and trackers).
func (c *Core) webBlocking(a *App) bool {
	for _, s := range c.appSettings(a) {
		if s.Key == "content_blocking" {
			on, _ := s.Value.(bool)
			return on
		}
	}
	return false
}

// webBlockRules copies the system's blocker rules into the web process's
// directory, which its sandbox can read.
func (c *Core) webBlockRules(a *App) (string, bool) {
	if !c.webBlocking(a) {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(c.cfg.AppsDir), "web", "blocklist.json"))
	if err != nil {
		return "", false
	}
	dst := filepath.Join(c.webData(a), "blocklist.json")
	if os.WriteFile(dst, raw, 0o600) != nil {
		return "", false
	}
	return dst, true
}
