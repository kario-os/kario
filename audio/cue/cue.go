// Package cue reads cue sheets: an album ripped as one big file (or a few)
// plus a .cue that says where each track starts. Text may be UTF-8 (with
// or without a byte order mark), UTF-16 or GBK. Times are CD frames, 1/75
// of a second, as the sheet writes them.
//
// Track boundaries follow the usual "gaps appended to the previous track"
// rule: a track runs from its INDEX 01 to the next track's INDEX 01, so
// the pregap (INDEX 00 to 01) plays at the end of the track before it and
// consecutive tracks join without a gap or an overlap, also across files.
package cue

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kario-os/kario/audio/internal/textenc"
	"github.com/kario-os/kario/audio/tags"
)

// FramesPerSecond: cue sheet times are mm:ss:ff with 75 frames a second.
const FramesPerSecond = 75

// Sheet is a parsed cue sheet.
type Sheet struct {
	Title, Performer, Songwriter string
	Genre, Date, Catalog         string
	Disc, DiscTotal              int
	AlbumGain, AlbumPeak         float64
	HasAlbumGain                 bool
	Files                        []string // in the order of the sheet
	Tracks                       []Track
}

// Track is one TRACK ... AUDIO entry.
type Track struct {
	Number                       int
	Title, Performer, Songwriter string
	ISRC                         string
	TrackGain, TrackPeak         float64
	HasTrackGain                 bool
	// File and Start: where INDEX 01 is. Pregap is INDEX 00 (in PregapFile),
	// -1 without one.
	File       string
	Start      int64
	PregapFile string
	Pregap     int64
	// Segments is what plays, in order (see the package comment).
	Segments []Segment
}

// Segment is a stretch of one file: [Start, End) in CD frames, End -1
// for "to the end of the file".
type Segment struct {
	File       string
	Start, End int64
}

// Frames is the length in CD frames, -1 when a segment runs to the end of
// its file (the file's length decides).
func (t Track) Frames() int64 {
	var n int64
	for _, s := range t.Segments {
		if s.End < 0 {
			return -1
		}
		n += s.End - s.Start
	}
	return n
}

// Parse reads a cue sheet.
func Parse(raw []byte) (*Sheet, error) {
	s := &Sheet{}
	var cur *Track
	file := ""
	for n, line := range strings.Split(textenc.Decode(raw), "\n") {
		f := fields(strings.TrimSpace(strings.TrimSuffix(line, "\r")))
		if len(f) == 0 {
			continue
		}
		arg := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		switch strings.ToUpper(f[0]) {
		case "FILE":
			file = arg(1)
			if file == "" {
				return nil, fmt.Errorf("cue line %d: FILE without a name", n+1)
			}
			s.Files = append(s.Files, file)
		case "TRACK":
			num, err := strconv.Atoi(arg(1))
			if err != nil {
				return nil, fmt.Errorf("cue line %d: bad TRACK", n+1)
			}
			if !strings.EqualFold(arg(2), "AUDIO") {
				cur = nil
				continue
			}
			s.Tracks = append(s.Tracks, Track{Number: num, File: file, Start: -1, Pregap: -1})
			cur = &s.Tracks[len(s.Tracks)-1]
		case "INDEX":
			if cur == nil {
				continue
			}
			idx, err := strconv.Atoi(arg(1))
			at, err2 := parseTime(arg(2))
			if err != nil || err2 != nil {
				return nil, fmt.Errorf("cue line %d: bad INDEX", n+1)
			}
			switch {
			case idx == 0:
				cur.PregapFile, cur.Pregap = file, at
			case idx == 1:
				cur.File, cur.Start = file, at
			}
		case "TITLE":
			if cur != nil {
				cur.Title = arg(1)
			} else {
				s.Title = arg(1)
			}
		case "PERFORMER":
			if cur != nil {
				cur.Performer = arg(1)
			} else {
				s.Performer = arg(1)
			}
		case "SONGWRITER":
			if cur != nil {
				cur.Songwriter = arg(1)
			} else {
				s.Songwriter = arg(1)
			}
		case "ISRC":
			if cur != nil {
				cur.ISRC = arg(1)
			}
		case "CATALOG":
			s.Catalog = arg(1)
		case "REM":
			key, val := strings.ToUpper(arg(1)), strings.Join(f[min(2, len(f)):], " ")
			switch key {
			case "GENRE":
				s.Genre = val
			case "DATE":
				s.Date = val
			case "DISCNUMBER":
				s.Disc, _ = strconv.Atoi(val)
			case "TOTALDISCS":
				s.DiscTotal, _ = strconv.Atoi(val)
			case "REPLAYGAIN_ALBUM_GAIN":
				s.AlbumGain, s.HasAlbumGain = tags.ParseGain(val)
			case "REPLAYGAIN_ALBUM_PEAK":
				s.AlbumPeak, _ = strconv.ParseFloat(val, 64)
			case "REPLAYGAIN_TRACK_GAIN":
				if cur != nil {
					cur.TrackGain, cur.HasTrackGain = tags.ParseGain(val)
				}
			case "REPLAYGAIN_TRACK_PEAK":
				if cur != nil {
					cur.TrackPeak, _ = strconv.ParseFloat(val, 64)
				}
			}
		}
	}
	out := s.Tracks[:0]
	for _, t := range s.Tracks {
		if t.Start >= 0 {
			out = append(out, t)
		}
	}
	s.Tracks = out
	if len(s.Tracks) == 0 {
		return nil, errors.New("cue sheet without audio tracks")
	}
	s.segments()
	return s, nil
}

