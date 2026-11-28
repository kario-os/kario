package audiod

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kario-os/kario/subproc"
)

// usbCard is a USB audio device ALSA has a card for and that plays.
type usbCard struct {
	num  string // the card's number
	id   string // vendor:product (usbid)
	name string // the card's long name
	mic  bool   // it also captures (a headset)
}

// cardLine is a card's line in /proc/asound/cards:
// " 1 [KA3            ]: USB-Audio - FiiO KA3".
var cardLine = regexp.MustCompile(`(?m)^\s*(\d+) \[[^\]]*\]: \S+ - (.*)$`)

// scanUSB lists the USB audio devices that can play: ALSA's cards with a
// usbid and a playback interface in stream0 (snd-usb-audio's), as the
// kernel shows them under asound (/proc/asound). The kernel adds and
// removes them as the device comes and goes; nothing else (udev) needs
// to run.
func scanUSB(asound string) []usbCard {
	names := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(asound, "cards")); err == nil {
		for _, m := range cardLine.FindAllStringSubmatch(string(raw), -1) {
			names[m[1]] = strings.TrimSpace(m[2])
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(asound, "card[0-9]*"))
	var out []usbCard
	for _, d := range dirs {
		num := strings.TrimPrefix(filepath.Base(d), "card")
		id, err := os.ReadFile(filepath.Join(d, "usbid"))
		if err != nil {
			continue
		}
		stream, err := os.ReadFile(filepath.Join(d, "stream0"))
		if err != nil || !strings.Contains(string(stream), "Playback:") {
			continue
		}
		out = append(out, usbCard{num: num, id: strings.TrimSpace(string(id)), name: cmp.Or(names[num], "USB audio"),
			mic: strings.Contains(string(stream), "Capture:")})
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].num) < len(out[j].num) || out[i].num < out[j].num })
	return out
}

// hwVolume sets a device's own volume control: media's volume on a USB
// device that has one is absolute, the samples go out untouched.
type hwVolume interface {
	// Control is the card's playback volume control, "" when it has none.
	Control(card string) string
	Set(card, control string, level float64) error
}

// amixer is alsa-utils' amixer on ALSA's simple mixer controls.
type amixer struct{ path string }

func (a amixer) run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := subproc.CombinedOutput(subproc.CommandContext(ctx, cmp.Or(a.path, "amixer"), args...))
	if err != nil {
		return nil, fmt.Errorf("amixer %q: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (a amixer) Control(card string) string {
	out, err := a.run("-c", card, "scontents")
	if err != nil {
		return ""
	}
	return playbackControl(string(out))
}

// Set sets the control on amixer's mapped scale (-M), the one alsamixer
// shows: even steps to the ear.
func (a amixer) Set(card, control string, level float64) error {
	_, err := a.run("-q", "-c", card, "-M", "sset", control, fmt.Sprintf("%d%%", int(level*100+0.5)))
	return err
}

var scontrol = regexp.MustCompile(`^Simple mixer control '(.*)',(\d+)$`)

// playbackControl picks from amixer's scontents the control that sets the
// playback volume: one of the usual names, else the first that has a
// playback volume.
func playbackControl(scontents string) string {
	var withVolume []string
	name := ""
	for _, l := range strings.Split(scontents, "\n") {
		if m := scontrol.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			name = m[1]
			continue
		}
		caps, ok := strings.CutPrefix(strings.TrimSpace(l), "Capabilities:")
		if ok && name != "" && (strings.Contains(" "+caps+" ", " pvolume ") || strings.Contains(" "+caps+" ", " volume ")) {
			withVolume = append(withVolume, name)
		}
	}
	for _, want := range []string{"PCM", "Master", "Headphone", "Speaker"} {
		for _, n := range withVolume {
			if n == want {
				return n
			}
		}
	}
	if len(withVolume) > 0 {
		return withVolume[0]
	}
	return ""
}
