package shellcmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kario-os/kario/shell"
	"github.com/kario-os/kario/ui"
)

// kario preview (29-app-development.md §3): pages an app's preview
// functions returned, drawn as the developer's app's without starting it,
// each snapped by the pixel renderer, headless, to <name>.png.

// pageToRender is one entry of --render-pages: the file's name and the page.
type pageToRender struct {
	Name   string    `json:"name"`
	Screen ui.Screen `json:"screen"`
}

var pageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// renderPages snaps each page of the file and prints a PHONE-PAGE line for
// each; it returns the exit status.
func renderPages(core *shell.Core, gui string, dev shell.Device, file, pixelDir string, skin bool) int {
	raw, err := os.ReadFile(file)
	if err != nil {
		fail("--render-pages: " + err.Error())
	}
	var pages []pageToRender
	if err := json.Unmarshal(raw, &pages); err != nil {
		fail("--render-pages: a JSON array of {name, screen}: " + err.Error())
	}
	for _, p := range pages {
		if !pageName.MatchString(p.Name) {
			fail(fmt.Sprintf("--render-pages: page name %q: letters, digits, . _ -", p.Name))
		}
	}
	if gui == "" || pixelDir == "" {
		fail("--render-pages needs --gui and --pixel-dir")
	}
	args := []string{"--headless", "--snap-dir", pixelDir}
	if skin {
		args = append(args, "--skin")
	}
	w, err := shell.WatchRenderer(core, dev, gui, "", args...)
	if err != nil {
		fail("pixel renderer: " + err.Error())
	}
	defer w.G.Close()
	for core.BootProgress() >= 0 {
		pump(core, 50*time.Millisecond)
	}
	status := 0
	for _, p := range pages {
		if err := core.ShowDevPage(p.Screen); err != nil {
			fmt.Fprintln(os.Stderr, "phone-shell:", err)
			return 1
		}
		pump(core, 30*time.Millisecond)
		core.Render()
		if err := w.G.Snap(p.Name); err != nil {
			fmt.Fprintf(os.Stderr, "phone-shell: %s: %v\n", p.Name, err)
			status = 1
			continue
		}
		fmt.Printf("PHONE-PAGE: %s %s\n", p.Name, filepath.Join(pixelDir, p.Name+".png"))
	}
	return status
}

// pump runs the core's events for d.
func pump(core *shell.Core, d time.Duration) {
	end := time.After(d)
	for {
		select {
		case f := <-core.Events():
			f()
		case <-end:
			return
		}
	}
}
