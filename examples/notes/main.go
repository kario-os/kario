// Notes, a sample third-party app: not in the system image, it is
// packaged and signed (fw/pkg) and installed through the store. A skeleton.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	path := filepath.Join(app.DataDir, "notes.json")
	var notes []string
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &notes)
	}
	screen := func() ui.Screen {
		s := ui.Screen{ID: "main", Title: tr.T("Notes"), Focus: "new", Body: []ui.Node{
			{Kind: ui.KindInput, ID: "new", Placeholder: tr.T("Write a note"), Submit: "add"},
		}}
		for i := len(notes) - 1; i >= 0; i-- {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("n", i), Title: notes[i]})
		}
		if len(notes) == 0 {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No notes yet")))
		}
		return s
	}
	app.OnStart = func(string) { app.Reset(screen()) }
	app.OnAction = func(e sdk.ActionEvent) {
		if text := strings.TrimSpace(e.Value); e.Action == "add" && text != "" {
			notes = append(notes, text)
			raw, _ := json.Marshal(notes)
			_ = os.WriteFile(path, raw, 0o600)
			app.Update(screen())
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
