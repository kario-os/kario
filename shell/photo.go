package shell

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The system viewfinder (camera.capture) and photo picker (media.pick):
// system UI above the app, so the app never sees the camera stream or the
// rest of the library. What the user takes or picks is the only thing the
// app gets.

type captureOverlay struct {
	app    *App
	origin string
	stream string
	shoot  func()
	cancel func()
}

func (o *captureOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyEnter:
		c.popOverlay(o)
		o.shoot()
	case KeyEsc:
		c.popOverlay(o)
		o.cancel()
	}
}

func (o *captureOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	can.Fill(0, 1, W, H-1, stBase)
	can.Fill(0, 1, W, 1, st(colFG, colTitle))
	can.Text(1, 1, tr.T("Take a photo · System camera"), bold(st(colTrusted, colTitle)), W-2)
	c.blitStream(can, 1, 2, W-2, H-7, o.stream, false, tr.T("Opening the camera…"))
	can.Text(1, H-5, cv.Truncate(tr.T("Press OK to take it · asked by “%s”", o.app.M.Name), W-2), st(colFG, colBG), W-2)
	can.Text(1, H-4, tr.T("The photo goes into the system's Pictures; the app gets only this one"), st(colDim, colBG), W-2)
	can.Fill(0, H-1, W, 1, st(colFG, colSoft))
	can.Text(1, H-1, tr.T("⏎ take   Esc cancel"), st(colFG, colSoft), W-2)
}

func (o *captureOverlay) frame(c *Core) *FrameOverlay {
	return &FrameOverlay{Kind: "capture", Title: tr.T("Take a photo · System camera"),
		Lines:  []string{tr.T("Press OK to take it · asked by “%s”", o.app.M.Name), tr.T("The photo goes into the system's Pictures; the app gets only this one")},
		Footer: []string{tr.T("OK take · Back cancel")}, Image: c.frameStream(o.stream, mediaW, mediaW, false), Phase: -1}
}

func (c *Core) capturePhoto(a *App, origin string, reply func(any, error)) {
	b := c.broker
	var open struct {
		CamID  string `json:"cam_id"`
		Stream string `json:"stream"`
	}
	b.call(svc.Mediad, svc.MediaCameraOpen, map[string]string{"app": a.M.ID}, &open, func(err error) {
		if err != nil {
			reply(nil, err)
			return
		}
		closeCam := func() {
			go func() { _ = b.rpc(svc.Mediad, svc.MediaCameraClose, map[string]string{"cam_id": open.CamID}, nil) }()
		}
		c.pushOverlay(&captureOverlay{app: a, origin: origin, stream: open.Stream,
			cancel: func() { closeCam(); reply(nil, errors.New(ui.ErrCancelled)) },
			shoot: func() {
				go func() {
					files, err := b.rpcFiles(svc.Mediad, svc.MediaCameraCapture, map[string]string{"cam_id": open.CamID}, nil)
					var raw []byte
					if err == nil && len(files) == 1 {
						raw, err = io.ReadAll(io.LimitReader(files[0], maxPhotoBytes+1))
					} else if err == nil {
						err = errors.New("camera returned no photo")
					}
					closeFiles(files)
					c.Post(func() {
						if err != nil {
							reply(nil, err)
							return
						}
						it, err := c.media.add(raw, tr.T("Camera photo"), c.now())
						if err != nil {
							reply(nil, err)
							return
						}
						c.media.grant(a.M.ID, it)
						c.log.Printf("%s: photo %s taken", a.M.ID, it.ID)
						reply(it.ui(), nil)
					})
				}()
			}})
	})
}

type pickerOverlay struct {
	app    *App
	origin string
	items  []*mediaItem
	sel    int
	done   func(*mediaItem)
}

func (o *pickerOverlay) key(c *Core, k Key) {
	n := len(o.items)
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
		o.done(o.items[o.sel])
	case KeyEsc:
		c.popOverlay(o)
		o.done(nil)
	case KeyRunes:
		if r := k.Runes; len(r) == 1 && r[0] >= '1' && r[0] <= '9' && int(r[0]-'1') < n {
			c.popOverlay(o)
			o.done(o.items[r[0]-'1'])
		}
	}
}

func (o *pickerOverlay) tiles() []*ui.Node {
	var out []*ui.Node
	for i, it := range o.items {
		out = append(out, &ui.Node{ID: fmt.Sprint(i), Icon: initial(tr.T("Pictures")), Title: it.Name})
	}
	return out
}

