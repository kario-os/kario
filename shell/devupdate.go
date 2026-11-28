package shell

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/devctl"
)

// kario device update (adb sideload): a system update bundle the computer
// sends goes the way one from the update server goes (update.go). RAUC
// checks its signature against the image's keyring and that it is for
// this machine, and refuses it otherwise; it writes the other slot, which
// starts on trial at the next restart and is left for the last good one
// when it does not come up healthy; the install is remembered for the
// update server's report like any other. The bundle waits on the data
// partition while RAUC reads it.

// devUpdateEvery: how often the computer hears the install's progress.
const devUpdateEvery = 500 * time.Millisecond

// sideloadable: the phone takes a system update now; on the loop.
func (c *Core) sideloadable() error {
	u := &c.update
	u.init()
	switch st := c.updateStatus(); {
	case !st.Supported:
		return errors.New("this phone has no A/B system slots (RAUC): its system is written by the board's own flashing (on the OnePlus 6T, yocto/fajita-flash.sh from fastboot)")
	case u.state == "installing":
		return errors.New("an update is being installed")
	}
	return nil
}

// bundleVersion is the version a bundle's manifest gives (rauc info,
// which checks its signature first), "" when RAUC does not say; off the
// loop.
func bundleVersion(rauc string, info func(string) string, path string) string {
	if info != nil {
		return info(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, rauc, "info", "--output-format=json", path).Output()
	if err != nil {
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(out, &m) != nil {
		return ""
	}
	return m.Version
}

// receiveFile writes size bytes from r to path, whole or not at all.
func receiveFile(r io.Reader, path string, size int64) error {
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.CopyN(f, r, size)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("the bundle: %d of %d bytes: %w", n, size, err)
	}
	return nil
}

// devUpdate receives the bundle, installs it and answers with RAUC's
// progress, then how it ended. An install once started goes on if the
// computer goes. Once received, the bundle is gone before the answer
// that ends the request, so the computer never hears "done" with it
// still on the phone.
func (c *Core) devUpdate(conn net.Conn, r *bufio.Reader, req devctl.Request) {
	bundle := filepath.Join(c.cfg.StateDir, "shell", "sideload.raucb")
	var rauc string
	var info func(string) string
	sideloaded := func() {}
	sent := false
	reply := func(rep devctl.Reply) {
		if sent {
			_ = os.Remove(bundle)
		}
		_ = devctl.WriteReply(conn, rep)
		sideloaded()
	}
	refuse := func(err error) {
		c.Post(func() { c.log.Printf("dev link: update: %v", err) })
		reply(devctl.Reply{Error: err.Error()})
	}
	err := c.onLoop(func() error {
		if err := c.devGate(req.Via); err != nil {
			return err
		}
		rauc, info = c.update.rauc, c.update.info
		if f := c.update.sideloaded; f != nil {
			sideloaded = f
		}
		return c.sideloadable()
	})
	if err == nil {
		err = os.MkdirAll(filepath.Dir(bundle), 0o700)
	}
	if err == nil {
		err = devSpace(filepath.Dir(bundle), req.Size)
	}
	if err != nil {
		refuse(err)
		return
	}
	if devctl.WriteReply(conn, devctl.Reply{OK: true}) != nil {
		return
	}
	sent = true
	if err := receiveFile(r, bundle, req.Size); err != nil {
		refuse(err)
		return
	}
	version := bundleVersion(rauc, info, bundle)
	done := make(chan error, 1)
	err = c.onLoop(func() error {
		if err := c.devGate(req.Via); err != nil {
			return err
		}
		if err := c.sideloadable(); err != nil {
			return err
		}
		u := &c.update
		offer := u.offer
		u.offer = &updateOffer{Version: version, URL: bundle}
		c.log.Printf("dev link: installing the system update the computer sent (version %q, %d bytes)", version, req.Size)
		c.showToast(tr.T("Installing the system update from the computer"))
		c.startInstall(func(_ any, err error) {
			u.offer = offer
			done <- err
		})
		return nil
	})
	if err != nil {
		refuse(err)
		return
	}
	shown := -1
	for {
		select {
		case err := <-done:
			if err != nil {
				var out string
				_ = c.onLoop(func() error { out = c.update.out; return nil })
				refuse(fmt.Errorf("not installed: %s", cmp.Or(lastLines(out, 2), err.Error())))
				return
			}
			var st UpdateStatus
			_ = c.onLoop(func() error { st = c.updateStatus(); return nil })
			text := fmt.Sprintf("installed %s into slot %s: it starts at the next restart (kario device reboot), on trial; if it does not come up healthy the phone goes back to slot %s",
				cmp.Or(version, "the update"), cmp.Or(st.Pending, "?"), cmp.Or(st.Good, st.Slot, "?"))
			reply(devctl.Reply{OK: true, Text: text})
			return
		case <-time.After(devUpdateEvery):
			var pct int
			_ = c.onLoop(func() error { pct = c.update.percent; return nil })
			if pct != shown {
				shown = pct
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true, More: true, Text: fmt.Sprintf("%3d%% installing", pct)})
			}
		}
	}
}
