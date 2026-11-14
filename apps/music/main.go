// Music: the media library's tracks by album, artist and folder, the
// format of each (FLAC 24/96, DSD64, SACD), a now-playing screen that says
// how the sound reaches the card (bit-perfect, DoP, mixed), and background
// playback. Sound comes from phone-audiod through the shell; a list plays
// as a queue that runs from one track into the next without a gap (the
// next track is queued while the current one plays). The "audio"
// background mode keeps the app running while a song plays, and the focus
// policy pauses it for calls, ringtones and alarms.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings, kept in the app's data directory.
type settings struct {
	ReplayGain string `json:"replaygain"` // "" off | track | album
	DSD        string `json:"dsd"`        // auto | dop | pcm
}

const seekStep = 10000

type player struct {
	app   *sdk.App
	lib   []track
	queue []int // the list playing, into lib
	cur   int   // index into queue, -1 when nothing was started
	// queueIDs: the queue by media ID while the library is read again.
	queueIDs []string
	stream   string
	state    string // playing | paused | stopped
	byFoc    bool
	posMS    int
	lenMS    int
	output   string
	onNow    bool
	set      settings
}

func clock(msec int) string { return fmt.Sprintf("%d:%02d", msec/60000, msec/1000%60) }

func (p *player) playing() *track {
	if p.cur < 0 || p.cur >= len(p.queue) {
		return nil
	}
	return &p.lib[p.queue[p.cur]]
}

func (p *player) settingsPath() string { return filepath.Join(p.app.DataDir, "settings.json") }

func (p *player) loadSettings() {
	p.set = settings{DSD: "auto"}
	if raw, err := os.ReadFile(p.settingsPath()); err == nil {
		_ = json.Unmarshal(raw, &p.set)
	}
}

func (p *player) saveSettings() {
	raw, _ := json.Marshal(p.set)
	if err := os.WriteFile(p.settingsPath(), raw, 0o600); err != nil {
		log.Print(err)
	}
}

// trackNode is a track in a list. Its ID is the track's own, not its
// place: the library grows while it is indexed, and a press must play the
// track it was on.
func (p *player) trackNode(t track, sub string) ui.Node {
	node := ui.Node{Kind: ui.KindItem, ID: "t:" + t.ID, Icon: tr.T("♪"), Title: t.Title, Subtitle: sub,
		Trailing: clock(t.LenMS), Actions: []ui.Action{{ID: "play", Label: tr.T("Play"), Primary: true}, {ID: "info", Label: tr.T("Track info"), Key: "i"}}}
	if cur := p.playing(); cur != nil && cur.ID == t.ID && p.state != ui.PlayStopped {
		node.Badge = map[string]string{ui.PlayPlaying: tr.T("Playing"), ui.PlayPaused: tr.T("Paused")}[p.state]
	}
	return node
}

func (t track) line() string {
	parts := []string{t.artist()}
	if t.Format != "" {
		parts = append(parts, t.Format)
	}
	return strings.Join(parts, " · ")
}

// listIndex is where the track a node shows is in its screen's list.
func (p *player) listIndex(screen, node string) (int, bool) {
	id, ok := strings.CutPrefix(node, "t:")
	if !ok {
		return 0, false
	}
	for i, li := range p.list(screen) {
		if p.lib[li].ID == id {
			return i, true
		}
	}
	return 0, false
}

// list is the tracks a screen shows, in order (what plays as the queue).
func (p *player) list(screen string) []int {
	as := albums(p.lib)
	switch {
	case screen == "list" || screen == "tracks":
		out := make([]int, len(p.lib))
		for i := range out {
			out[i] = i
		}
		return out
	case strings.HasPrefix(screen, "album:"):
		for _, a := range as {
			if a.key == strings.TrimPrefix(screen, "album:") {
				return a.tracks
			}
		}
	case strings.HasPrefix(screen, "artist:"):
		for _, a := range artists(p.lib, as) {
			if a.name == strings.TrimPrefix(screen, "artist:") {
				return a.tracks
			}
		}
	case strings.HasPrefix(screen, "folder:"):
		_, ts := folder(p.lib, strings.TrimPrefix(screen, "folder:"))
		return ts
	}
	return nil
}

