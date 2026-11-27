package shell

import (
	"slices"
	"testing"

	"github.com/kario-os/kario/ui"
)

// A page's origin is what grants are kept under and what system dialogs
// name, so only http(s) pages with a host get one; anything else is refused.
func TestWebOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://mp.weixin.qq.com/s/abc?x=1": tr.T("Web page %s", "mp.weixin.qq.com"),
		"http://127.0.0.1:8080/article":      tr.T("Web page %s", "127.0.0.1:8080"),
		"javascript:alert(1)":                "",
		"file:///etc/passwd":                 "",
		"data:text/html,hi":                  "",
		"about:blank":                        "",
		"https:///nohost":                    "",
		"":                                   "",
	} {
		if got := webOrigin(raw); got != want {
			t.Errorf("webOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Pages get a short allowlist; signing keys, scanning, the camera, photos and
// files stay with apps.
// The system screen in front is redrawn twice a second; a page's prompt
// there keeps its text and stays up, while the task list follows the apps.
func TestPromptSurvivesRefresh(t *testing.T) {
	c := &Core{sys: &App{system: true}}
	prompt := newView(ui.Screen{ID: "sys.prompt", Title: tr.T("The page asks"), Body: []ui.Node{{Kind: ui.KindInput, ID: "text"}}})
	prompt.inputs["text"] = "keyboard phone"
	c.sys.stack = []*View{prompt}
	c.refreshSystemTop()
	if top := c.sys.top(); top.S.ID != "sys.prompt" || top.inputs["text"] != "keyboard phone" {
		t.Fatalf("after a refresh the prompt became %s %q", top.S.ID, top.inputs["text"])
	}
	tasks := newView(ui.Screen{ID: "sys.tasks", Title: "old"})
	c.sys.stack = []*View{tasks}
	c.refreshSystemTop()
	if tasks.S.Title != tr.T("Switch apps") {
		t.Fatalf("the task list was not redrawn: %q", tasks.S.Title)
	}
}

// F3 again, the task list still the system screen under the app switched
// to from it: the apps in their order now, at once. F3, Down, Down, OK
// pressed together landed on a row of the old order, which the refresh
// above replaced only up to half a second later.
func TestTaskListReopenedFollowsTheApps(t *testing.T) {
	app := func(id string) *App { return &App{M: &Manifest{ID: id, Name: id}} }
	a, b, d := app("a"), app("b"), app("d")
	c := &Core{sys: &App{system: true}}
	c.mru = []*App{a, b, d}
	c.openSystem("sys.tasks")
	c.mru = []*App{d, a, b}
	c.openSystem("sys.tasks")
	if len(c.sys.stack) != 1 {
		t.Fatalf("%d system screens, want the one task list", len(c.sys.stack))
	}
	var got []string
	for _, n := range c.sys.top().S.Body {
		got = append(got, n.ID)
	}
	if want := []string{"app:d", "app:a", "app:b"}; !slices.Equal(got, want) {
		t.Fatalf("the task list shows %v, want %v", got, want)
	}
}

func TestWebCaps(t *testing.T) {
	for _, c := range []string{ui.CapKeySign, ui.CapScanQR, ui.CapCameraCapture, ui.CapMediaList, ui.CapFilePick, ui.CapSettingsSet} {
		if webCaps[c] {
			t.Errorf("%s is open to web pages", c)
		}
	}
	for _, c := range []string{ui.CapShareOpen, ui.CapClipboardSet, ui.CapLocationGet, ui.CapAuthConfirm} {
		if !webCaps[c] {
			t.Errorf("%s is not open to web pages", c)
		}
	}
}

// A blank page (black before WebKit paints, or all white) is not painted.
func TestVaried(t *testing.T) {
	black, white := make([]byte, 30), make([]byte, 30)
	for i := range white {
		white[i] = 0xff
	}
	page := append([]byte(nil), white...)
	page[16] = 0x40
	if varied(black) || varied(white) || !varied(page) {
		t.Fatalf("varied: black %v, white %v, page %v", varied(black), varied(white), varied(page))
	}
}
