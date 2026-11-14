package shell

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/hifi"
	"github.com/kario-os/kario/audio/musicfile"
	"github.com/kario-os/kario/audio/tags"
	"github.com/kario-os/kario/ui"
)

// The music library: the files under media/music, with their folders (an
// album copied from a computer stays an album folder). Tracks come from
// the files' headers and tags (audio/musicfile): cue sheets and SACD
// images become their tracks, and the files a cue sheet plays are not
// listed again on their own. Apps see "music:<path in the library>" and,
// for a track of a cue sheet or an SACD image, "#<n>"; never a file path.
//
// The index lives in memory, refreshed by size and modification time on
// every listing, so files added over USB (or any other way) show up and
// removed ones go.

type musicLib struct {
	dir   string
	mu    sync.Mutex // the index; saving files
	infos map[string]probed
	files map[string]*musicEntry
	list  []musicTrack
}

type stamp struct {
	size int64
	mod  time.Time
}

type probed struct {
	stamp
	info musicfile.Info
	err  error
}

// musicEntry is one file's tracks, kept while it and (for a cue sheet)
// its folder do not change.
type musicEntry struct {
	stamp
	dirMod time.Time
	tracks []musicfile.Track
	err    error
}

// musicTrack is a library track: what media.list gives (ui.MediaItem) and
// where it is.
type musicTrack struct {
	ui.MediaItem
	path  string
	index int
	mt    musicfile.Track
}

var (
	musicLibsMu sync.Mutex
	musicLibs   = map[*Core]*musicLib{}
)

// music is the Core's library.
func (c *Core) music() *musicLib {
	musicLibsMu.Lock()
	defer musicLibsMu.Unlock()
	l := musicLibs[c]
	if l == nil {
		l = &musicLib{dir: c.musicDir(), infos: map[string]probed{}, files: map[string]*musicEntry{}}
		musicLibs[c] = l
	}
	return l
}

const maxMusicDepth = 8

func stampOf(fi fs.FileInfo) stamp { return stamp{fi.Size(), fi.ModTime()} }

// probe reads a file's info, from the cache while the file is unchanged.
func (l *musicLib) probe(p string) (musicfile.Info, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return musicfile.Info{}, err
	}
	if c, ok := l.infos[p]; ok && c.stamp == stampOf(fi) {
		return c.info, c.err
	}
	i, err := musicfile.Probe(p)
	if c := i.Tags.Cover; c != nil {
		// The index keeps that there is a cover, not the image.
		i.Tags.Cover = &tags.Cover{MIME: c.MIME, Offset: c.Offset, Size: c.Size}
	}
	l.infos[p] = probed{stampOf(fi), i, err}
	return i, err
}

// scan refreshes the index.
func (l *musicLib) scan() []musicTrack {
	l.mu.Lock()
	defer l.mu.Unlock()
	var files []string
	_ = filepath.WalkDir(l.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(l.dir, p)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) >= maxMusicDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && musicfile.IsMusic(d.Name()) {
			files = append(files, p)
		}
		return nil
	})
	keep := map[string]bool{}
	hidden := map[string]bool{}
	for _, p := range files {
		keep[p] = true
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		var dirMod time.Time
		if musicfile.IsSheet(p) {
			if di, err := os.Stat(filepath.Dir(p)); err == nil {
				dirMod = di.ModTime()
			}
		}
		e := l.files[p]
		if e == nil || e.stamp != stampOf(fi) || !e.dirMod.Equal(dirMod) || musicfile.IsSheet(p) && depsChanged(e, l) {
			ts, err := musicfile.Expand(p, l.probe)
			e = &musicEntry{stamp: stampOf(fi), dirMod: dirMod, tracks: ts, err: err}
			l.files[p] = e
		}
		for _, t := range e.tracks {
			for _, u := range t.Uses {
				hidden[u] = true
			}
		}
	}
	for p := range l.files {
		if !keep[p] {
			delete(l.files, p)
		}
	}
	for p := range l.infos {
		if _, err := os.Stat(p); err != nil {
			delete(l.infos, p)
		}
	}
	var out []musicTrack
	for _, p := range files {
		e := l.files[p]
		if e == nil || e.err != nil || hidden[p] {
			continue
		}
		for _, t := range e.tracks {
			out = append(out, l.item(p, t))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Folder != b.Folder:
			return a.Folder < b.Folder
		case a.Album != b.Album:
			return a.Album < b.Album
		case a.Disc != b.Disc:
			return a.Disc < b.Disc
		case a.Number != b.Number:
			return a.Number < b.Number
		case a.path != b.path:
			return a.path < b.path
		}
		return a.index < b.index
	})
	l.list = out
	return out
}