func (p *player) homeScreen() ui.Screen {
	s := ui.Screen{ID: "list", Title: tr.T("Music"), Actions: []ui.Action{
		{ID: "albums", Label: tr.T("Albums"), Key: "a"},
		{ID: "artists", Label: tr.T("Artists"), Key: "r"},
		{ID: "folders", Label: tr.T("Folders"), Key: "f"},
		{ID: "settings", Label: tr.T("Playback settings"), Key: "g"},
	}}
	if t := p.playing(); t != nil && p.state != ui.PlayStopped {
		s.Right = "now"
		s.Actions = append(s.Actions, ui.Action{ID: "now", Label: tr.T("Now playing"), Key: "o"})
	}
	if len(p.lib) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No music in the media library")))
		return s
	}
	for _, t := range p.lib {
		s.Body = append(s.Body, p.trackNode(t, t.line()))
	}
	return s
}

func (p *player) albumsScreen() ui.Screen {
	s := ui.Screen{ID: "albums", Title: tr.T("Albums")}
	for _, a := range albums(p.lib) {
		sub := a.artist
		if a.year != "" {
			sub += " · " + a.year
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "a:" + a.key, Icon: tr.T("◉"), Title: a.title, Subtitle: sub,
			Badge: quality(p.lib, a.tracks), Trailing: tr.T("%d tracks", len(a.tracks)), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	if len(s.Body) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No music in the media library")))
	}
	return s
}

func (p *player) artistsScreen() ui.Screen {
	s := ui.Screen{ID: "artists", Title: tr.T("Artists")}
	for _, a := range artists(p.lib, albums(p.lib)) {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "r:" + a.name, Title: a.name,
			Subtitle: tr.T("%d albums", a.albums), Trailing: tr.T("%d tracks", len(a.tracks)), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	if len(s.Body) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No music in the media library")))
	}
	return s
}

func (p *player) albumScreen(a album) ui.Screen {
	s := ui.Screen{ID: "album:" + a.key, Title: a.title}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, a.artist+" · "+quality(p.lib, a.tracks)))
	for _, i := range a.tracks {
		t := p.lib[i]
		sub := t.Format
		if t.Artist != "" && t.Artist != a.artist {
			sub = t.Artist + " · " + sub
		}
		node := p.trackNode(t, sub)
		if t.Number > 0 {
			node.Icon = fmt.Sprint(t.Number)
		}
		s.Body = append(s.Body, node)
	}
	return s
}

func (p *player) artistScreen(a artist) ui.Screen {
	s := ui.Screen{ID: "artist:" + a.name, Title: a.name}
	last := ""
	for _, i := range a.tracks {
		t := p.lib[i]
		if t.album() != last {
			last = t.album()
			s.Body = append(s.Body, sdk.Section(last))
		}
		s.Body = append(s.Body, p.trackNode(t, t.Format))
	}
	return s
}

func (p *player) folderScreen(dir string) ui.Screen {
	title := tr.T("Folders")
	if dir != "" {
		title = path.Base(dir)
	}
	s := ui.Screen{ID: "folder:" + dir, Title: title}
	subs, ts := folder(p.lib, dir)
	for _, sub := range subs {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "d:" + sub, Icon: tr.T("▸"), Title: path.Base(sub), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	for _, i := range ts {
		t := p.lib[i]
		s.Body = append(s.Body, p.trackNode(t, t.line()))
	}
	if len(s.Body) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No music in the media library")))
	}
	return s
}

func (p *player) gainLabel() string {
	return map[string]string{"": tr.T("Off"), "track": tr.T("Track gain"), "album": tr.T("Album gain")}[p.set.ReplayGain]
}

func (p *player) dsdLabel() string {
	return map[string]string{"auto": tr.T("Native DSD, else PCM"), "dop": tr.T("DoP (DSD over PCM)"), "pcm": tr.T("Always PCM")}[p.set.DSD]
}

func (p *player) settingsScreen() ui.Screen {
	return ui.Screen{ID: "settings", Title: tr.T("Playback settings"), Body: []ui.Node{
		{Kind: ui.KindItem, ID: "replaygain", Title: tr.T("ReplayGain"), Subtitle: p.gainLabel(), Actions: []ui.Action{{ID: "cycle", Label: tr.T("Change"), Primary: true}}},
		{Kind: ui.KindItem, ID: "dsd", Title: tr.T("DSD output"), Subtitle: p.dsdLabel(), Actions: []ui.Action{{ID: "cycle", Label: tr.T("Change"), Primary: true}}},
		sdk.Styled(ui.StyleDim, tr.T("Bit-perfect: with media volume at its highest and ReplayGain off, a track goes to the sound card unchanged at its own rate and depth. DoP is only for a DAC that plays DSD: others play it as loud noise.")),
	}}
}

