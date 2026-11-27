package shell

import (
	"bytes"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/usbshare"
)

// A computer's thumbnails (MTP's GetThumb) are the gallery's, as JPEG: a
// photo's, made again from the photo when missing, and a video's once
// mediad gave its still. Music and Files keep none.
func TestUSBThumbnails(t *testing.T) {
	c := loopCore(t, t.TempDir())
	var lib usbshare.Library = usbLibrary{c}
	thumbs, ok := lib.(usbshare.Thumbs)
	if !ok {
		t.Fatal("the Shell's library keeps no thumbnails")
	}
	for _, f := range lib.Folders() {
		if f.Thumbs != (f.ID == "photos" || f.ID == "videos") {
			t.Errorf("%s: Thumbs %v", f.ID, f.Thumbs)
		}
	}
	var photo, video *mediaItem
	clip := filepath.Join(t.TempDir(), "clip.webm")
	_ = os.WriteFile(clip, []byte("webm bytes"), 0o600)
	if err := c.onLoop(func() error {
		raw, err := encodePNG(samplePhoto("gen:usb", 640, 480))
		if err == nil {
			photo, err = c.media.add(raw, "usb", time.Now())
		}
		if err == nil {
			video, err = c.media.addVideo(clip, "clip", "webm", time.Now())
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := func(what string, th usbshare.Thumb, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(th.JPEG))
		if err != nil || cfg.Width != thumbSide || cfg.Height != thumbSide || th.W != thumbSide || th.H != thumbSide {
			t.Errorf("%s: a %dx%d JPEG said to be %dx%d, %v", what, cfg.Width, cfg.Height, th.W, th.H, err)
		}
	}
	th, err := thumbs.Thumb("photos", mediaFileName(photo))
	want("a photo", th, err)
	if _, err := thumbs.Thumb("videos", mediaFileName(video)); err == nil {
		t.Error("a video's thumbnail before its still")
	}
	still, _ := encodePNG(scaleCrop(samplePhoto("gen:still", 320, 180), thumbSide, thumbSide))
	if err := os.WriteFile(c.media.thumb(video.ID), still, 0o600); err != nil {
		t.Fatal(err)
	}
	th, err = thumbs.Thumb("videos", mediaFileName(video))
	want("a video with its still", th, err)
	os.Remove(c.media.thumb(photo.ID))
	th, err = thumbs.Thumb("photos", mediaFileName(photo))
	want("a photo without its thumbnail", th, err)
	for _, p := range [][2]string{{"photos", "none.png"}, {"photos", mediaFileName(video)}, {"videos", mediaFileName(photo)}, {"files", "x.txt"}, {"music", "a.flac"}} {
		if _, err := thumbs.Thumb(p[0], p[1]); err == nil {
			t.Errorf("a thumbnail of %s/%s", p[0], p[1])
		}
	}
}

// The helper keeps the Shell's GODEBUG and its GOCOVERDIR, where a
// coverage build writes its counters (make coverage); neither when unset.
func TestUSBHelperGetsTheShellsGoVariables(t *testing.T) {
	t.Setenv("GODEBUG", "disablethp=1")
	t.Setenv("GOCOVERDIR", "/build/coverage/go/e2e")
	if got := strings.Join(usbHelperShellEnv(), " "); got != "GODEBUG=disablethp=1 GOCOVERDIR=/build/coverage/go/e2e" {
		t.Errorf("the helper gets %q", got)
	}
	t.Setenv("GOCOVERDIR", "")
	if got := strings.Join(usbHelperShellEnv(), " "); got != "GODEBUG=disablethp=1" {
		t.Errorf("without GOCOVERDIR the helper gets %q", got)
	}
}

// MTP has no password, so under a PIN lock the computer is asked about
// only once the phone is unlocked; network sharing asks as before (its
// password is on the phone's screen).
func TestFileTransferWaitsForTheUnlock(t *testing.T) {
	c, _ := lockCore(t)
	c.usb.sim = true
	if err := c.setUSBMode(1); err != nil || c.usbMode() != ui.USBModeMTP {
		t.Fatalf("usb_mode 1: %q, %v", c.usbMode(), err)
	}
	c.lock.pin = true
	c.lockNow("test")
	c.usb.simOn = true
	c.checkUSB()
	c.checkUSB()
	if c.usb.prompt != nil || !c.usb.waitUnlock {
		t.Fatalf("locked: prompt %v, waiting %v", c.usb.prompt != nil, c.usb.waitUnlock)
	}
	c.unlockNow("test")
	c.checkUSB()
	if c.usb.prompt == nil || c.usb.waitUnlock {
		t.Fatalf("unlocked: prompt %v, waiting %v", c.usb.prompt != nil, c.usb.waitUnlock)
	}
	c.usb.simOn = false
	c.checkUSB()
	if c.usb.prompt != nil {
		t.Fatal("the question outlived the cable")
	}

	if err := c.setUSBMode(0); err != nil || c.usbMode() != ui.USBModeNetwork {
		t.Fatalf("usb_mode 0: %q, %v", c.usbMode(), err)
	}
	c.lockNow("test")
	c.usb.simOn = true
	c.checkUSB()
	if c.usb.prompt == nil {
		t.Fatal("network sharing did not ask under the lock")
	}
	if err := c.setUSBMode(2); err == nil {
		t.Error("usb_mode 2 taken")
	}
}

// A bus reset (libmtp resets a phone it takes for Android when it lets
// go) passes through default and addressed: sharing stays while the
// computer enumerates the phone again, but not past the wait, and pulling
// the cable meanwhile still ends it.
func TestBusResetKeepsSharing(t *testing.T) {
	c, _ := lockCore(t)
	c.usb.trusted, c.usb.state = true, "configured"
	for _, st := range []string{"default", "addressed", "configured"} {
		if !c.usbReset(st) {
			t.Fatalf("%s during a reset was not held", st)
		}
	}
	if !c.usb.resetBy.IsZero() || c.usb.state != "configured" {
		t.Errorf("after the reset: %q, until %v", c.usb.state, c.usb.resetBy)
	}
	c.usbReset("default")
	if c.usbReset("not attached") {
		t.Error("the cable pulled during a reset was held")
	}
	c.usbReset("default")
	c.usb.resetBy = time.Now().Add(-time.Second)
	if c.usbReset("default") {
		t.Error("a reset held past its wait")
	}
	c.usb.trusted = false
	if c.usbReset("default") {
		t.Error("held without a trusted computer")
	}
	c.usb.resetBy = time.Time{}
	c.usb.prompt = &confirmOverlay{text: "Trust this computer?"}
	c.pushOverlay(c.usb.prompt)
	for _, st := range []string{"default", "addressed", "configured"} {
		if !c.usbReset(st) {
			t.Fatalf("%s while the user reads whether to trust the computer was not held", st)
		}
	}
	if len(c.overlays) == 0 || c.overlays[len(c.overlays)-1] != c.usb.prompt {
		t.Error("the question whether to trust the computer went during a bus reset")
	}
}
