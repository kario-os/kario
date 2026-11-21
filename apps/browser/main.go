// Browser: an address bar, bookmarks and the pages visited, over the
// system's web view (a web node: WPE WebKit in this app's sandboxed web
// process). The app never sees a page's pixels or cookies: it keeps what the
// user visited and saved, and drives the page through web.control; the page's
// state arrives as "web" events.
package main

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type entry struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	At    int64  `json:"at"`
}

type browser struct {
	app       *sdk.App
	Bookmarks []entry `json:"bookmarks"`
	History   []entry `json:"history"`
	url       string  // what the page screen opened
	onPage    bool
	page      ui.WebState
	finding   string
	search    string
}

const maxHistory = 100

// searchBaidu is the manifest's choice of search engine (settings: search)
// for Baidu.
const searchBaidu = "\u767e\u5ea6"

func (b *browser) path() string { return filepath.Join(b.app.DataDir, "browser.json") }

func (b *browser) load() {
	if raw, err := os.ReadFile(b.path()); err == nil {
		_ = json.Unmarshal(raw, b)
	}
}

func (b *browser) save() {
	raw, _ := json.MarshalIndent(b, "", "  ")
	_ = os.WriteFile(b.path(), raw, 0o600)
}

func host(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// address makes a URL of what was typed: a web address as it is (https
// unless it names a scheme), anything else a search.
func (b *browser) address(typed string) string {
	t := strings.TrimSpace(typed)
	switch {
	case t == "":
		return ""
	case strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://"):
		return t
	case !strings.ContainsAny(t, " \t") && strings.Contains(t, ".") && !strings.HasSuffix(t, "."):
		return "https://" + t
	}
	if b.search == searchBaidu {
		return "https://www.baidu.com/s?wd=" + url.QueryEscape(t)
	}
	return "https://cn.bing.com/search?q=" + url.QueryEscape(t)
}

func (b *browser) home(prefill string) ui.Screen {
	s := ui.Screen{ID: "home", Title: tr.T("Browser"), Focus: "url",
		Actions: []ui.Action{{ID: "clear", Label: tr.T("Clear browsing data"), Key: "x", Confirm: tr.T("Clear history, cookies and site data? Bookmarks are kept.")}}}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "url", Placeholder: tr.T("Enter an address or search"), Keyboard: "email", Submit: "go", Value: prefill})
	if len(b.Bookmarks) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Bookmarks")))
		for i, e := range b.Bookmarks {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "b:" + itoa(i), Title: firstOf(e.Title, host(e.URL)), Subtitle: host(e.URL),
				Actions: []ui.Action{sdk.Open(tr.T("Open")), {ID: "unbookmark", Label: tr.T("Delete bookmark"), Key: "d"}}})
		}
	}
	if len(b.History) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Recently visited")))
		for i, e := range b.History {
			if i == 20 {
				break
			}
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "h:" + itoa(i), Title: firstOf(e.Title, host(e.URL)), Subtitle: host(e.URL),
				Trailing: time.Unix(e.At, 0).Format("01-02 15:04"), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
		}
	}
	if len(b.Bookmarks) == 0 && len(b.History) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Type an address and press OK to open it; anything else is searched. On a page, the left soft key bookmarks, finds and zooms.")))
	}
	return s
}

func (b *browser) pageScreen() ui.Screen {
	title := firstOf(b.page.Title, host(b.url))
	if b.page.Loading {
		title = tr.T("Loading · %s", title)
	}
	s := ui.Screen{ID: "page", Title: title, Body: []ui.Node{{Kind: ui.KindWeb, ID: "page", Src: b.url}},
		Actions: []ui.Action{
			{ID: "address", Label: tr.T("Address bar"), Key: "g"},
			{ID: ui.WebReload, Label: tr.T("Reload"), Key: "r"},
			{ID: "bookmark", Label: tr.T("Add bookmark"), Key: "b"},
			{ID: "find", Label: tr.T("Find in page"), Key: "s"},
			{ID: "zoomin", Label: tr.T("Zoom in"), Key: "+"},
			{ID: "zoomout", Label: tr.T("Zoom out"), Key: "-"},
			{ID: "share", Label: tr.T("Share address"), Key: "h"},
			{ID: ui.WebSites, Label: tr.T("Site data"), Key: "d"},
		}}
	if b.page.CanForward {
		s.Actions = append(s.Actions, ui.Action{ID: ui.WebForward, Label: tr.T("Forward"), Key: "f"})
	}
	if b.finding != "" {
		s.Actions = append([]ui.Action{{ID: ui.WebFindNext, Label: tr.T("Next \"%s\"", b.finding), Key: "n"},
			{ID: ui.WebFindEnd, Label: tr.T("Stop finding")}}, s.Actions...)
	}
	return s
}

func (b *browser) open(u string) {
	if u == "" {
		return
	}
	b.url, b.page, b.finding = u, ui.WebState{URL: u, Loading: true}, ""
	if b.onPage {
		b.app.Pop()
		b.app.Update(b.pageScreen())
		return
	}
	b.onPage = true
	b.app.Push(b.pageScreen())
}

func (b *browser) control(op, text string, value float64) {
	b.app.Go(ui.CapWebControl, ui.WebControlArgs{Screen: "page", Node: "page", Op: op, Text: text, Value: value}, nil, func(err error) {
		if err != nil {
			b.app.Toast(err.Error())
		}
	})
}

