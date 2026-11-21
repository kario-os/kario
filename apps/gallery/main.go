// Gallery: the system media library, with the photos permission (a system
// app has it preset). Photos are drawn by the system from
// "media:<id>" and videos decoded by it (video.open); the app handles IDs,
// never pixels.
package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

func clock(ms int64) string { return fmt.Sprintf("%d:%02d", ms/60000, ms/1000%60) }

type gallery struct {
	app    *sdk.App
	photos []ui.MediaItem
	videos []ui.MediaItem
	// The video playing, on screen "play:<media id>".
	mu    sync.Mutex
	video ui.VideoState
	media string
}

// items: photos and videos together, newest first.
func (g *gallery) items() []ui.MediaItem {
	all := append(append([]ui.MediaItem(nil), g.photos...), g.videos...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].At > all[j].At })
	return all
}

// list is a grid as a phone's gallery shows it: the picture fills the
// cell; the title is for screens that cannot draw it.
func (g *gallery) list() ui.Screen {
	all := g.items()
	s := ui.Screen{ID: "list", Title: tr.T("Gallery (%d)", len(all)), Columns: 3,
		Actions: []ui.Action{{ID: "refresh", Label: tr.T("Refresh"), Key: "r"}}}
	for _, p := range all {
		n := ui.Node{Kind: ui.KindItem, Style: ui.StyleTile, ID: p.ID, Title: p.Title, Src: p.ID, Actions: []ui.Action{sdk.Open(tr.T("View"))}}
		if p.Kind == ui.MediaVideo {
			n.Badge = "▶ " + clock(int64(p.LenMS))
		}
		s.Body = append(s.Body, n)
	}
	if len(all) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No photos yet")))
	}
	return s
}