func (p *player) infoScreen(t track) ui.Screen {
	field := func(k, v string) ui.Node { return ui.Node{Kind: ui.KindItem, Title: k, Subtitle: v} }
	body := []ui.Node{
		field(tr.T("Title"), t.Title),
		field(tr.T("Artist"), t.artist()),
		field(tr.T("Album"), t.album()),
		field(tr.T("Format"), t.Format),
		field(tr.T("Duration"), clock(t.LenMS)),
	}
	if t.Rate > 0 {
		body = append(body, field(tr.T("Sample rate"), fmt.Sprintf("%g kHz", float64(t.Rate)/1000)))
	}
	if t.Bits > 1 {
		body = append(body, field(tr.T("Bit depth"), fmt.Sprintf("%d bit", t.Bits)))
	}
	if t.Channels > 0 {
		body = append(body, field(tr.T("Channels"), fmt.Sprint(t.Channels)))
	}
	switch t.From {
	case "cue":
		body = append(body, field(tr.T("Source"), tr.T("A track of a cue sheet")))
	case "sacd":
		body = append(body, field(tr.T("Source"), tr.T("A track of an SACD image")))
	}
	if t.Folder != "" {
		body = append(body, field(tr.T("Folder"), t.Folder))
	}
	if t.ReplayGain {
		body = append(body, field(tr.T("ReplayGain"), tr.T("In the tags")))
	}
	return ui.Screen{ID: "info", Title: tr.T("Track info"), Body: body}
}

func (p *player) nowScreen() ui.Screen {
	t := p.playing()
	status := map[string]string{ui.PlayPlaying: tr.T("Playing"), ui.PlayPaused: tr.T("Playback paused"), ui.PlayStopped: tr.T("Playback stopped")}[p.state]
	if p.byFoc {
		status = tr.T("Paused for a call or ringtone")
	}
	toggle := tr.T("Pause")
	if p.state != ui.PlayPlaying {
		toggle = tr.T("Play")
	}
	length := p.lenMS
	if length == 0 {
		length = t.LenMS
	}
	frac := 0.0
	if length > 0 {
		frac = float64(p.posMS) / float64(length)
	}
	body := []ui.Node{
		{Kind: ui.KindText, Style: ui.StyleDisplay, Text: t.Title},
		{Kind: ui.KindText, Style: ui.StyleDim, Text: t.artist() + " · " + t.album()},
		{Kind: ui.KindText, Style: ui.StyleAccent, Text: status},
	}
	if t.Format != "" {
		line := t.Format
		if o := outputLabel(p.output, *t); o != "" && p.state == ui.PlayPlaying {
			line += " · " + o
		}
		body = append(body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: line})
	}
	body = append(body, sdk.Progress("", frac, clock(p.posMS)+" / "+clock(length)), sdk.Button("toggle", toggle, "toggle"))
	return ui.Screen{ID: "now", Title: tr.T("Now playing"), Right: "next", Focus: "toggle", Layout: ui.LayoutCenter, Actions: []ui.Action{
		{ID: "toggle", Label: toggle, Key: "p"},
		{ID: "next", Label: tr.T("Next"), Key: "right"},
		{ID: "prev", Label: tr.T("Previous"), Key: "left"},
		{ID: "fwd", Label: tr.T("Forward 10 s"), Key: "f"},
		{ID: "back", Label: tr.T("Back 10 s"), Key: "b"},
		{ID: "info", Label: tr.T("Track info"), Key: "i"},
		{ID: ui.OpStop, Label: tr.T("Stop"), Key: "s"},
	}, Body: body}
}

// refresh updates the screens that show playback.
func (p *player) refresh() {
	p.app.Update(p.homeScreen())
	if p.onNow && p.playing() != nil {
		p.app.Update(p.nowScreen())
	}
}

func (p *player) nextID() string {
	if p.cur+1 < len(p.queue) {
		return p.lib[p.queue[p.cur+1]].ID
	}
	return ""
}

