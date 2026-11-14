// Camera: takes photos through the system viewfinder. The app never sees
// the camera stream; it gets the ID of the photo the user took, which now
// lives in the system's media library (shown by Gallery).
package main

import (
	"fmt"
	"log"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	var last *ui.MediaItem
	shots := 0
	screen := func() ui.Screen {
		s := ui.Screen{ID: "main", Title: tr.T("Camera"), Focus: "shoot"}
		s.Body = append(s.Body, sdk.Button("shoot", tr.T("Take photo"), "shoot"))
		if last == nil {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Press OK to open the system viewfinder; photos are saved to Gallery")))
			return s
		}
		s.Body = append(s.Body,
			sdk.Styled(ui.StyleOK, tr.T("Saved to Gallery (photo %d this time)", shots)),
			ui.Node{Kind: ui.KindImage, Src: last.ID, Rows: 8, Label: fmt.Sprintf("%s · %d×%d", last.Title, last.W, last.H)},
		)
		return s
	}
	app.OnStart = func(string) { app.Reset(screen()) }
	app.OnAction = func(e sdk.ActionEvent) {
		if e.Action != "shoot" {
			return
		}
		var it ui.MediaItem
		app.Go(ui.CapCameraCapture, nil, &it, func(err error) {
			if err != nil {
				if err.Error() != ui.CapCameraCapture+": cancelled" {
					app.Toast(tr.T("Cannot take a photo: %s", err.Error()))
				}
				return
			}
			last, shots = &it, shots+1
			app.Update(screen())
		})
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
