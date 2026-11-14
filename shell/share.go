package shell

import (
	"errors"
	"fmt"
	"net/url"
	"sort"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// The share sheet (Android's share intent, iOS's activity sheet): the system
// lists the apps whose manifest accepts the kind, the user picks one, and the
// system opens it with a "share:<kind>:<payload>" deep link. A shared photo
// is granted to the target. The sender learns only whether something was
// shared, not which apps the user has.

const shareTextMax = 4 << 10

func (m *Manifest) accepts(kind string) bool {
	for _, k := range m.Share {
		if k == kind {
			return true
		}
	}
	return false
}

func (c *Core) share(a *App, origin string, args ui.ShareArgs, reply func(any, error)) {
	var payload, what string
	var photo *mediaItem
	var file *fileItem
	switch args.Kind {
	case "text":
		if args.Text == "" || len(args.Text) > shareTextMax {
			reply(nil, errors.New("share text must be 1 to 4096 bytes"))
			return
		}
		payload, what = args.Text, tr.T("a piece of text")
	case "image":
		photo = c.media.get(args.Media)
		if photo == nil || !c.mediaAllowed(a, photo) {
			reply(nil, errors.New("no such photo"))
			return
		}
		payload, what = ui.MediaPrefix+photo.ID, tr.T("the photo “%s”", photo.Name)
	case "link":
		u, err := url.Parse(args.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(args.URL) > shareTextMax || len(args.Text) > 256 {
			reply(nil, errors.New("a link is an http(s) URL with an optional title"))
			return
		}
		payload, what = args.URL+"\n"+args.Text, tr.T("the link “%s”", cv.Truncate(firstNonEmpty(args.Text, u.Host), 16))
	case "file":
		file = c.files.get(args.File)
		if file == nil || !c.files.Grants[permKey(a, "")][file.ID] {
			reply(nil, errors.New("no such file"))
			return
		}
		payload, what = file.ID, tr.T("the file “%s”", file.Name)
	default:
		reply(nil, errors.New(`kind must be "text", "image", "link" or "file"`))
		return
	}
	var targets []*App
	for _, t := range c.apps {
		if t != a && t.M.accepts(args.Kind) {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		reply(nil, errors.New("no app accepts this"))
		return
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].M.Order < targets[j].M.Order })
	c.pushOverlay(&shareOverlay{from: a, origin: origin, what: what, targets: targets, done: func(t *App) {
		if t == nil {
			reply(nil, errors.New(ui.ErrCancelled))
			return
		}
		if photo != nil {
			c.media.grant(t.M.ID, photo)
		}
		if file != nil {
			c.files.grant(t.M.ID, file)
		}
		c.log.Printf("%s: shared %s with %s", a.M.ID, args.Kind, t.M.ID)
		reply(map[string]bool{"shared": true}, nil)
		c.Launch(t, "share:"+args.Kind+":"+payload)
	}})
}

type shareOverlay struct {
	from   *App
	origin string // the page inside the app that shares, if not the app
	what   string
	// title and said replace the share sheet's (the intent picker).
	title, said string
	targets     []*App
	sel         int
	done        func(*App)
}

func (o *shareOverlay) key(c *Core, k Key) {
	n := len(o.targets)
	switch k.Name {
	case KeyLeft:
		o.sel = max(0, o.sel-1)
	case KeyRight:
		o.sel = min(n-1, o.sel+1)
	case KeyUp:
		o.sel = max(0, o.sel-c.menuColumns())
	case KeyDown:
		o.sel = min(n-1, o.sel+c.menuColumns())
	case KeyEnter:
		c.popOverlay(o)
		o.done(o.targets[o.sel])
	case KeyEsc:
		c.popOverlay(o)
		o.done(nil)
	case KeyRunes:
		if r := k.Runes; len(r) == 1 && r[0] >= '1' && r[0] <= '9' && int(r[0]-'1') < n {
			c.popOverlay(o)
			o.done(o.targets[r[0]-'1'])
		}
	}
}

func (o *shareOverlay) heading() string {
	if o.title != "" {
		return o.title
	}
	return tr.T("Share to…")
}

func (o *shareOverlay) line() string {
	if o.said != "" {
		return o.said
	}
	if o.origin != "" {
		return tr.T("In “%s”, %s shares %s", o.from.M.Name, o.origin, o.what)
	}
	return tr.T("“%s” shares %s", o.from.M.Name, o.what)
}

func (o *shareOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	can.Fill(0, 1, W, H-1, stBase)
	can.Fill(0, 1, W, 1, st(colFG, colTitle))
	can.Text(1, 1, o.heading(), bold(st(colTrusted, colTitle)), W-2)
	var tiles []*ui.Node
	for i, t := range o.targets {
		tiles = append(tiles, &ui.Node{ID: fmt.Sprint(i), Icon: t.M.Icon, Title: t.M.Name})
	}
	y := 3
	for i := 0; i < len(tiles) && y < H-4; i += c.menuColumns() {
		blk := c.drawTileRow(tiles[i:min(len(tiles), i+c.menuColumns())], fmt.Sprint(o.sel), W, c.menuColumns())
		can.Blit(0, y, blk)
		y += blk.H
	}
	can.Text(1, H-4, cv.Truncate(o.line(), W-2), st(colDim, colBG), W-2)
	can.Fill(0, H-1, W, 1, st(colFG, colSoft))
	can.Text(1, H-1, tr.T("⏎ choose   1–9 pick   Esc cancel"), st(colFG, colSoft), W-2)
}

func (o *shareOverlay) frame(c *Core) *FrameOverlay {
	f := &FrameOverlay{Kind: "picker", Title: o.heading(), Choice: o.sel, Columns: c.menuColumns(), Lines: []string{o.line()},
		Footer: []string{tr.T("OK choose · 1–9 pick · Back cancel")}}
	for i, t := range o.targets {
		f.Tiles = append(f.Tiles, FrameTile{Icon: t.M.Icon, Image: t.M.IconPath(), Title: t.M.Name, Focused: i == o.sel})
	}
	return f
}