// start plays queue[i], with the one after it queued.
func (p *player) start(queue []int, i int) {
	if i < 0 || i >= len(queue) {
		return
	}
	var s struct {
		ID     string `json:"id"`
		Output string `json:"output"`
	}
	// The stream being replaced ends on its way out: that is not the track
	// finishing, so its events are ignored from here on.
	p.stream = ""
	p.queue = queue
	t := p.lib[queue[i]]
	args := ui.AudioPlayArgs{Media: t.ID, ReplayGain: p.set.ReplayGain, DSD: p.set.DSD}
	if i+1 < len(queue) && !t.demo() {
		args.Next = p.lib[queue[i+1]].ID
	}
	p.app.Go(ui.CapAudioPlay, args, &s, func(err error) {
		if err != nil {
			p.app.Toast(tr.T("Cannot play: %s", err.Error()))
			return
		}
		p.cur, p.stream, p.state, p.byFoc, p.posMS, p.lenMS, p.output = i, s.ID, ui.PlayPlaying, false, 0, 0, s.Output
		if !p.onNow {
			p.onNow = true
			p.app.Push(p.nowScreen())
		}
		p.refresh()
	})
}

func (p *player) control(a ui.AudioControlArgs) {
	if p.stream == "" {
		return
	}
	a.ID = p.stream
	p.app.Go(ui.CapAudioControl, a, nil, func(err error) {
		if err != nil {
			p.app.Toast(err.Error())
		}
	})
}

type streamEvent struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	ByFocus bool   `json:"by_focus"`
	PosMS   int    `json:"pos_ms"`
	LenMS   int    `json:"len_ms"`
	Media   string `json:"media"`
	Output  string `json:"output"`
}

func (p *player) onAudio(e streamEvent) {
	if e.ID != p.stream || p.stream == "" {
		return
	}
	p.posMS, p.lenMS, p.byFoc = e.PosMS, e.LenMS, e.ByFocus
	if e.Output != "" {
		p.output = e.Output
	}
	if t := p.playing(); t != nil && e.Media != "" && e.Media != t.ID && e.State != ui.PlayEnded {
		// The queue moved on by itself: queue the one after.
		for k := p.cur + 1; k < len(p.queue); k++ {
			if p.lib[p.queue[k]].ID == e.Media {
				p.cur = k
				p.control(ui.AudioControlArgs{Op: ui.OpQueue, Next: p.nextID(), ReplayGain: p.set.ReplayGain})
				break
			}
		}
	}
	switch e.State {
	case ui.PlayEnded:
		// The queue plays on by itself between library tracks; a track
		// ends here when nothing could be queued after it (a demo track).
		p.stream = ""
		if p.state == ui.PlayPlaying && p.cur+1 < len(p.queue) {
			p.start(p.queue, p.cur+1)
			return
		}
		p.state = ui.PlayStopped
	default:
		p.state = e.State
	}
	p.refresh()
}

func (p *player) load() {
	var lib []track
	_ = p.app.Call(ui.CapMediaList, map[string]string{"kind": "audio"}, &lib)
	cur := p.playing()
	p.queueIDs = p.queueIDs[:0]
	for _, i := range p.queue {
		p.queueIDs = append(p.queueIDs, p.lib[i].ID)
	}
	p.lib = lib
	if cur == nil {
		return
	}
	// The queue's indexes were into the old list: keep what still exists.
	ids := map[string]int{}
	for i, t := range lib {
		ids[t.ID] = i
	}
	var q []int
	at := -1
	for k, old := range p.queueIDs {
		if i, ok := ids[old]; ok {
			if k == p.cur {
				at = len(q)
			}
			q = append(q, i)
		}
	}
	p.queue, p.cur = q, at
}

