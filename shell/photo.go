package shell

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
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
	case KeyFirst:
		o.sel = 0
	case KeyLast:
		o.sel = n - 1
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
	c.drawPickerGrid(can, o.tiles(), o.sel, 3, H-4)
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
func (c *Core) pickPhoto(a *App, origin string, maxSide int, reply func(any, error)) {
	if maxSide != 0 && (maxSide < ui.PhotoSideMin || maxSide > ui.PhotoSideMax) {
		reply(nil, fmt.Errorf("max_side must be %d to %d", ui.PhotoSideMin, ui.PhotoSideMax))
		return
	}
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
		if maxSide == 0 {
			reply(withFiles{data: it.ui(), files: []*os.File{f}}, nil)
			return
		}
		item, lg := it.ui(), c.log
		go func() {
			small, err := shrunkPhoto(f, maxSide, &item)
			if err == nil && small == nil {
				_, err = f.Seek(0, io.SeekStart)
				small = f
			}
			if small != f {
				f.Close()
			}
			if err != nil {
				c.Post(func() { reply(nil, err) })
				return
			}
			if item.Original != 0 {
				lg.Printf("%s: photo %s handed over as %dx%d, %d of %d bytes", a.M.ID, it.ID, item.W, item.H, item.Size, item.Original)
			}
			c.Post(func() { reply(withFiles{data: item, files: []*os.File{small}}, nil) })
		}()
	}})
}

const (
	photoQuality   = 80
	photoPixelsMax = 50_000_000
)

// shrunkPhoto makes the JPEG copy media.pick hands over with max_side, in
// an unlinked file at its start, and describes it in item; nil when the
// album's file is a JPEG within maxSide already.
func shrunkPhoto(f io.Reader, maxSide int, item *ui.MediaItem) (*os.File, error) {
	raw, err := io.ReadAll(io.LimitReader(f, maxPhotoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPhotoBytes {
		return nil, errors.New("photo too large")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > photoPixelsMax {
		return nil, errors.New("photo too large")
	}
	if format == "jpeg" && max(cfg.Width, cfg.Height) <= maxSide {
		return nil, nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	w, h := cfg.Width, cfg.Height
	if long := max(w, h); long > maxSide {
		w, h = max(1, (w*maxSide+long/2)/long), max(1, (h*maxSide+long/2)/long)
		img = scaleCrop(img, w, h)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: photoQuality}); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "kario-photo-*.jpg")
	if err != nil {
		return nil, err
	}
	os.Remove(tmp.Name())
	if _, err := tmp.Write(out.Bytes()); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return nil, err
	}
	item.Original, item.Size = int64(len(raw)), int64(out.Len())
	item.W, item.H, item.Mime = w, h, "image/jpeg"
	return tmp, nil
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