// depsChanged: a file a cue sheet plays changed since it was read.
func depsChanged(e *musicEntry, l *musicLib) bool {
	for _, t := range e.tracks {
		for _, u := range t.Uses {
			fi, err := os.Stat(u)
			if c, ok := l.infos[u]; err != nil || !ok || c.stamp != stampOf(fi) {
				return true
			}
		}
	}
	return false
}

var lossless = map[string]bool{musicfile.FLAC: true, musicfile.PCM: true, musicfile.ALAC: true, musicfile.APE: true,
	musicfile.WavPack: true, musicfile.DSD: true, musicfile.DST: true}

func (l *musicLib) item(p string, t musicfile.Track) musicTrack {
	rel, _ := filepath.Rel(l.dir, p)
	rel = filepath.ToSlash(rel)
	id := "music:" + rel
	if t.Index > 0 {
		id += "#" + strconv.Itoa(t.Index)
	}
	folder := path.Dir(rel)
	if folder == "." {
		folder = ""
	}
	m := musicTrack{
		MediaItem: ui.MediaItem{ID: id, Kind: ui.MediaAudio, Title: t.Title, Artist: t.Artist, LenMS: int(t.LenMS()),
			Album: t.Album, AlbumArtist: t.AlbumArtist, Number: t.Number, Disc: t.Disc, Genre: t.Genre, Year: t.Date,
			Folder: folder, Format: t.Info.Label(), Quality: t.Info.Quality(), Codec: t.Info.Codec,
			Rate: t.Info.Rate, Bits: t.Info.Bits, Channels: t.Info.Channels, Lossless: lossless[t.Info.Codec],
			ReplayGain: t.HasTrackGain || t.HasAlbumGain, Cover: t.Cover},
		path: p, index: t.Index, mt: t,
	}
	if fi, err := os.Stat(p); err == nil {
		m.Size, m.At = fi.Size(), fi.ModTime().Unix()
	}
	switch {
	case musicfile.IsSheet(p):
		m.From = "cue"
	case musicfile.IsImage(p):
		m.From = "sacd"
	}
	if m.AlbumArtist == "" {
		m.AlbumArtist = m.Artist
	}
	if m.Album == "" {
		// Loose tracks: the bucket a player shows as an album of its own.
		m.Album = tr.T("Music on this phone")
	}
	return m
}

// find looks a track up by its media ID, scanning again if it is not in
// the last listing (a file added since).
func (l *musicLib) find(id string) (musicTrack, bool) {
	if !strings.HasPrefix(id, "music:") {
		return musicTrack{}, false
	}
	l.mu.Lock()
	list := l.list
	l.mu.Unlock()
	for pass := 0; pass < 2; pass++ {
		for _, t := range list {
			if t.ID == id {
				return t, true
			}
		}
		list = l.scan()
	}
	return musicTrack{}, false
}

// ReplayGain modes of audio.play.
const (
	gainTrack = "track"
	gainAlbum = "album"
)

// spec is what audiod plays for a track, with the ReplayGain the mode
// picks (falling back to the other kind when the tags lack it).
func (t musicTrack) spec(mode string) hifi.Spec {
	s := hifi.Spec{Path: t.path, Track: t.index, Media: t.ID, Title: t.Title, Label: t.Format}
	mt := t.mt
	track := func() bool {
		if mt.HasTrackGain {
			s.GainDB, s.Peak = mt.TrackGain, mt.TrackPeak
		}
		return mt.HasTrackGain
	}
	album := func() bool {
		if mt.HasAlbumGain {
			s.GainDB, s.Peak = mt.AlbumGain, mt.AlbumPeak
		}
		return mt.HasAlbumGain
	}
	switch mode {
	case gainTrack:
		_ = track() || album()
	case gainAlbum:
		_ = album() || track()
	}
	if math.IsNaN(s.GainDB) {
		s.GainDB = 0
	}
	return s
}

// musicPlay is what the shell asks audiod for a music: media ID.
func (c *Core) musicPlay(app string, a ui.AudioPlayArgs) (map[string]any, string, error) {
	t, ok := c.music().find(a.Media)
	if !ok {
		return nil, "", fmt.Errorf("no media %q", a.Media)
	}
	req := map[string]any{"app": app, "name": t.Title, "category": audio.Media, "track": t.spec(a.ReplayGain), "start_ms": a.AtMS, "dsd": a.DSD}
	if n, ok := c.music().find(a.Next); ok {
		req["next"] = n.spec(a.ReplayGain)
	}
	return req, t.Title, nil
}