func (p *player) cycle(node string) {
	switch node {
	case "replaygain":
		p.set.ReplayGain = map[string]string{"": "track", "track": "album", "album": ""}[p.set.ReplayGain]
		if p.stream != "" {
			p.control(ui.AudioControlArgs{Op: ui.OpQueue, Next: p.nextID(), ReplayGain: p.set.ReplayGain})
		}
	case "dsd":
		p.set.DSD = map[string]string{"auto": "dop", "dop": "pcm", "pcm": "auto"}[p.set.DSD]
		p.control(ui.AudioControlArgs{Op: ui.OpOptions, DSD: p.set.DSD})
	}
	p.saveSettings()
	p.app.Update(p.settingsScreen())
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	p := &player{app: app, cur: -1, state: ui.PlayStopped}
	p.loadSettings()
	app.OnStart = func(string) {
		p.load()
		app.Reset(p.homeScreen())
	}
	app.OnPopped = func(screen string) {
		if screen == "now" {
			p.onNow = false
		}
	}
	app.OnLifecycle = func(state string) {
		// Music copied in from a computer shows up when the app comes back.
		if state == ui.StateForeground {
			p.load()
			p.refresh()
		}
	}
	app.OnEvent = func(name, value string) {
		if name == "media" {
			// The media keys (headset, keyboard) through the system.
			switch {
			case value == "next" && p.cur+1 < len(p.queue):
				p.start(p.queue, p.cur+1)
			case value == "previous" && p.cur > 0:
				p.start(p.queue, p.cur-1)
			}
			return
		}
		if name != "audio" {
			return
		}
		var e streamEvent
		if json.Unmarshal([]byte(value), &e) == nil {
			p.onAudio(e)
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		switch e.Action {
		case "play":
			if i, ok := p.listIndex(e.Screen, e.Node); ok {
				p.start(p.list(e.Screen), i)
			}
		case "info":
			if i, ok := p.listIndex(e.Screen, e.Node); ok {
				app.Push(p.infoScreen(p.lib[p.list(e.Screen)[i]]))
			} else if t := p.playing(); t != nil {
				app.Push(p.infoScreen(*t))
			}
		case ui.ActionOpen:
			switch {
			case strings.HasPrefix(e.Node, "a:"):
				for _, a := range albums(p.lib) {
					if "a:"+a.key == e.Node {
						app.Push(p.albumScreen(a))
					}
				}
			case strings.HasPrefix(e.Node, "r:"):
				for _, a := range artists(p.lib, albums(p.lib)) {
					if "r:"+a.name == e.Node {
						app.Push(p.artistScreen(a))
					}
				}
			case strings.HasPrefix(e.Node, "d:"):
				app.Push(p.folderScreen(strings.TrimPrefix(e.Node, "d:")))
			}
		case "albums":
			app.Push(p.albumsScreen())
		case "artists":
			app.Push(p.artistsScreen())
		case "folders":
			app.Push(p.folderScreen(""))
		case "settings":
			app.Push(p.settingsScreen())
		case "cycle":
			p.cycle(e.Node)
		case "now":
			if p.playing() != nil {
				p.onNow = true
				app.Push(p.nowScreen())
			}
		case "toggle":
			switch {
			case p.state == ui.PlayPlaying:
				p.control(ui.AudioControlArgs{Op: ui.OpPause})
			case p.state == ui.PlayPaused:
				p.control(ui.AudioControlArgs{Op: ui.OpResume})
			default:
				if p.queue == nil {
					p.queue = p.list("list")
				}
				p.start(p.queue, max(0, p.cur))
			}
		case "next":
			if len(p.queue) > 0 {
				p.start(p.queue, (p.cur+1)%len(p.queue))
			}
		case "prev":
			if len(p.queue) > 0 {
				p.start(p.queue, (p.cur-1+len(p.queue))%len(p.queue))
			}
		case "fwd", "back":
			step := seekStep
			if e.Action == "back" {
				step = -step
			}
			at := max(0, p.posMS+step)
			if p.lenMS > 0 {
				at = min(at, p.lenMS-500)
			}
			p.posMS = at
			p.control(ui.AudioControlArgs{Op: ui.OpSeek, AtMS: int64(at)})
			p.refresh()
		case ui.OpStop:
			p.state = ui.PlayStopped
			p.control(ui.AudioControlArgs{Op: ui.OpStop})
			p.refresh()
		}
	}
	// Progress: ask audiod once a second while the now-playing screen shows.
	go func() {
		for range time.Tick(time.Second) {
			app.Post(func() {
				if !p.onNow || p.stream == "" || p.state != ui.PlayPlaying {
					return
				}
				var st []streamEvent
				if app.Call(ui.CapAudioState, nil, &st) == nil {
					for _, s := range st {
						if s.ID == p.stream {
							p.onAudio(s)
						}
					}
				}
			})
		}
	}()
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