func (s *Sheet) fileIndex(name string) int {
	for i, f := range s.Files {
		if f == name {
			return i
		}
	}
	return -1
}

func (s *Sheet) segments() {
	for i := range s.Tracks {
		t := &s.Tracks[i]
		fi := s.fileIndex(t.File)
		if i+1 == len(s.Tracks) {
			t.Segments = []Segment{{t.File, t.Start, -1}}
			continue
		}
		next := s.Tracks[i+1]
		ni := s.fileIndex(next.File)
		if ni == fi {
			t.Segments = []Segment{{t.File, t.Start, max(t.Start, next.Start)}}
			continue
		}
		t.Segments = []Segment{{t.File, t.Start, -1}}
		for k := fi + 1; k < ni && k > 0; k++ {
			t.Segments = append(t.Segments, Segment{s.Files[k], 0, -1})
		}
		if next.Start > 0 && ni >= 0 {
			t.Segments = append(t.Segments, Segment{next.File, 0, next.Start})
		}
	}
}

// parseTime reads mm:ss:ff (minutes may exceed 99).
func parseTime(v string) (int64, error) {
	p := strings.Split(v, ":")
	if len(p) != 3 {
		return 0, fmt.Errorf("bad time %q", v)
	}
	var n [3]int64
	for i := range p {
		x, err := strconv.ParseInt(p[i], 10, 64)
		if err != nil || x < 0 {
			return 0, fmt.Errorf("bad time %q", v)
		}
		n[i] = x
	}
	if n[1] >= 60 || n[2] >= FramesPerSecond {
		return 0, fmt.Errorf("bad time %q", v)
	}
	return (n[0]*60+n[1])*FramesPerSecond + n[2], nil
}

// fields splits a line into words, a quoted string being one word.
func fields(line string) []string {
	var out []string
	for line != "" {
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			break
		}
		if line[0] == '"' {
			end := strings.IndexByte(line[1:], '"')
			if end < 0 {
				out = append(out, line[1:])
				break
			}
			out = append(out, line[1:1+end])
			line = line[2+end:]
			continue
		}
		end := strings.IndexAny(line, " \t")
		if end < 0 {
			out = append(out, line)
			break
		}
		out = append(out, line[:end])
		line = line[end:]
	}
	return out
}