// musicControl turns a music op into audiod's control request.
func (c *Core) musicControl(a ui.AudioControlArgs) (map[string]any, error) {
	req := map[string]any{"id": a.ID, "op": a.Op}
	switch a.Op {
	case ui.OpSeek:
		req["at_ms"] = max(0, a.AtMS)
	case ui.OpQueue:
		req["next"] = nil
		if a.Next != "" {
			n, ok := c.music().find(a.Next)
			if !ok {
				return nil, fmt.Errorf("no media %q", a.Next)
			}
			req["next"] = n.spec(a.ReplayGain)
		}
	case ui.OpOptions:
		req["dsd"] = a.DSD
	}
	return req, nil
}

// The computer's side (USB sharing, shell/usb.go): the library's folders
// as they are on the phone. What it may add: music in any format played
// here, cue sheets, SACD images, and what albums come with (cover
// pictures, lyrics, rip logs).

var musicCompanions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".lrc": true, ".txt": true, ".log": true, ".m3u": true, ".m3u8": true}

// Largest file the computer may add to Music: a dual-layer SACD image.
const maxMusicFile = 9 << 30

func errMusicName() error { return errors.New(tr.T("Not a name for a file or folder in Music")) }

// rel checks a path inside the library from the computer: names without
// leading dots, control characters or "..", at most maxMusicDepth deep.
func (l *musicLib) rel(name string) (string, error) {
	name = strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/")
	parts := strings.Split(name, "/")
	if name == "" || len(parts) > maxMusicDepth+1 {
		return "", errMusicName()
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") || len(p) > 255 || !utf8.ValidString(p) || strings.TrimSpace(p) == "" {
			return "", errMusicName()
		}
		for _, r := range p {
			if r < 0x20 || r == 0x7f {
				return "", errMusicName()
			}
		}
	}
	return filepath.Join(append([]string{l.dir}, parts...)...), nil
}

// inside: p is in the library and no link leads out of it.
func (l *musicLib) inside(p string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(l.dir)
	return err == nil && (real == root || strings.HasPrefix(real, root+string(filepath.Separator)))
}

// usbList lists a folder of the library ("" the top).
func (l *musicLib) usbList(dir string) ([]musicFileItem, bool) {
	p := l.dir
	if dir != "" {
		var err error
		if p, err = l.rel(dir); err != nil {
			return nil, false
		}
	}
	_ = os.MkdirAll(l.dir, 0o700)
	if fi, err := os.Lstat(p); err != nil || !fi.IsDir() || !l.inside(p) {
		return nil, false
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, false
	}
	var out []musicFileItem
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, musicFileItem{name: e.Name(), dir: e.IsDir(), size: fi.Size(), mod: fi.ModTime()})
	}
	return out, true
}

type musicFileItem struct {
	name string
	dir  bool
	size int64
	mod  time.Time
}

// usbOpen opens a file of the library for the computer.
func (l *musicLib) usbOpen(name string) (*os.File, fs.FileInfo, error) {
	p, err := l.rel(name)
	if err != nil {
		return nil, nil, os.ErrNotExist
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || !l.inside(p) {
		return nil, nil, os.ErrNotExist
	}
	f, err := os.Open(p)
	return f, fi, err
}

func (l *musicLib) usbMakeDir(dir string) error {
	p, err := l.rel(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return err
	}
	if !l.inside(p) {
		return errMusicName()
	}
	return nil
}

// usbSave copies a file from the computer into the library, under a free
// name in its folder (made as needed). The copy runs outside the shell's
// loop (an album image is hundreds of MB); the index sees it at the next
// listing.
func (l *musicLib) usbSave(name string, r io.Reader) (string, error) {
	p, err := l.rel(name)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(p))
	if !musicfile.IsMusic(p) && !musicCompanions[ext] {
		return "", errMusicKind()
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if !l.inside(dir) {
		return "", errMusicName()
	}
	tmp, err := os.CreateTemp(dir, ".copy-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, io.LimitReader(r, maxMusicFile))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	got := p
	stem := strings.TrimSuffix(p, filepath.Ext(p))
	for i := 2; ; i++ {
		if _, err := os.Lstat(got); errors.Is(err, os.ErrNotExist) {
			break
		}
		got = fmt.Sprintf("%s (%d)%s", stem, i, filepath.Ext(p))
	}
	if err := os.Rename(tmp.Name(), got); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	rel, _ := filepath.Rel(l.dir, got)
	return filepath.ToSlash(rel), nil
}

func errMusicKind() error {
	return errors.New(tr.T("Music takes music files (FLAC, WAV, AIFF, ALAC, APE, WavPack, MP3, Ogg, DSF, DFF), cue sheets, SACD images (.iso) and cover pictures"))
}
