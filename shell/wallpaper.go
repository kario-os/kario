package shell

import (
	"bytes"
	"errors"
	"image"
	"os"
	"path/filepath"

	"github.com/kario-os/kario/media"
	"github.com/kario-os/kario/ui"
)

// Wallpapers (Pictures › Set as wallpaper, Settings › Wallpaper): the
// standby screen's picture, one for the unlocked phone and one while the
// keypad is locked. A wallpaper is a copy of the photo the user chose, kept
// with the system settings, so deleting the photo leaves it. The pixel
// renderer gets it cut to the screen in a surface of the shell's own,
// written once per picture and size.

type wallpaper struct {
	path string
	w, h int
	surf *media.Surface
	seq  uint32 // a new picture changes the frame, so that the new surface is sent
}

func (c *Core) wallpaperPath(which, ext string) string {
	return filepath.Join(c.cfg.StateDir, "shell", "wallpaper-"+which+"."+ext)
}

// setWallpaper puts a photo the app may see on the standby screen
// (target idle), the lock screen (lock) or both; apps outside the system
// image ask the user first.
func (c *Core) setWallpaper(a *App, args ui.WallpaperArgs, reply func(any, error)) {
	it := c.media.get(args.Media)
	if it == nil || !c.mediaAllowed(a, it) {
		reply(nil, errors.New("no such photo"))
		return
	}
	targets, name := map[string][]string{ui.WallpaperIdle: {ui.WallpaperIdle}, ui.WallpaperLock: {ui.WallpaperLock}, ui.WallpaperBoth: {ui.WallpaperIdle, ui.WallpaperLock}}[args.Target],
		map[string]string{ui.WallpaperIdle: tr.T("standby"), ui.WallpaperLock: tr.T("lock screen"), ui.WallpaperBoth: tr.T("standby and lock screen")}[args.Target]
	if targets == nil {
		reply(nil, errors.New("target is idle, lock or both"))
		return
	}
	apply := func() {
		raw, err := os.ReadFile(c.media.path(it))
		if err == nil {
			err = os.MkdirAll(filepath.Join(c.cfg.StateDir, "shell"), 0o700)
		}
		for _, t := range targets {
			if err != nil {
				break
			}
			for _, ext := range []string{"png", "jpg"} {
				_ = os.Remove(c.wallpaperPath(t, ext))
			}
			dst := c.wallpaperPath(t, it.ext())
			c.staleWallpaper(t)
			if err = os.WriteFile(dst, raw, 0o600); err == nil {
				if t == ui.WallpaperIdle {
					c.settings.WallpaperIdle = dst
				} else {
					c.settings.WallpaperLock = dst
				}
			}
		}
		if err != nil {
			reply(nil, err)
			return
		}
		c.saveSettings()
		c.log.Printf("%s: wallpaper (%s) from %s", a.M.ID, args.Target, it.ID)
		c.showToast(tr.T("Set as the %s wallpaper", name))
		reply(map[string]bool{"ok": true}, nil)
	}
	if a.system || !a.M.installed {
		apply()
		return
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("“%s” wants to make this photo the %s wallpaper.", a.M.Name, name), yes: tr.T("Set as wallpaper"), no: tr.T("Cancel"),
		onYes: apply, onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// clearWallpaper: 1 the standby picture, 2 the lock screen's, 3 both.
func (c *Core) clearWallpaper(which int) {
	for bit, t := range map[int]string{1: ui.WallpaperIdle, 2: ui.WallpaperLock} {
		if which&bit == 0 {
			continue
		}
		for _, ext := range []string{"png", "jpg"} {
			_ = os.Remove(c.wallpaperPath(t, ext))
		}
		c.staleWallpaper(t)
		if t == ui.WallpaperIdle {
			c.settings.WallpaperIdle = ""
		} else {
			c.settings.WallpaperLock = ""
		}
	}
	c.saveSettings()
}

// staleWallpaper makes the next frame cut the picture again: a new one may
// be written under the same name.
func (c *Core) staleWallpaper(which string) {
	if wp := c.wallpapers[which]; wp != nil {
		wp.path = ""
	}
}

// wallpaperFrame is the picture the standby screen shows now (the lock
// screen's while the keypad is locked, if there is one), for renderers
// that take surfaces; nil without one.
func (c *Core) wallpaperFrame() *FrameImage {
	which, path := ui.WallpaperIdle, c.settings.WallpaperIdle
	if c.keylock && c.settings.WallpaperLock != "" {
		which, path = ui.WallpaperLock, c.settings.WallpaperLock
	}
	d := c.screenDev
	if path == "" || c.renderer == nil || !c.renderer.Has("shm") || d.Width <= 0 || d.Height <= 0 {
		return nil
	}
	if c.wallpapers == nil {
		c.wallpapers = map[string]*wallpaper{}
	}
	wp := c.wallpapers[which]
	if wp == nil || wp.path != path || wp.w != d.Width || wp.h != d.Height {
		seq := uint32(1)
		if wp != nil {
			seq = wp.seq + 1
			wp.surf.Close()
			delete(c.wallpapers, which)
		}
		surf, err := wallpaperSurface(path, d.Width, d.Height)
		if err != nil {
			c.log.Printf("wallpaper %s: %v", which, err)
			return nil
		}
		wp = &wallpaper{path: path, w: d.Width, h: d.Height, surf: surf, seq: seq}
		c.wallpapers[which] = wp
	}
	key := "wallpaper:" + which
	c.frameSurfs[key] = true
	return &FrameImage{W: wp.w, H: wp.h, Surface: key, Seq: wp.seq}
}

func wallpaperSurface(path string, w, h int) (*media.Surface, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return rgbaSurface(scaleCrop(img, w, h))
}

// wallpaperFile hands a wallpaper's surface to the renderer.
func (c *Core) wallpaperFile(id string) (*os.File, int, int, bool) {
	for which, wp := range c.wallpapers {
		if id == "wallpaper:"+which && wp != nil {
			return wp.surf.File(), wp.w, wp.h, true
		}
	}
	return nil, 0, 0, false
}
