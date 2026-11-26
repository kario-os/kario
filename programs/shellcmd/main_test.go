package shellcmd

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kario-os/kario/shell"
	"github.com/kario-os/kario/ui"
)

// The Shell's own telephonyd drives the phone's modem when the Shell runs a
// phone by itself (the chroot on Nura); scripts and the developer machine
// keep the simulated baseband.
func TestPrivateTelephonyFollowsTheSKU(t *testing.T) {
	phone := shell.SKU{ID: "oneplus-fajita-t9", Modem: "modemmanager"}
	for _, tc := range []struct {
		sku      shell.SKU
		headless bool
		want     string
	}{
		{phone, false, "modemmanager"},
		{phone, true, ""},
		{shell.SKU{Modem: "modemmanager"}, false, ""},
		{shell.SKU{ID: "qemu-t9", Modem: "sim"}, false, "sim"},
		{shell.SKU{}, false, ""},
	} {
		if got := privateModem(tc.sku, tc.headless); got != tc.want {
			t.Errorf("privateModem(%+v, headless %v) = %q, want %q", tc.sku, tc.headless, got, tc.want)
		}
	}
	if got := strings.Join(telephonydArgs("/r", "/s", "modemmanager"), " "); got != "--run-dir /r --state-dir /s --modem modemmanager" {
		t.Errorf("args %q", got)
	}
	if got := strings.Join(telephonydArgs("/r", "/s", ""), " "); got != "--run-dir /r --state-dir /s" {
		t.Errorf("args without a modem %q", got)
	}
}

// The Shell's own audiod plays to the PCM the image's SKU names (the 6T's
// speaker, whose path its UCM profile routes) and sets the card up as the
// whole phone's audiod does, from the same board values (sku.json's audio,
// not a chroot's set-up): for media always, for calls only with the SKU's
// own modem (not the scripts' simulated baseband); without a PCM, or a SKU
// simulated on the developer machine, it finds the card itself. Before,
// it only played to the PCM and the card stayed as something else left it.
func TestPrivateAudioFollowsTheSKU(t *testing.T) {
	audio := shell.SKUAudio{PCM: "plughw:0,2", Card: "hw:0", Verb: "HiFi", Device: "Speaker", CallVerb: "Voice Call",
		Earpiece: "Earpiece", EarpiecePCM: "plughw:0,0", CallSpeaker: "Speaker1", CallSpeakerPCM: "plughw:0,2", CallMic: "Mic"}
	phone := shell.SKU{ID: "oneplus-fajita-t9", Modem: "modemmanager", Audio: audio}
	media := "--sink alsa:plughw:0,2 --ucm-card hw:0 --ucm-verb HiFi --ucm-device Speaker"
	calls := media + " --ucm-call-verb Voice Call --ucm-earpiece Earpiece --ucm-earpiece-pcm plughw:0,0" +
		" --ucm-speaker Speaker1 --ucm-speaker-pcm plughw:0,2 --ucm-mic Mic"
	noUCM := phone
	noUCM.Audio = shell.SKUAudio{PCM: "plughw:0,2"}
	for _, tc := range []struct {
		sku      shell.SKU
		headless bool
		want     string
	}{
		{phone, false, calls},
		{phone, true, media},
		{shell.SKU{ID: phone.ID, Audio: audio}, false, media},
		{noUCM, false, "--sink alsa:plughw:0,2"},
		{shell.SKU{Audio: audio}, false, "--sink auto"},
		{shell.SKU{ID: "qemu-t9"}, false, "--sink auto"},
		{shell.SKU{}, true, "--sink auto"},
	} {
		if got := strings.Join(privateAudio(tc.sku, tc.headless), " "); got != tc.want {
			t.Errorf("privateAudio(%+v, headless %v) = %q, want %q", tc.sku, tc.headless, got, tc.want)
		}
	}
	raw := `{"id": "oneplus-fajita-t9", "device": "bar-t9", "audio": {"pcm": "plughw:0,2", "card": "hw:0", "verb": "HiFi", "device": "Speaker", "call_mic": "Mic"}}`
	path := filepath.Join(t.TempDir(), "sku.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	sku, err := shell.LoadSKUFile(path)
	if err != nil || sku.Audio != (shell.SKUAudio{PCM: "plughw:0,2", Card: "hw:0", Verb: "HiFi", Device: "Speaker", CallMic: "Mic"}) {
		t.Fatalf("%+v %v", sku.Audio, err)
	}
}

// A selftest that compares snapshots seeds what apps draw at random and
// takes the same photo every time; other runs do neither.
func TestComparedRunHoldsStill(t *testing.T) {
	seed := ui.EnvTestSeed + "=" + strconv.Itoa(ui.TestSeed)
	if env := appEnv(t.TempDir(), 1, true); !slices.Contains(env, seed) {
		t.Errorf("compared run's apps get %q", env)
	}
	if env := strings.Join(appEnv(t.TempDir(), 1, false), " "); strings.Contains(env, ui.EnvTestSeed) {
		t.Errorf("other runs' apps get %q", env)
	}
	if got := strings.Join(mediadArgs("/r", true), " "); got != "--run-dir /r --test-codes --test-stills" {
		t.Errorf("compared run's mediad %q", got)
	}
	if got := strings.Join(mediadArgs("/r", false), " "); got != "--run-dir /r --test-codes" {
		t.Errorf("other runs' mediad %q", got)
	}
}

// Apps get the Shell's GOCOVERDIR (a coverage build's, make coverage) and
// nothing in its place without one.
func TestAppsGetTheCoverageDirectory(t *testing.T) {
	t.Setenv("GOCOVERDIR", "/build/coverage/e2e")
	if env := appEnv(t.TempDir(), 1, false); !slices.Contains(env, "GOCOVERDIR=/build/coverage/e2e") {
		t.Errorf("apps get %q", env)
	}
	t.Setenv("GOCOVERDIR", "")
	if env := strings.Join(appEnv(t.TempDir(), 1, false), " "); strings.Contains(env, "GOCOVERDIR") {
		t.Errorf("without one apps get %q", env)
	}
}