func (o *pickerOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	can.Fill(0, 1, W, H-1, stBase)
	can.Fill(0, 1, W, 1, st(colFG, colTitle))
	can.Text(1, 1, tr.T("Choose a photo · System album"), bold(st(colTrusted, colTitle)), W-2)
	tiles := o.tiles()
	y := 3
	for i := 0; i < len(tiles) && y < H-4; i += c.menuColumns() {
		row := tiles[i:min(len(tiles), i+c.menuColumns())]
		blk := c.drawTileRow(row, fmt.Sprint(o.sel), W, c.menuColumns())
		can.Blit(0, y, blk)
		y += blk.H
	}
	can.Text(1, H-4, cv.Truncate(tr.T("Only the chosen one goes to “%s”", o.app.M.Name), W-2), st(colDim, colBG), W-2)
	can.Fill(0, H-1, W, 1, st(colFG, colSoft))
	can.Text(1, H-1, tr.T("⏎ choose   1–9 pick   Esc cancel"), st(colFG, colSoft), W-2)
}

func (o *pickerOverlay) frame(c *Core) *FrameOverlay {
	f := &FrameOverlay{Kind: "picker", Title: tr.T("Choose a photo · System album"), Choice: o.sel, Columns: c.menuColumns(),
		Lines:  []string{tr.T("Only the chosen one goes to “%s”", o.app.M.Name)},
		Footer: []string{tr.T("OK choose · 1–9 pick · Back cancel")}}
	for i, it := range o.items {
		t := FrameTile{Icon: initial(tr.T("Pictures")), Title: it.Name, Focused: i == o.sel}
		if thumbShown(i, o.sel, f.Columns) {
			t.Pixels = c.thumbFrame(it, f.Columns)
		}
		f.Tiles = append(f.Tiles, t)
	}
	return f
}

// deletePhoto removes a photo from the library after the system asks the
// user (the photo is the system's, so the app cannot skip the question).
func (c *Core) deletePhoto(a *App, id string, reply func(any, error)) {
	it := c.media.get(id)
	if it == nil {
		reply(nil, errors.New("no such photo"))
		return
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("“%s” wants to delete the photo “%s”. It cannot be restored.", a.M.Name, it.Name),
		onYes: func() {
			c.media.remove(it)
			c.log.Printf("%s: photo %s deleted", a.M.ID, it.ID)
			reply(map[string]bool{"deleted": true}, nil)
		},
		onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// pickPhoto hands the chosen photo over as a read-only file: the app can
// read (and upload) exactly that one.
func (c *Core) pickPhoto(a *App, origin string, reply func(any, error)) {
	var items []*mediaItem
	for _, it := range c.media.newestFirst() {
		if !it.video() {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		reply(nil, errors.New("no photos"))
		return
	}
	c.pushOverlay(&pickerOverlay{app: a, origin: origin, items: items, done: func(it *mediaItem) {
		if it == nil {
			reply(nil, errors.New(ui.ErrCancelled))
			return
		}
		f, err := os.Open(c.media.path(it))
		if err != nil {
			reply(nil, err)
			return
		}
		c.media.grant(a.M.ID, it)
		c.log.Printf("%s: picked photo %s at %s", a.M.ID, it.ID, time.Now().Format("15:04:05"))
		reply(withFiles{data: it.ui(), files: []*os.File{f}}, nil)
	}})
}

// savePhoto stores an image an app hands over (a received picture) in the
// album once the user agrees. The app may see what it saved; the rest of
// the album stays out of its reach, as with the picker.
func (c *Core) savePhoto(a *App, origin string, args ui.MediaSaveArgs, reply func(any, error)) {
	var raw []byte
	var err error
	switch {
	case args.Data != "":
		if len(args.Data) > maxPhotoBytes/3*4+8 {
			reply(nil, errors.New("photo too large"))
			return
		}
		raw, err = base64.StdEncoding.DecodeString(args.Data)
	case strings.HasPrefix(args.Src, "gen:"):
		raw, err = encodePNG(samplePhoto(args.Src, 960, 720))
	default:
		err = errors.New("media.save needs data or a gen: image")
	}
	var cfg image.Config
	if err == nil {
		var format string
		cfg, format, err = image.DecodeConfig(bytes.NewReader(raw))
		if err == nil && format != "png" && format != "jpeg" {
			err = errors.New("not a PNG or JPEG")
		}
	}
	if err != nil {
		reply(nil, err)
		return
	}
	who := a.M.Name
	if origin != "" {
		who += " · " + origin
	}
	name := args.Name
	if name == "" {
		name = tr.T("From %s", who)
	}
	if rs := []rune(name); len(rs) > 32 {
		name = string(rs[:32])
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("Save this picture to the album?\nFrom: %s\n%d×%d", who, cfg.Width, cfg.Height),
		onYes: func() {
			it, err := c.media.add(raw, name, c.now())
			if err != nil {
				reply(nil, err)
				return
			}
			c.media.grant(a.M.ID, it)
			c.log.Printf("%s: saved photo %s to the album", a.M.ID, it.ID)
			c.showToast(tr.T("Saved to the album"))
			reply(it.ui(), nil)
		},
		onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}
