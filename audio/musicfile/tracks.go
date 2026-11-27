package musicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/audio/cue"
	"github.com/kario-os/kario/audio/dsd"
)

// Segment is a stretch of one audio file: frames [Start, End) at that
// file's rate, End -1 for "to the end" (a whole file; a cue sheet's
// segments end where the file says it does).
type Segment struct {
	Path       string
	Start, End int64
}

// Track is one entry of the library: a whole file, a track of a cue sheet
// or a track of an SACD image.
type Track struct {
	Path  string // the file the entry comes from (the .cue or .iso for those)
	Index int    // 0 for a whole file; the track's place (from 1) in a cue sheet or SACD area
	Info  Info   // the audio's format: the (first) file's, an SACD area's
	// Segments is what plays (cue sheets; one per file the track spans).
	Segments []Segment
	Frames   int64 // exact length at Info.Rate, 0 if unknown

	Title, Artist, Album, AlbumArtist string
	Genre, Date                       string
	Number, Disc                      int
	TrackGain, TrackPeak              float64
	AlbumGain, AlbumPeak              float64
	HasTrackGain, HasAlbumGain        bool
	Cover                             bool

	// Uses lists the audio files a cue sheet plays: the library does not
	// list them again on their own.
	Uses []string
	// Area of an SACD: "stereo", or "multichannel" (mixed down to two
	// channels) on a disc without a stereo area.
	Area string
}

// SACD areas.
const (
	AreaStereo = "stereo"
	AreaMulti  = "multichannel"
)

// LenMS is the track's length in milliseconds.
func (t Track) LenMS() int64 {
	if t.Info.Rate == 0 {
		return 0
	}
	return t.Frames * 1000 / int64(t.Info.Rate)
}

// Prober reads a file's Info (the library passes a cached one).
type Prober func(path string) (Info, error)

// Tracks lists what path holds.
func Tracks(path string) ([]Track, error) { return Expand(path, Probe) }

