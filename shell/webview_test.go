package shell

import (
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