func (g *gallery) photo(id string) int {
	for i, p := range g.photos {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (g *gallery) find(id string) *ui.MediaItem {
	for _, p := range g.items() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// view shows photo i; ← → go through the photos. Deleting asks through the
// system (media.delete), so no confirmation of the app's own here.
func (g *gallery) view(i int) ui.Screen {
	p := g.photos[i]
	return ui.Screen{ID: "view:" + p.ID, Title: fmt.Sprintf("%s · %d/%d", p.Title, i+1, len(g.photos)), Right: "delete", Layout: ui.LayoutCenter,
		Actions: []ui.Action{
			{ID: "prev", Label: tr.T("Previous"), Key: "left"},
			{ID: "next", Label: tr.T("Next"), Key: "right"},
			{ID: "wallpaper", Label: tr.T("Set as wallpaper"), Key: "w"},
			{ID: "info", Label: tr.T("Details"), Key: "i"},
			{ID: "share", Label: tr.T("Share"), Key: "s"},
			{ID: "delete", Label: tr.T("Delete"), Key: "d"},
		},
		Body: []ui.Node{{Kind: ui.KindImage, Src: p.ID, Rows: 14}}}
}

// play is the player of video id: OK pauses and plays, ← → step 5 s.
func (g *gallery) play(id string) ui.Screen {
	v := g.find(id)
	title := ""
	if v != nil {
		title = v.Title
	}
	g.mu.Lock()
	st := g.video
	g.mu.Unlock()
	toggle := ui.Action{ID: "toggle", Label: tr.T("Pause"), Primary: true}
	status := tr.T("Opening…")
	switch st.State {
	case ui.PlayPaused:
		toggle.Label, status = tr.T("Play"), tr.T("Paused")
	case ui.PlayEnded:
		toggle.Label, status = tr.T("Replay"), tr.T("Finished")
	case ui.PlayFailed:
		toggle.Label, status = tr.T("Replay"), tr.T("Cannot play: %s", st.Error)
	case ui.PlayPlaying:
		status = tr.T("Playing")
	}
	frac := 0.0
	if st.DurMS > 0 {
		frac = float64(st.PosMS) / float64(st.DurMS)
	}
	return ui.Screen{ID: "play:" + id, Title: title, Right: "delete", Layout: ui.LayoutCenter,
		Actions: []ui.Action{{ID: "info", Label: tr.T("Details"), Key: "i"}, {ID: "delete", Label: tr.T("Delete"), Key: "d"}},
		Body: []ui.Node{
			{Kind: ui.KindMedia, ID: "video", Stream: st.Stream, Rows: 10, Actions: []ui.Action{toggle,
				{ID: "back5", Label: tr.T("Back 5 s"), Key: "left"}, {ID: "fwd5", Label: tr.T("Forward 5 s"), Key: "right"}}},
			sdk.Progress(clock(st.PosMS)+" / "+clock(st.DurMS), frac, status),
		}}
}

func (g *gallery) load(show func(ui.Screen)) {
	g.app.Go(ui.CapMediaList, ui.MediaArgs{Kind: ui.MediaImage}, &g.photos, func(err error) {
		if err != nil {
			g.app.Toast(tr.T("Cannot read the photo library: %s", err.Error()))
		}
		g.app.Go(ui.CapMediaList, ui.MediaArgs{Kind: ui.MediaVideo}, &g.videos, func(error) { show(g.list()) })
	})
}

// control sends op to the player and shows its new state.
func (g *gallery) control(op string, atMS int64) {
	g.mu.Lock()
	player, media := g.video.Player, g.media
	g.mu.Unlock()
	if player == "" {
		return
	}
	var st ui.VideoState
	g.app.Go(ui.CapVideoControl, ui.VideoArgs{Player: player, Op: op, AtMS: atMS}, &st, func(err error) {
		if err != nil {
			if op != ui.OpState {
				g.app.Toast(tr.T("Cannot play: %s", err.Error()))
			}
			return
		}
		g.mu.Lock()
		if g.video.Player != player {
			g.mu.Unlock()
			return
		}
		g.video = st
		g.mu.Unlock()
		g.app.Update(g.play(media))
	})
}

func (g *gallery) startVideo(id string) {
	g.mu.Lock()
	g.video, g.media = ui.VideoState{}, id
	g.mu.Unlock()
	g.app.Push(g.play(id))
	var st ui.VideoState
	g.app.Go(ui.CapVideoOpen, ui.VideoArgs{Media: id}, &st, func(err error) {
		if err != nil {
			g.app.Toast(tr.T("Cannot play: %s", err.Error()))
			return
		}
		g.mu.Lock()
		stale := g.media != id
		if !stale {
			g.video = st
		}
		g.mu.Unlock()
		if stale {
			g.app.Go(ui.CapVideoControl, ui.VideoArgs{Player: st.Player, Op: ui.OpClose}, nil, func(error) {})
			return
		}
		g.app.Update(g.play(id))
	})
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	g := &gallery{app: app}
	app.OnStart = func(string) { g.load(app.Reset) }
	app.OnLifecycle = func(state string) {
		if state == ui.StateForeground {
			g.load(app.Update)
		}
	}
	// The progress of the video on screen.
	go func() {
		for range time.Tick(500 * time.Millisecond) {
			app.Post(func() {
				g.mu.Lock()
				playing := g.video.Player != "" && g.video.State == ui.PlayPlaying
				g.mu.Unlock()
				if playing {
					g.control(ui.OpState, 0)
				}
			})
		}
	}()
	app.OnPopped = func(screen string) {
		if !strings.HasPrefix(screen, "play:") {
			return
		}
		g.mu.Lock()
		player := g.video.Player
		g.video, g.media = ui.VideoState{}, ""
		g.mu.Unlock()
		if player != "" {
			app.Go(ui.CapVideoControl, ui.VideoArgs{Player: player, Op: ui.OpClose}, nil, func(error) {})
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		id := strings.TrimPrefix(strings.TrimPrefix(e.Screen, "view:"), "play:")
		switch {
		case e.Action == "refresh":
			g.load(app.Update)
		case e.Action == "toggle":
			g.mu.Lock()
			state := g.video.State
			g.mu.Unlock()
			switch state {
			case ui.PlayPlaying:
				g.control(ui.OpPause, 0)
			case ui.PlayPaused:
				g.control(ui.OpResume, 0)
			case ui.PlayEnded, ui.PlayFailed:
				g.control(ui.OpSeek, 0)
			}
		case e.Action == "back5" || e.Action == "fwd5":
			g.mu.Lock()
			pos, dur := g.video.PosMS, g.video.DurMS
			g.mu.Unlock()
			step := int64(5000)
			if e.Action == "back5" {
				step = -step
			}
			g.control(ui.OpSeek, max(0, min(dur-500, pos+step)))
		case e.Action == "prev" || e.Action == "next":
			if i := g.photo(id); i >= 0 && len(g.photos) > 1 {
				step := 1
				if e.Action == "prev" {
					step = len(g.photos) - 1
				}
				app.Replace(g.view((i + step) % len(g.photos)))
			}
		case e.Action == "info":
			if p := g.find(id); p != nil {
				field := func(k, v string) ui.Node { return ui.Node{Kind: ui.KindItem, Title: k, Subtitle: v} }
				body := []ui.Node{
					field(tr.T("Name"), p.Title),
					field(tr.T("Dimensions"), fmt.Sprintf("%d×%d", p.W, p.H)),
					field(tr.T("Format"), p.Mime),
					field(tr.T("Size"), fmt.Sprintf("%d KB", (p.Size+1023)/1024)),
					field(tr.T("Time"), time.Unix(p.At, 0).Format("2006-01-02 15:04")),
				}
				if p.Kind == ui.MediaVideo {
					body = append(body[:2], append([]ui.Node{field(tr.T("Duration"), clock(int64(p.LenMS)))}, body[2:]...)...)
				}
				app.Push(ui.Screen{ID: "info:" + p.ID, Title: tr.T("Details"), Body: body})
			}
		case e.Action == "wallpaper":
			app.Push(ui.Screen{ID: "wallpaper:" + id, Title: tr.T("Set as wallpaper"), Body: []ui.Node{
				{Kind: ui.KindItem, ID: ui.WallpaperBoth, Title: tr.T("Standby and lock screen"), Actions: []ui.Action{sdk.Open(tr.T("Set"))}},
				{Kind: ui.KindItem, ID: ui.WallpaperIdle, Title: tr.T("Standby screen only"), Actions: []ui.Action{sdk.Open(tr.T("Set"))}},
				{Kind: ui.KindItem, ID: ui.WallpaperLock, Title: tr.T("Lock screen only"), Subtitle: tr.T("Shown when the keys are locked"), Actions: []ui.Action{sdk.Open(tr.T("Set"))}},
			}})
		case e.Action == ui.ActionOpen && strings.HasPrefix(e.Screen, "wallpaper:"):
			app.Go(ui.CapWallpaperSet, ui.WallpaperArgs{Media: strings.TrimPrefix(e.Screen, "wallpaper:"), Target: e.Node}, nil, func(err error) {
				if err != nil {
					if !ui.Cancelled(err) {
						app.Toast(tr.T("Cannot set as wallpaper: %s", err.Error()))
					}
					return
				}
				app.Pop()
			})
		case e.Action == "share":
			app.Go(ui.CapShareOpen, ui.ShareArgs{Kind: ui.MediaImage, Media: id}, nil, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(tr.T("Cannot share: %s", err.Error()))
				}
			})
		case e.Action == "delete":
			app.Go(ui.CapMediaDelete, ui.MediaArgs{ID: id}, nil, func(err error) {
				if err != nil {
					if !ui.Cancelled(err) {
						app.Toast(tr.T("Delete failed: %s", err.Error()))
					}
					return
				}
				app.Pop()
				app.Toast(tr.T("Deleted"))
				g.load(app.Update)
			})
		case e.Action == ui.ActionOpen:
			if p := g.find(e.Node); p != nil && p.Kind == ui.MediaVideo {
				g.startVideo(p.ID)
			} else if i := g.photo(e.Node); i >= 0 {
				app.Push(g.view(i))
			}
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
