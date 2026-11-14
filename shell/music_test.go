package shell

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func musicWAV(t *testing.T, path string, frames int) {
	t.Helper()
	le := binary.LittleEndian
	b := make([]byte, 44+2*frames)
	copy(b, "RIFF")
	le.PutUint32(b[4:], uint32(36+2*frames))
	copy(b[8:], "WAVEfmt ")
	le.PutUint32(b[16:], 16)
	le.PutUint16(b[20:], 1)
	le.PutUint16(b[22:], 1)
	le.PutUint32(b[24:], 44100)
	le.PutUint32(b[28:], 88200)
	le.PutUint16(b[32:], 2)
	le.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	le.PutUint32(b[40:], uint32(2*frames))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMusicLibrary(t *testing.T) {
	dir := t.TempDir()
	l := &musicLib{dir: dir, infos: map[string]probed{}, files: map[string]*musicEntry{}}
	musicWAV(t, filepath.Join(dir, "loose.wav"), 44100)
	musicWAV(t, filepath.Join(dir, "Band", "Album", "image.wav"), 3*44100)
	os.WriteFile(filepath.Join(dir, "Band", "Album", "image.cue"), []byte("REM REPLAYGAIN_ALBUM_GAIN -4.00 dB\nPERFORMER \"Band\"\nTITLE \"Album\"\nFILE \"image.wav\" WAVE\nTRACK 01 AUDIO\nTITLE \"First\"\nREM REPLAYGAIN_TRACK_GAIN -2.00 dB\nINDEX 01 00:00:00\nTRACK 02 AUDIO\nTITLE \"Second\"\nINDEX 01 00:01:00\n"), 0o600)
	musicWAV(t, filepath.Join(dir, ".hidden", "x.wav"), 100)
	os.Symlink("/etc", filepath.Join(dir, "link"))
	list := l.scan()
	var ids []string
	for _, tr := range list {
		ids = append(ids, tr.ID)
	}
	want := []string{"music:loose.wav", "music:Band/Album/image.cue#1", "music:Band/Album/image.cue#2"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("tracks %v, want %v (the image is not listed on its own; hidden folders and links are skipped)", ids, want)
	}
	first := list[1]
	if first.Title != "First" || first.Album != "Album" || first.Folder != "Band/Album" || first.Format != "WAV 16/44.1" || first.From != "cue" || first.LenMS != 1000 || !first.Lossless {
		t.Fatalf("%+v", first)
	}
	if list[0].Album == "" {
		t.Fatal("a loose track gets the library's own album name")
	}
	if s := first.spec(gainTrack); s.GainDB != -2 || s.Track != 1 || s.Media != first.ID {
		t.Fatalf("track gain: %+v", s)
	}
	if s := list[2].spec(gainTrack); s.GainDB != -4 {
		t.Fatalf("no track gain falls back to the album's: %+v", s)
	}
	if s := first.spec(""); s.GainDB != 0 {
		t.Fatalf("ReplayGain off: %+v", s)
	}
	if _, ok := l.find("music:Band/Album/image.wav"); ok {
		t.Fatal("the image is not a track of its own")
	}
	musicWAV(t, filepath.Join(dir, "new.wav"), 100)
	if _, ok := l.find("music:new.wav"); !ok {
		t.Fatal("a file added since the last listing is found")
	}
}

func TestMusicFromTheComputer(t *testing.T) {
	dir := t.TempDir()
	l := &musicLib{dir: dir, infos: map[string]probed{}, files: map[string]*musicEntry{}}
	got, err := l.usbSave("Album/01.flac", strings.NewReader("fLaC"))
	if err != nil || got != "Album/01.flac" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := l.usbSave("Album/01.flac", strings.NewReader("fLaC")); got != "Album/01 (2).flac" {
		t.Fatalf("a taken name: %q", got)
	}
	if _, err := l.usbSave("Album/cover.jpg", strings.NewReader("jpeg")); err != nil {
		t.Fatalf("a cover picture: %v", err)
	}
	for _, bad := range []string{"../x.flac", "Album/../../x.flac", ".hidden/x.flac", "a/\x01.flac", "x.exe", strings.Repeat("d/", 10) + "x.flac"} {
		if _, err := l.usbSave(bad, strings.NewReader("x")); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	ents, ok := l.usbList("")
	if !ok || len(ents) != 1 || ents[0].name != "Album" || !ents[0].dir {
		t.Fatalf("top: %+v", ents)
	}
	ents, ok = l.usbList("Album")
	if !ok || len(ents) != 3 {
		t.Fatalf("album: %+v", ents)
	}
	if _, _, err := l.usbOpen("Album/01.flac"); err != nil {
		t.Fatal(err)
	}
	os.Symlink("/etc/passwd", filepath.Join(dir, "Album", "p.flac"))
	if _, _, err := l.usbOpen("Album/p.flac"); err == nil {
		t.Fatal("a link out of the library opened")
	}
	if err := l.usbMakeDir("Live/2024"); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "Album", ".copy-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}