// Expand lists what path holds: a cue sheet's or an SACD image's tracks,
// the tracks of a cue sheet embedded in a file, or the file itself.
func Expand(path string, probe Prober) ([]Track, error) {
	switch {
	case IsSheet(path):
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(raw) > 1<<20 {
			return nil, errors.New("cue sheet too large")
		}
		s, err := cue.Parse(raw)
		if err != nil {
			return nil, err
		}
		return sheetTracks(path, s, probe, func(name string) (string, error) { return resolve(path, name) })
	case IsImage(path):
		return sacdTracks(path)
	}
	i, err := probe(path)
	if err != nil {
		return nil, err
	}
	if i.Tags.CueSheet != "" {
		if s, err := cue.Parse([]byte(i.Tags.CueSheet)); err == nil && len(s.Tracks) > 1 {
			if ts, err := sheetTracks(path, s, probe, func(string) (string, error) { return path, nil }); err == nil {
				for k := range ts {
					ts[k].Uses = nil
					ts[k].Cover = i.Tags.Cover != nil
				}
				return ts, nil
			}
		}
	}
	t := Track{Path: path, Info: i, Frames: i.Frames, Segments: []Segment{{path, 0, -1}}}
	g := i.Tags
	t.Title, t.Artist, t.Album, t.AlbumArtist = g.Title, g.Artist, g.Album, g.AlbumArtist
	t.Genre, t.Date, t.Number, t.Disc = g.Genre, g.Date, g.Track, g.Disc
	t.TrackGain, t.TrackPeak, t.HasTrackGain = g.TrackGain, g.TrackPeak, g.HasTrackGain
	t.AlbumGain, t.AlbumPeak, t.HasAlbumGain = g.AlbumGain, g.AlbumPeak, g.HasAlbumGain
	t.Cover = g.Cover != nil
	if t.Title == "" {
		t.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return []Track{t}, nil
}

func sheetTracks(sheet string, s *cue.Sheet, probe Prober, find func(string) (string, error)) ([]Track, error) {
	files := map[string]string{}
	infos := map[string]Info{}
	for _, name := range s.Files {
		p, err := find(name)
		if err != nil {
			return nil, err
		}
		i, err := probe(p)
		if err != nil {
			return nil, err
		}
		files[name], infos[p] = p, i
	}
	var uses []string
	for _, p := range files {
		if p != sheet {
			uses = append(uses, p)
		}
	}
	var out []Track
	for k, ct := range s.Tracks {
		first := files[ct.Segments[0].File]
		info := infos[first]
		t := Track{Path: sheet, Index: k + 1, Info: info, Number: ct.Number, Uses: uses,
			Title: ct.Title, Artist: ct.Performer, Album: s.Title, AlbumArtist: s.Performer, Genre: s.Genre, Date: s.Date, Disc: s.Disc,
			TrackGain: ct.TrackGain, TrackPeak: ct.TrackPeak, HasTrackGain: ct.HasTrackGain,
			AlbumGain: s.AlbumGain, AlbumPeak: s.AlbumPeak, HasAlbumGain: s.HasAlbumGain}
		g := info.Tags
		if !t.HasAlbumGain && g.HasAlbumGain {
			t.AlbumGain, t.AlbumPeak, t.HasAlbumGain = g.AlbumGain, g.AlbumPeak, true
		}
		if t.Album == "" {
			t.Album = g.Album
		}
		if t.AlbumArtist == "" {
			t.AlbumArtist = g.AlbumArtist
			if t.AlbumArtist == "" {
				t.AlbumArtist = g.Artist
			}
		}
		if t.Artist == "" {
			t.Artist = t.AlbumArtist
		}
		if t.Genre == "" {
			t.Genre = g.Genre
		}
		if t.Date == "" {
			t.Date = g.Date
		}
		t.Cover = g.Cover != nil
		if t.Title == "" {
			t.Title = tr.T("Track %02d", ct.Number)
		}
		for _, seg := range ct.Segments {
			p := files[seg.File]
			fi := infos[p]
			if fi.Rate != info.Rate || fi.Channels != info.Channels || fi.DSD() != info.DSD() {
				return nil, fmt.Errorf("cue track %d spans files of different formats", ct.Number)
			}
			conv := func(cd int64) int64 { return cd * int64(fi.Rate) / cue.FramesPerSecond }
			sg := Segment{Path: p, Start: conv(seg.Start), End: -1}
			if seg.End >= 0 {
				sg.End = conv(seg.End)
			}
			end := sg.End
			if end < 0 {
				end = fi.Frames
			}
			if fi.Frames > 0 && (sg.Start >= fi.Frames || end > fi.Frames) {
				if sg.Start >= fi.Frames {
					return nil, fmt.Errorf("cue track %d starts after the end of %s", ct.Number, filepath.Base(p))
				}
				end = fi.Frames
			}
			t.Frames += max(0, end-sg.Start)
			if sg.End < 0 && end > 0 {
				sg.End = end
			}
			t.Segments = append(t.Segments, sg)
		}
		out = append(out, t)
	}
	return out, nil
}

// resolve finds the file a cue sheet names, beside the sheet: as written,
// in another letter case, with another audio extension (an image
// converted after ripping), or the one audio file named like the sheet.
// Names that leave the sheet's folder are refused.
func resolve(sheet, name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		name = filepath.Base(name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("cue sheet names %q outside its folder", name)
		}
	}
	dir := filepath.Dir(sheet)
	p := filepath.Join(dir, filepath.FromSlash(name))
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return p, nil
	}
	ents, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	base := filepath.Base(p)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	sheetStem := strings.TrimSuffix(filepath.Base(sheet), filepath.Ext(sheet))
	var sameStem, likeSheet string
	for _, e := range ents {
		n := e.Name()
		if !e.Type().IsRegular() || IsSheet(n) || IsImage(n) || !IsMusic(n) {
			continue
		}
		if strings.EqualFold(n, base) {
			return filepath.Join(filepath.Dir(p), n), nil
		}
		s := strings.TrimSuffix(n, filepath.Ext(n))
		if strings.EqualFold(s, stem) {
			sameStem = n
		}
		if strings.EqualFold(s, sheetStem) {
			likeSheet = n
		}
	}
	switch {
	case sameStem != "":
		return filepath.Join(filepath.Dir(p), sameStem), nil
	case likeSheet != "":
		return filepath.Join(filepath.Dir(p), likeSheet), nil
	}
	return "", fmt.Errorf("the cue sheet's %q is not there", name)
}

func sacdTracks(path string) ([]Track, error) {
	s, err := dsd.OpenSACD(path)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	a := s.Stereo()
	area := AreaStereo
	if a.Multichannel {
		area = AreaMulti
	}
	codec := DSD
	if a.DST {
		codec = DST
	}
	info := Info{Container: "SACD", Codec: codec, Rate: a.Rate, Bits: 1, Channels: a.Channels}
	var out []Track
	for k, st := range a.Tracks {
		t := Track{Path: path, Index: k + 1, Info: info, Area: area, Number: st.Number, Title: st.Title, Artist: st.Performer,
			Album: s.Title, AlbumArtist: s.Artist, Frames: st.Frames * int64(a.Rate) / dsd.FramesPerSecond}
		if s.Year > 0 {
			t.Date = fmt.Sprint(s.Year)
		}
		t.Info.Frames = t.Frames
		if t.Title == "" {
			t.Title = tr.T("Track %02d", st.Number)
		}
		if t.Artist == "" {
			t.Artist = s.Artist
		}
		if t.Album == "" {
			t.Album = s.DiscTitle
		}
		out = append(out, t)
	}
	return out, nil
}
