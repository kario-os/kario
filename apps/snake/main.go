// Snake as the Nokia 6110 and 3310 played it, a canvas app
// (07-ecosystem-and-store.md §0): the board is a monochrome LCD the system
// paints in every renderer, the hints under it are ordinary text. Before a
// game the arrows or 2 4 6 8 pick the level (the speed, 1 to 9), # switches
// between walls and wrap-around edges and * the sound; OK or 5 starts and
// pauses. The top score and the choices stay in the app's data; off the
// screen the game pauses.
package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type snakeApp struct {
	app    *sdk.App
	g      *game
	fg     bool
	ticker *time.Ticker
	period time.Duration
	sound  beeper
}

var dirKeys = map[string]pt{"up": up, "2": up, "down": down, "8": down, "left": left, "4": left, "right": right, "6": right}

func loadPrefs(path string) prefs {
	var p prefs
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func savePrefs(path string, p prefs) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func (s *snakeApp) path() string { return filepath.Join(s.app.DataDir, "snake.json") }

func (s *snakeApp) save() {
	if err := savePrefs(s.path(), s.g.prefs); err != nil {
		log.Printf("save: %v", err)
	}
}

func (s *snakeApp) actions() []ui.Action {
	g := s.g
	sound := ui.Action{ID: "sound", Label: tr.T("Turn the sound off"), Key: "*"}
	if g.Mute {
		sound.Label = tr.T("Turn the sound on")
	}
	switch g.phase {
	case inMenu:
		walls := ui.Action{ID: "walls", Label: tr.T("Let the snake pass the edges"), Key: "#"}
		if g.Wrap {
			walls.Label = tr.T("Put walls at the edges")
		}
		return []ui.Action{{ID: "start", Label: tr.T("Start the game"), Key: "5"},
			{ID: "faster", Label: tr.T("Higher level (faster)"), Key: "6"}, {ID: "slower", Label: tr.T("Lower level (slower)"), Key: "4"}, walls, sound}
	case playing:
		return []ui.Action{{ID: "pause", Label: tr.T("Pause"), Key: "5"}, sound}
	case paused:
		return []ui.Action{{ID: "resume", Label: tr.T("Go on"), Key: "5"}, {ID: "end", Label: tr.T("End this game"), Key: "0"}, sound}
	case over:
		return []ui.Action{{ID: "new", Label: tr.T("New game"), Key: "5"}, sound}
	}
	return nil
}

// keyAction is the page action a key on the board stands for.
func (s *snakeApp) keyAction(k string) string {
	if k == "enter" {
		k = "5"
	}
	if s.g.phase == inMenu {
		switch dirKeys[k] {
		case up, right:
			k = "6"
		case down, left:
			k = "4"
		}
	}
	for _, a := range s.actions() {
		if a.Key == k {
			return a.ID
		}
	}
	return ""
}

func (s *snakeApp) do(id string) {
	offered := false
	for _, a := range s.actions() {
		offered = offered || a.ID == id
	}
	if !offered {
		return
	}
	g := s.g
	switch id {
	case "start":
		g.start()
	case "pause":
		g.phase = paused
	case "resume":
		g.phase = playing
	case "end":
		g.end()
		s.save()
	case "new":
		g.phase = inMenu
	case "faster", "slower":
		g.setLevel(g.Level + map[string]int{"faster": 1, "slower": -1}[id])
		s.save()
	case "walls":
		g.Wrap = !g.Wrap
		s.save()
		s.app.Toast(map[bool]string{false: tr.T("Walls: hitting an edge ends the game"), true: tr.T("Wrap-around: the snake comes out on the other side")}[g.Wrap])
	case "sound":
		g.Mute = !g.Mute
		s.save()
		s.app.Toast(map[bool]string{false: tr.T("Sound on"), true: tr.T("Sound off")}[g.Mute])
	}
	s.sync()
}

func (s *snakeApp) hints() []string {
	g := s.g
	switch g.phase {
	case inMenu:
		return []string{tr.T("Level %d · 4 6 change it, OK starts", g.Level), tr.T("# walls or wrap-around, * sound")}
	case paused:
		return []string{tr.T("Paused · OK goes on, 0 ends the game")}
	case over:
		if g.newTop {
			return []string{tr.T("Game over · new top score %d · OK for a new game", g.score)}
		}
		return []string{tr.T("Game over · score %d · OK for a new game", g.score)}
	}
	return []string{tr.T("Arrows or 2 4 6 8 turn, OK pauses")}
}

func (s *snakeApp) screen() ui.Screen {
	body := []ui.Node{{Kind: ui.KindCanvas, ID: "board", Canvas: &ui.Canvas{W: lcdW, H: lcdH, Bg: lcdBg, Ops: draw(s.g), Keys: true}}}
	for _, h := range s.hints() {
		body = append(body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: h})
	}
	return ui.Screen{ID: "board", Title: tr.T("Snake"), Focus: "board", Layout: ui.LayoutCenter, Body: body, Actions: s.actions()}
}

// sync runs the clock only while the snake moves or blinks on screen, and
// shows the board.
func (s *snakeApp) sync() {
	var d time.Duration
	if s.fg && (s.g.phase == playing || s.g.phase == dying) {
		d = s.g.interval()
	}
	if d != s.period {
		if d == 0 {
			s.ticker.Stop()
		} else {
			s.ticker.Reset(d)
		}
		s.period = d
	}
	s.app.Update(s.screen())
}

func (s *snakeApp) step() {
	if !s.fg {
		return
	}
	switch s.g.step() {
	case nothing:
		return
	case ateFood:
		s.beep(eatSound)
	case ateBug:
		s.beep(bugSound)
	case died:
		if s.g.newTop {
			s.save()
		}
	}
	s.sync()
}

func (s *snakeApp) beep(samples []int16) {
	if !s.g.Mute {
		s.sound.play(samples)
	}
}

func (s *snakeApp) lifecycle(state string) {
	s.fg = state == ui.StateForeground
	if !s.fg {
		s.pause()
	}
}

// pause stops the game when the player cannot steer: the app left the
// screen, or a menu or system dialog came over it (the focus event).
func (s *snakeApp) pause() {
	switch s.g.phase {
	case playing:
		s.g.phase = paused
	case dying:
		s.g.phase, s.g.blink = over, 0
	}
	s.sync()
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	s := &snakeApp{app: app, sound: beeper{app: app}, ticker: time.NewTicker(time.Hour)}
	s.ticker.Stop()
	s.g = newGame(loadPrefs(s.path()), rand.New(rand.NewSource(time.Now().UnixNano())))
	go func() {
		for range s.ticker.C {
			app.Post(s.step)
		}
	}()
	app.OnStart = func(string) { app.Reset(s.screen()) }
	app.OnLifecycle = s.lifecycle
	app.OnEvent = func(name, value string) {
		if name == ui.EventFocus && value == ui.FocusLost {
			s.pause()
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		if e.Action != ui.ActionKey {
			s.do(e.Action)
			return
		}
		if d, ok := dirKeys[e.Value]; ok && s.g.phase == playing {
			s.g.turn(d)
			return
		}
		s.do(s.keyAction(e.Value))
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
