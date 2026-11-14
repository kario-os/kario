package main

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/kario-os/kario/ui"
)

// track is a media.list item for audio, with the system's music library's
// fields (album, format…).
type track struct {
	ui.MediaItem
}

func (t track) artist() string {
	if t.Artist != "" {
		return t.Artist
	}
	return tr.T("Unknown artist")
}

func (t track) albumArtist() string {
	if t.AlbumArtist != "" {
		return t.AlbumArtist
	}
	return t.artist()
}

func (t track) album() string {
	if t.Album != "" {
		return t.Album
	}
	return tr.T("Unknown album")
}

// demo: the synthesised tracks that stand in when the library is empty.
func (t track) demo() bool { return strings.HasPrefix(t.ID, "demo:") }

// album groups tracks by album artist and title.
type album struct {
	key, title, artist, year string
	tracks                   []int // into the library, in disc and track order
}

func albumKey(t track) string { return t.albumArtist() + "\x00" + t.album() }

func albums(lib []track) []album {
	idx := map[string]int{}
	var out []album
	for i, t := range lib {
		if t.demo() {
			continue
		}
		k := albumKey(t)
		j, ok := idx[k]
		if !ok {
			j = len(out)
			idx[k] = j
			out = append(out, album{key: k, title: t.album(), artist: t.albumArtist(), year: t.Year})
		}
		out[j].tracks = append(out[j].tracks, i)
	}
	for _, a := range out {
		sort.SliceStable(a.tracks, func(x, y int) bool {
			p, q := lib[a.tracks[x]], lib[a.tracks[y]]
			if p.Disc != q.Disc {
				return p.Disc < q.Disc
			}
			return p.Number < q.Number
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].artist != out[j].artist {
			return out[i].artist < out[j].artist
		}
		return out[i].title < out[j].title
	})
	return out
}

// quality is an album's (or list's) best format: "SACD", "DSD64",
// "24/96", "16/44.1".
func quality(lib []track, idx []int) string {
	best, score := "", -1
	for _, i := range idx {
		t := lib[i]
		s := t.Rate * max(1, t.Bits)
		q := t.Quality
		switch {
		case t.From == "sacd":
			q, s = "SACD", 1<<40
		case !t.Lossless:
			s = 0
		}
		if s > score {
			best, score = q, s
		}
	}
	return best
}

type artist struct {
	name   string
	albums int
	tracks []int
}

func artists(lib []track, as []album) []artist {
	idx := map[string]int{}
	var out []artist
	for _, a := range as {
		j, ok := idx[a.artist]
		if !ok {
			j = len(out)
			idx[a.artist] = j
			out = append(out, artist{name: a.artist})
		}
		out[j].albums++
		out[j].tracks = append(out[j].tracks, a.tracks...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// folder lists what is directly in dir: its subfolders and its tracks.
func folder(lib []track, dir string) (subs []string, tracks []int) {
	seen := map[string]bool{}
	for i, t := range lib {
		if t.demo() {
			continue
		}
		f := t.Folder
		switch {
		case f == dir:
			tracks = append(tracks, i)
		case dir == "" || strings.HasPrefix(f, dir+"/"):
			rest := strings.TrimPrefix(strings.TrimPrefix(f, dir), "/")
			sub, _, _ := strings.Cut(rest, "/")
			if sub != "" && !seen[sub] {
				seen[sub] = true
				subs = append(subs, path.Join(dir, sub))
			}
		}
	}
	sort.Strings(subs)
	return
}

// outputLabel says how a stream reaches the sound card, from audiod's
// "mode FORMAT RATE Hz CHch [bit-perfect]" (or the mixer's).
func outputLabel(out string, t track) string {
	f := strings.Fields(out)
	if len(f) < 3 || f[0] == "mixer" {
		if out == "" {
			return ""
		}
		return tr.T("Mixed with system sounds at 48 kHz")
	}
	var rate int
	fmt.Sscanf(f[2], "%d", &rate)
	khz := fmt.Sprintf("%g kHz", float64(rate)/1000)
	switch {
	case f[0] == "dsd":
		return tr.T("Native DSD to the sound card")
	case f[0] == "dop":
		return tr.T("DoP (DSD over PCM) to the sound card")
	case strings.HasSuffix(out, "bit-perfect"):
		return tr.T("Bit-perfect at %s", khz)
	case t.Codec == "DSD" || t.Codec == "DST":
		return tr.T("DSD converted to PCM at %s", khz)
	}
	return tr.T("At %s, with digital volume", khz)
}
