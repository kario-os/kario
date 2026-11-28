package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/ui"
)

// kario device screenshot: a PNG of the screen as the person sees it. The
// renderer on the panel hands its picture to no process
// (23-renderer-protocol.md §5), so another instance of the same renderer,
// headless and of its own, draws the frame the panel's has and writes it
// as the selftest's snap steps do. A secure screen is drawn masked: the
// frame keeps what surrounds it and has one secure picture in place of
// what it shows, which renderers mask in every snapshot.

const errDevNoPixels = "this Shell draws no pixels (no renderer): there is nothing to take a screenshot of"

// devShot is a screenshot to draw: the renderer and the panel as the
// Shell lays out for it, the frame and the shared memory it names.
type devShot struct {
	renderer string
	dev      Device
	env      []string

	frame  []byte
	surfs  []devSurface
	masked string
}

type devSurface struct {
	id   string
	f    *os.File
	w, h int
}

// secureScreen is why the screen is a secure one ("" when it is not): a
// PIN being entered or set, a trusted confirmation (payment), a pairing
// code, or a page with secure content (a payment code, the switch's QR
// code and hotspot password).
func (c *Core) secureScreen() string {
	if n := len(c.overlays); n > 0 {
		switch o := c.overlays[n-1].(type) {
		case *authOverlay:
			return "a trusted confirmation"
		case *lockPadOverlay, *lockSetOverlay, *simPinOverlay, *simSetOverlay, *codeOverlay:
			return "a PIN entry"
		case *confirmOverlay:
			if o.secret {
				return "a pairing code"
			}
		case *pairCodeOverlay:
			return "a pairing code"
		}
	}
	if v := c.current().top(); v != nil && hasSecure(v.S.Body) {
		return "secure content"
	}
	return ""
}

func hasSecure(body []ui.Node) bool {
	for i := range body {
		if body[i].Secure {
			return true
		}
	}
	return false
}

// maskFrame keeps the status bar, the title and the soft keys of a secure
// screen and puts one blank secure picture in place of what it shows.
func maskFrame(f *Frame) {
	const w, h = 3, 4
	f.Body = []FrameNode{{Kind: ui.KindImage, Sel: -1, Image: encodeRGB(w, h, make([]byte, w*h*3), true, true)}}
	f.Overlay, f.Idle, f.IME, f.Boot = nil, nil, nil, nil
	f.Focus, f.Scroll, f.Loading, f.Layout = -1, FrameScroll{Frac: -1}, "", ""
}

// devShotFrame is the screenshot of the screen now; on the loop. The
// shared memory goes to the screenshot's renderer as copies of the
// descriptors, which the loop may close meanwhile.
func (c *Core) devShotFrame() (*devShot, error) {
	if c.gui == nil {
		return nil, errors.New(errDevNoPixels)
	}
	if c.dev.shooting {
		return nil, errors.New("a screenshot is being taken already")
	}
	c.Render()
	f := c.Frame(c.gui.dev)
	s := &devShot{renderer: c.gui.cmd.Path, dev: c.screenDev, env: c.procEnv(os.Environ())}
	if why := c.secureScreen(); why != "" {
		maskFrame(f)
		s.masked = why
	} else {
		for id := range c.frameSurfs {
			file, w, h := c.gui.surface(id)
			if file == nil {
				continue
			}
			fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
			if err != nil {
				s.close()
				return nil, err
			}
			s.surfs = append(s.surfs, devSurface{id: id, f: os.NewFile(uintptr(fd), id), w: w, h: h})
		}
	}
	var err error
	if s.frame, err = json.Marshal(f); err != nil {
		s.close()
		return nil, err
	}
	c.dev.shooting = true
	return s, nil
}

func (s *devShot) close() {
	for _, sf := range s.surfs {
		sf.f.Close()
	}
}

// draw has the screenshot's renderer draw the frame and returns the PNG;
// off the loop. The renderer writes it as nobody on a device, so it is
// read through an os.Root: a link out of the directory is not followed.
func (s *devShot) draw(c *Core) ([]byte, error) {
	dir, err := os.MkdirTemp("", "kario-screenshot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, nobodyID, nobodyID); err != nil {
			return nil, err
		}
	}
	g, _, err := launchRenderer(c, s.renderer, s.dev, s.env, true, "--headless", "--snap-dir", dir)
	if err != nil {
		return nil, fmt.Errorf("the screenshot's renderer: %w", err)
	}
	defer g.Close()
	if !g.info.Has("snap") {
		return nil, fmt.Errorf("the renderer (%s) takes no snapshots", g.info)
	}
	for _, sf := range s.surfs {
		msg, _ := json.Marshal(map[string]any{"t": "surface", "id": sf.id, "w": sf.w, "h": sf.h})
		if _, _, err := g.side.WriteMsgUnix(msg, syscall.UnixRights(int(sf.f.Fd())), nil); err != nil {
			return nil, err
		}
	}
	if _, err := g.in.Write(append(s.frame, '\n')); err != nil {
		return nil, err
	}
	if err := g.snap("screen", false); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile("screen.png")
}

// devScreenshot answers the PNG, saying beside it why it is masked.
func (c *Core) devScreenshot(conn net.Conn, req devctl.Request) {
	var shot *devShot
	err := c.onLoop(func() error {
		if err := c.devGate(req.Via); err != nil {
			return err
		}
		var e error
		shot, e = c.devShotFrame()
		return e
	})
	var png []byte
	if err == nil {
		png, err = shot.draw(c)
		shot.close()
		c.Post(func() { c.dev.shooting = false })
	}
	if err != nil {
		c.Post(func() { c.log.Printf("dev link: screenshot: %v", err) })
		_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
		return
	}
	note := ""
	if shot.masked != "" {
		note = "masked: the screen shows " + shot.masked
	}
	c.Post(func() { c.log.Printf("dev link: screenshot for the computer (%d bytes) %s", len(png), note) })
	if devctl.WriteReply(conn, devctl.Reply{OK: true, Size: int64(len(png)), Note: note}) == nil {
		_, _ = conn.Write(png)
	}
}