// visited keeps a page that finished loading at the top of the history.
func (b *browser) visited(s ui.WebState) {
	if s.Loading || s.URL == "" || !strings.HasPrefix(s.URL, "http") {
		return
	}
	for i, e := range b.History {
		if e.URL == s.URL {
			b.History = append(b.History[:i], b.History[i+1:]...)
			break
		}
	}
	b.History = append([]entry{{URL: s.URL, Title: s.Title, At: time.Now().Unix()}}, b.History...)
	if len(b.History) > maxHistory {
		b.History = b.History[:maxHistory]
	}
	b.save()
}

func (b *browser) settings() {
	var v map[string]any
	b.app.Go(ui.CapAppSettings, nil, &v, func(err error) {
		if s, ok := v["search"].(string); ok && err == nil {
			b.search = s
		}
	})
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	b := &browser{app: app}
	b.load()
	app.OnStart = func(arg string) {
		b.settings()
		app.Reset(b.home(""))
		if u, ok := strings.CutPrefix(arg, "share:link:"); ok {
			b.open(b.address(u))
		} else if u, ok := strings.CutPrefix(arg, "open:"); ok {
			b.open(b.address(u))
		}
	}
	app.OnPopped = func(screen string) {
		if screen == "page" {
			b.onPage, b.finding = false, ""
			app.Update(b.home(""))
		}
	}
	app.OnEvent = func(name, value string) {
		switch name {
		case ui.EventWeb:
			var s ui.WebState
			if json.Unmarshal([]byte(value), &s) != nil || s.Screen != "page" || !b.onPage {
				return
			}
			was := b.page
			b.page = s
			if was.Loading && !s.Loading || was.URL != s.URL && !s.Loading || was.Title != s.Title && !s.Loading {
				b.visited(s)
			}
			app.Update(b.pageScreen())
		case ui.EventSettings:
			b.settings()
		case ui.EventOpen:
			b.open(b.address(strings.TrimPrefix(strings.TrimPrefix(value, "share:link:"), "open:")))
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		switch {
		case e.Action == "go" && e.Screen == "home":
			b.open(b.address(e.Fields["url"]))
		case e.Action == "go" && e.Screen == "find":
			if q := strings.TrimSpace(e.Fields["q"]); q != "" {
				b.finding = q
				app.Pop()
				app.Update(b.pageScreen())
				b.control(ui.WebFind, q, 0)
			}
		case e.Action == ui.ActionOpen && strings.HasPrefix(e.Node, "b:"):
			if i := atoi(strings.TrimPrefix(e.Node, "b:")); i >= 0 && i < len(b.Bookmarks) {
				b.open(b.Bookmarks[i].URL)
			}
		case e.Action == ui.ActionOpen && strings.HasPrefix(e.Node, "h:"):
			if i := atoi(strings.TrimPrefix(e.Node, "h:")); i >= 0 && i < len(b.History) {
				b.open(b.History[i].URL)
			}
		case e.Action == "unbookmark":
			if i := atoi(strings.TrimPrefix(e.Node, "b:")); i >= 0 && i < len(b.Bookmarks) {
				b.Bookmarks = append(b.Bookmarks[:i], b.Bookmarks[i+1:]...)
				b.save()
				app.Update(b.home(""))
			}
		case e.Action == "clear":
			b.History = nil
			b.save()
			b.app.Go(ui.CapWebControl, ui.WebControlArgs{Screen: "page", Node: "page", Op: ui.WebClearData}, nil, func(err error) {
				if err != nil {
					app.Toast(err.Error())
				}
			})
			app.Update(b.home(""))
		case e.Action == "address":
			app.Push(b.home(firstOf(b.page.URL, b.url)))
		case e.Action == ui.WebReload:
			b.control(ui.WebReload, "", 0)
		case e.Action == ui.WebForward:
			b.control(ui.WebForward, "", 0)
		case e.Action == ui.WebSites:
			b.control(ui.WebSites, "", 0)
		case e.Action == "bookmark":
			u := firstOf(b.page.URL, b.url)
			for _, x := range b.Bookmarks {
				if x.URL == u {
					app.Toast(tr.T("Already in the bookmarks"))
					return
				}
			}
			b.Bookmarks = append(b.Bookmarks, entry{URL: u, Title: firstOf(b.page.Title, host(u)), At: time.Now().Unix()})
			b.save()
			app.Toast(tr.T("Added to the bookmarks"))
		case e.Action == "find":
			app.Push(ui.Screen{ID: "find", Title: tr.T("Find in page"), Focus: "q", Body: []ui.Node{
				{Kind: ui.KindInput, ID: "q", Placeholder: tr.T("Text to find"), Submit: "go", Value: b.finding}}})
		case e.Action == ui.WebFindNext:
			b.control(ui.WebFindNext, "", 0)
		case e.Action == ui.WebFindEnd:
			b.finding = ""
			b.control(ui.WebFindEnd, "", 0)
			app.Update(b.pageScreen())
		case e.Action == "zoomin" || e.Action == "zoomout":
			z := b.page.Zoom
			if z <= 0 {
				z = 1
			}
			if e.Action == "zoomin" {
				z += 0.25
			} else {
				z -= 0.25
			}
			b.control(ui.WebZoom, "", max(0.5, min(3, z)))
		case e.Action == "share":
			u := firstOf(b.page.URL, b.url)
			app.Go(ui.CapShareOpen, ui.ShareArgs{Kind: "link", URL: u, Text: firstOf(b.page.Title, host(u))}, nil, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(tr.T("Cannot share: %s", err.Error()))
				}
			})
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func itoa(i int) string { return strconv.Itoa(i) }

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}
