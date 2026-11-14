package shellcmd

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/kario-os/kario/shell"
)

// The developer preview (make preview): the pixel renderer runs headless,
// every changed screen is snapped to a PNG and shown on a local web page,
// whose keyboard and buttons are the phone's keys. It needs no display and
// no emulator; over SSH, forward the port and open it in any browser.

//go:embed preview.html
var previewHTML string

var previewPage = template.Must(template.New("preview.html").Parse(previewHTML))

// previewText is the page's text in the device's language.
type previewText struct {
	Lang, Options, RightSoft, Call, HangUp, Back, VolumeDown, VolumeUp string
	Help                                                               template.HTML
}

func previewWords() previewText {
	return previewText{
		Lang: tr.Lang(), Options: tr.T("Options"), RightSoft: tr.T("Right soft key"), Call: tr.T("Call"),
		HangUp: tr.T("Hang up"), Back: tr.T("Back"), VolumeDown: tr.T("Volume −"), VolumeUp: tr.T("Volume +"),
		Help: template.HTML(tr.T(`Keyboard: arrow keys, <kbd>Enter</kbd> OK, <kbd>Esc</kbd> back, <kbd>Tab</kbd>/<kbd>F1</kbd> options, <kbd>F2</kbd> right soft key, <kbd>F5</kbd> call, <kbd>F4</kbd> hang up, <kbd>F3</kbd> switch apps, <kbd>/</kbd> command palette; letters and digits type directly, holding <kbd>Alt</kbd> is a long press.`)),
	}
}

// previewQuiet: a snap waits this long after the last frame, so that the
// renderer's own animations (focus, scrolling) have settled.
const previewQuiet = 120 * time.Millisecond

type previewFrame struct {
	mu   sync.Mutex
	png  []byte
	seq  int
	cond *sync.Cond
}

func (p *previewFrame) set(png []byte) {
	p.mu.Lock()
	p.png, p.seq = png, p.seq+1
	p.mu.Unlock()
	p.cond.Broadcast()
}

// wait returns the frame number once it is past seen (or after a while).
func (p *previewFrame) wait(seen int) int {
	done := time.AfterFunc(25*time.Second, p.cond.Broadcast)
	defer done.Stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	start := time.Now()
	for p.seq <= seen && time.Since(start) < 25*time.Second {
		p.cond.Wait()
	}
	return p.seq
}

func runPreview(core *shell.Core, path string, dev shell.Device, addr string, skin bool) {
	dir, err := os.MkdirTemp("", "phone-preview-")
	if err != nil {
		fail(err.Error())
	}
	defer os.RemoveAll(dir)
	args := []string{"--headless", "--snap-dir", dir}
	if skin {
		args = append(args, "--skin")
	}
	w, err := shell.WatchRenderer(core, dev, path, "", args...)
	if err != nil {
		fail("pixel renderer: " + err.Error())
	}
	defer func() { w.G.Close() }()
	frame := &previewFrame{}
	frame.cond = sync.NewCond(&frame.mu)
	keys := make(chan shell.Key, 64)
	var page bytes.Buffer
	if err := previewPage.Execute(&page, previewWords()); err != nil {
		fail("preview: " + err.Error())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write(page.Bytes())
	})
	mux.HandleFunc("/frame.png", func(rw http.ResponseWriter, r *http.Request) {
		frame.mu.Lock()
		png := frame.png
		frame.mu.Unlock()
		rw.Header().Set("Content-Type", "image/png")
		rw.Header().Set("Cache-Control", "no-store")
		_, _ = rw.Write(png)
	})
	mux.HandleFunc("/wait", func(rw http.ResponseWriter, r *http.Request) {
		seen, _ := strconv.Atoi(r.URL.Query().Get("seq"))
		_ = json.NewEncoder(rw).Encode(map[string]int{"seq": frame.wait(seen)})
	})
	mux.HandleFunc("/key", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(rw, "POST a key", http.StatusMethodNotAllowed)
			return
		}
		var k shell.Key
		switch name, text := r.FormValue("key"), r.FormValue("text"); {
		case text != "":
			k = shell.Runes(text)
			k.Long = r.FormValue("long") == "1"
		default:
			var ok bool
			if k, ok = shell.KeyByName(name); !ok {
				http.Error(rw, "unknown key "+name, http.StatusBadRequest)
				return
			}
		}
		select {
		case keys <- k:
		default:
		}
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fail("preview: " + err.Error())
	}
	go func() { _ = http.Serve(ln, mux) }()
	fmt.Printf("PHONE-PREVIEW: http://%s  (%s, %s %dx%d; over SSH: ssh -L %s:%s <this machine>)\n",
		ln.Addr(), w.G.Info(), dev.ID, dev.Width, dev.Height, portOf(ln.Addr()), ln.Addr())

	core.RestartBoot()
	tick := time.NewTicker(16 * time.Millisecond)
	defer tick.Stop()
	dirty := true
	var due time.Time
	snap := func() {
		if err := w.G.Snap("live"); err != nil {
			fmt.Fprintln(os.Stderr, "preview:", err)
			return
		}
		name := "live.png"
		if skin {
			name = "live.skin.png"
		}
		if png, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			frame.set(png)
		}
	}
	for !core.Quit() {
		select {
		case f := <-core.Events():
			f()
			dirty = true
		case k := <-keys:
			core.Key(k)
			dirty = true
		case <-tick.C:
			if dirty || core.BootProgress() >= 0 {
				before := w.G.Stats().Frames
				core.Render()
				_ = w.G.Push()
				dirty = false
				if w.G.Stats().Frames != before {
					due = time.Now().Add(previewQuiet)
				}
			}
			if !due.IsZero() && time.Now().After(due) {
				due = time.Time{}
				snap()
			}
		case <-w.G.Done():
			if err := w.Restart(); err != nil {
				fail("pixel renderer: " + err.Error())
			}
			dirty = true
		}
	}
}

func portOf(a net.Addr) string {
	_, port, _ := net.SplitHostPort(a.String())
	return port
}
