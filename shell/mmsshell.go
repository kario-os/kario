package shell

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"os"
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The shell's side of class 0 messages, multimedia messages and alerts
// for an area (32-messaging.md §12, §11, §7.1).

// smsFlash shows a class 0 message at once: a system dialog over whatever
// is on the screen (the lock screen too) that offers to keep it. smsd keeps
// nothing until the user does; telephonyd lets the message go once the
// dialog was shown.
func (c *Core) smsFlash(m ui.SMS) {
	if !c.smsShown(m.ID) {
		return
	}
	m.Name = c.contactName(m.Peer)
	c.sms.flashes++
	c.Wake("flash")
	c.pushOverlay(&flashOverlay{msg: m, at: time.Now()})
	c.vibrateMS(300)
}

// FlashesShown counts the class 0 messages shown (selftest).
func (c *Core) FlashesShown() int { return c.sms.flashes }

// flashOverlay is a class 0 message: OK keeps it in Messages, Back closes
// it (the message is gone).
type flashOverlay struct {
	msg ui.SMS
	at  time.Time
}

func (o *flashOverlay) title(c *Core) string {
	return tr.T("Flash message from %s", firstNonEmpty(o.msg.Name, ui.NumberText(o.msg.Peer)))
}

func (o *flashOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyEnter:
		c.popOverlay(o)
		id := o.msg.ID
		go func() {
			var saved ui.SMS
			err := c.smsRPC(svc.SMSFlashSave, ui.SMSIDArgs{ID: id}, &saved)
			c.Post(func() {
				if err != nil {
					c.showToast(tr.T("Failed: %v", err))
					return
				}
				c.showToast(tr.T("Saved to Messages"))
			})
		}()
	case KeyEsc:
		c.popOverlay(o)
	}
}

func (o *flashOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bg := colMenu
	can.Fill(0, 1, W, H-1, st(colFG, bg))
	center := func(y int, s string, sty cv.Style) { can.Text((W-cv.StringWidth(s))/2, y, s, sty, W) }
	center(3, o.title(c), bold(st(colWarn, bg)))
	for i, l := range cv.Wrap(o.msg.Text, W-4) {
		if 5+i >= H-4 {
			break
		}
		can.Text(2, 5+i, l, st(colFG, bg), W-4)
	}
	center(H-3, tr.T("Enter save        Esc close"), bold(st(colFG, bg)))
}

func (o *flashOverlay) frame(c *Core) *FrameOverlay {
	return &FrameOverlay{Kind: "confirm", Title: o.title(c), Lines: []string{o.msg.Text}, Choices: []string{tr.T("Save"), tr.T("Close")},
		Footer: []string{tr.T("OK saves · Back closes")}}
}

// smsImportParts moves the pictures of a received multimedia message into
// the media library (the album), once each, and tells smsd, which then
// lets its copy go; the default SMS app may see them. A picture the
// library does not take (not PNG, JPEG or GIF) stays with smsd.
func (c *Core) smsImportParts(m ui.SMS) {
	if m.Dir != "in" || m.Folder != "" || m.MMS != "" || c.media == nil {
		return
	}
	for i, p := range m.Parts {
		key := fmt.Sprintf("%s/%d", m.ID, i)
		if !p.Local || p.Media != "" || !strings.HasPrefix(p.Type, "image/") || c.sms.importing[key] {
			continue
		}
		if c.sms.importing == nil {
			c.sms.importing = map[string]bool{}
		}
		c.sms.importing[key] = true
		id, index, at := m.ID, i, time.Unix(m.At, 0)
		name := tr.T("MMS from %s", firstNonEmpty(m.Name, ui.NumberText(m.Peer)))
		go func() {
			var part svc.SMSPartRes
			err := c.smsRPC(svc.SMSPart, svc.SMSPartArgs{ID: id, Index: index}, &part)
			c.Post(func() {
				defer delete(c.sms.importing, key)
				if err != nil {
					c.log.Printf("%s: part %d: %v", id, index, err)
					return
				}
				raw, err := libraryPicture(part.Data)
				var it *mediaItem
				if err == nil {
					it, err = c.media.add(raw, name, at)
				}
				if err != nil {
					c.log.Printf("%s: part %d stays with phone-smsd: %v", id, index, err)
					return
				}
				if a := c.smsApp(); a != nil && a.M.installed {
					c.media.grant(a.M.ID, it)
				}
				media := ui.MediaPrefix + it.ID
				c.log.Printf("%s: part %d is now %s", id, index, media)
				go func() {
					if err := c.smsRPC(svc.SMSPartMedia, svc.SMSPartArgs{ID: id, Index: index, Media: media}, nil); err != nil {
						c.log.Printf("%s: telling phone-smsd about %s: %v", id, media, err)
					}
				}()
			})
		}()
	}
}

// libraryPicture is a picture as the library takes it: PNG and JPEG as
// they are, a GIF's first frame as PNG.
func libraryPicture(raw []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if format != "gif" {
		return raw, nil
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxPhotoSide || cfg.Height > maxPhotoSide {
		return nil, errors.New("picture too large")
	}
	img, err := gif.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return encodePNG(img)
}

// smsSendReq is what smsd gets for sms.send: the app's photos (ones it may
// see) read from the library.
func (c *Core) smsSendReq(a *App, args ui.SMSSendArgs) (svc.SMSSendReq, error) {
	req := svc.SMSSendReq{To: args.To, Text: args.Text, ID: args.ID, Subject: args.Subject, At: args.At, Zone: args.Zone}
	if len(args.Media) > ui.MaxMMSMedia {
		return req, fmt.Errorf("at most %d pictures a message", ui.MaxMMSMedia)
	}
	for _, src := range args.Media {
		it := c.media.get(src)
		if it == nil || it.video() || !c.mediaAllowed(a, it) {
			return req, errors.New(tr.T("No such photo"))
		}
		raw, err := os.ReadFile(c.media.path(it))
		if err != nil {
			return req, err
		}
		req.Parts = append(req.Parts, svc.SMSPartIn{Type: it.ui().Mime, Name: it.Name, Data: raw, Media: ui.MediaPrefix + it.ID})
	}
	return req, nil
}

// alertFixAge: a position this old still says where the phone is, for an
// alert's area.
const alertFixAge = 30 * time.Minute

// What alertWhere decides about an alert with an area.
const alertOutside = "outside"

// alertWhere decides an alert with an area against the phone's position:
// alertOutside when the last fix (at most alertFixAge old) is outside
// every shape by more than its accuracy; otherwise why it is shown ("" for
// an alert without an area). An alert is never kept from a phone that
// does not know where it is (WEA 3.0's rule: better shown outside than
// missed inside).
func (c *Core) alertWhere(al ui.SMSAlert) string {
	if len(al.Area) == 0 {
		return ""
	}
	if c.settings.LocationOff || c.loc.lastAt.IsZero() || time.Since(c.loc.lastAt) > alertFixAge {
		return "position unknown, shown"
	}
	f := c.loc.last
	for _, a := range al.Area {
		if a.InArea(f.Lat, f.Lon, f.AccuracyM) {
			return "inside its area"
		}
	}
	return alertOutside
}

// AlertsOutside counts the alerts not shown, the phone being outside
// their area (selftest).
func (c *Core) AlertsOutside() int { return c.sms.outside }
