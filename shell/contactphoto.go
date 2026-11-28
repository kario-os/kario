package shell

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"strings"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// Contacts' photos (contacts.photo): a photo of the library the app was
// handed (the system picker, media.pick) becomes a contact's picture, its
// middle square scaled to ui.ContactPhotoSide and kept as a JPEG of at
// most ui.ContactPhotoBytes in the contacts store (contacts.Store's
// photos), the bytes a sync sends. The system draws them (an image src of
// ui.ContactPhotoPrefix and the Photo) for apps with the contacts
// permission: in an item's icon's place, or as an image node.

func (c *Core) contactPhoto(a *App, args ui.ContactPhotoArgs, reply func(any, error)) {
	it := c.media.get(args.Media)
	if it == nil || it.video() || !c.mediaAllowed(a, it) {
		reply(nil, errors.New(tr.T("No such photo")))
		return
	}
	path := c.media.path(it)
	go func() {
		data, err := avatarJPEG(path)
		c.Post(func() {
			if err != nil {
				c.log.Printf("%s: a contact's photo of %s: %v", a.M.ID, it.ID, err)
				reply(nil, errors.New(tr.T("Not a photo the contacts take")))
				return
			}
			key, err := c.contactBook().PutPhoto(data)
			if err != nil {
				reply(nil, contactsErr(err))
				return
			}
			c.log.Printf("%s: a contact's photo of %s (%d bytes)", a.M.ID, it.ID, len(data))
			reply(ui.ContactPhotoRes{Photo: key}, nil)
		})
	}()
}

// avatarJPEG is the photo at path as a contact's: its middle square, at
// most ui.ContactPhotoSide a side, as a JPEG within ui.ContactPhotoBytes
// (the quality lowered as it needs).
func avatarJPEG(path string) ([]byte, error) {
	raw, err := readLimited(path, maxPhotoBytes)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	side := min(ui.ContactPhotoSide, b.Dx(), b.Dy())
	if side < 1 {
		return nil, errors.New("an empty picture")
	}
	square := scaleCrop(img, side, side)
	for q := 85; ; q -= 15 {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, square, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		if out.Len() <= ui.ContactPhotoBytes || q <= 25 {
			if out.Len() > ui.ContactPhotoBytes {
				return nil, fmt.Errorf("%d bytes at quality %d", out.Len(), q)
			}
			return out.Bytes(), nil
		}
	}
}

// contactPhotoRGB is a contact's photo as side × side pixels (cached), nil
// when the app in front may not see it or there is none.
func (c *Core) contactPhotoRGB(src string, side int) *FrameImage {
	key, ok := strings.CutPrefix(src, ui.ContactPhotoPrefix)
	a := c.current()
	if !ok || side < 1 || a != nil && !a.system && !c.broker.holds(a, ui.PermContacts) {
		return nil
	}
	ck := fmt.Sprintf("contact:%s@%d", key, side)
	if f := c.media.cache[ck]; f != nil {
		return f
	}
	raw, err := c.contactBook().Photo(key)
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	small := scaleCrop(img, side, side)
	pix := make([]byte, side*side*3)
	for i := 0; i < side*side; i++ {
		copy(pix[3*i:3*i+3], small.Pix[4*i:4*i+3])
	}
	f := encodeRGB(side, side, pix, false, false)
	if len(c.media.cache) > 64 {
		c.media.cache = map[string]*FrameImage{}
	}
	c.media.cache[ck] = f
	return f
}

// contactPhotoCanvas is a contact's photo in the text renderer's cells:
// side pixels a side, two to a cell's height; nil without one.
func (c *Core) contactPhotoCanvas(src string, side int, secure bool) *cv.Canvas {
	f := c.contactPhotoRGB(src, side)
	if f == nil {
		return nil
	}
	pix, _ := base64.StdEncoding.DecodeString(f.RGB)
	rows := (f.H + 1) / 2
	b := cv.New(f.W, rows, stBase)
	b.Pixels(0, 0, f.W, rows, func(i, j int) cv.RGB {
		k := (min(j, f.H-1)*f.W + i) * 3
		return cv.RGB{R: pix[k], G: pix[k+1], B: pix[k+2]}
	}, secure)
	return b
}

// isContactPhoto: an item's src that takes its icon's place.
func isContactPhoto(n *ui.Node) bool {
	return n.Icon != "" && strings.HasPrefix(n.Src, ui.ContactPhotoPrefix)
}
