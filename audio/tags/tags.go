// Package tags reads what music files say about themselves: Vorbis
// comments (FLAC, Ogg), ID3v2 and ID3v1 (MP3, and inside WAV, AIFF and
// DSF), and APEv2 (Monkey's Audio, WavPack, some MP3s). MP4 atoms are read
// with the container in audio/musicfile. All text comes out as UTF-8.
package tags

import (
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"strings"
)

// Tags is the common subset.
type Tags struct {
	Title, Artist, Album, AlbumArtist string
	Genre, Date, Composer             string
	Track, TrackTotal                 int
	Disc, DiscTotal                   int
	// ReplayGain in dB and as linear peaks; Has* says the tag was there.
	TrackGain, TrackPeak float64
	AlbumGain, AlbumPeak float64
	HasTrackGain         bool
	HasAlbumGain         bool
	// CueSheet is an embedded cue sheet (FLAC's and APE's CUESHEET tag).
	CueSheet string
	Cover    *Cover
}

// Cover is an attached picture: its bytes, or where they are in the file
// (Offset, Size) when they are stored as is.
type Cover struct {
	MIME   string
	Data   []byte
	Offset int64
	Size   int64
}

// Merge fills what t lacks from o.
func (t *Tags) Merge(o Tags) {
	str := func(a *string, b string) {
		if *a == "" {
			*a = b
		}
	}
	num := func(a *int, b int) {
		if *a == 0 {
			*a = b
		}
	}
	str(&t.Title, o.Title)
	str(&t.Artist, o.Artist)
	str(&t.Album, o.Album)
	str(&t.AlbumArtist, o.AlbumArtist)
	str(&t.Genre, o.Genre)
	str(&t.Date, o.Date)
	str(&t.Composer, o.Composer)
	str(&t.CueSheet, o.CueSheet)
	num(&t.Track, o.Track)
	num(&t.TrackTotal, o.TrackTotal)
	num(&t.Disc, o.Disc)
	num(&t.DiscTotal, o.DiscTotal)
	if !t.HasTrackGain && o.HasTrackGain {
		t.TrackGain, t.TrackPeak, t.HasTrackGain = o.TrackGain, o.TrackPeak, true
	}
	if !t.HasAlbumGain && o.HasAlbumGain {
		t.AlbumGain, t.AlbumPeak, t.HasAlbumGain = o.AlbumGain, o.AlbumPeak, true
	}
	if t.Cover == nil {
		t.Cover = o.Cover
	}
}

// Set applies one field by its usual name (Vorbis comment and APE keys,
// ID3's TXXX descriptions); unknown names are ignored.
func (t *Tags) Set(key, value string) {
	value = strings.TrimRight(strings.TrimSpace(value), "\x00")
	if value == "" {
		return
	}
	add := func(s *string) {
		if *s == "" {
			*s = value
		} else if !strings.Contains(*s, value) {
			*s += ", " + value
		}
	}
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "TITLE":
		add(&t.Title)
	case "ARTIST", "PERFORMER":
		add(&t.Artist)
	case "ALBUM":
		add(&t.Album)
	case "ALBUMARTIST", "ALBUM ARTIST", "ALBUM_ARTIST":
		add(&t.AlbumArtist)
	case "GENRE":
		add(&t.Genre)
	case "DATE", "YEAR":
		if t.Date == "" {
			t.Date = value
		}
	case "COMPOSER":
		add(&t.Composer)
	case "TRACKNUMBER", "TRACK":
		t.Track, t.TrackTotal = numPair(value, t.TrackTotal)
	case "TRACKTOTAL", "TOTALTRACKS":
		t.TrackTotal, _ = strconv.Atoi(value)
	case "DISCNUMBER", "DISC":
		t.Disc, t.DiscTotal = numPair(value, t.DiscTotal)
	case "DISCTOTAL", "TOTALDISCS":
		t.DiscTotal, _ = strconv.Atoi(value)
	case "REPLAYGAIN_TRACK_GAIN":
		if g, ok := ParseGain(value); ok {
			t.TrackGain, t.HasTrackGain = g, true
		}
	case "REPLAYGAIN_ALBUM_GAIN":
		if g, ok := ParseGain(value); ok {
			t.AlbumGain, t.HasAlbumGain = g, true
		}
	case "REPLAYGAIN_TRACK_PEAK":
		t.TrackPeak, _ = strconv.ParseFloat(value, 64)
	case "REPLAYGAIN_ALBUM_PEAK":
		t.AlbumPeak, _ = strconv.ParseFloat(value, 64)
	case "R128_TRACK_GAIN":
		// Opus (RFC 7845): Q7.8 dB towards -23 LUFS; ReplayGain's
		// reference is 5 dB louder.
		if q, err := strconv.Atoi(value); err == nil && !t.HasTrackGain {
			t.TrackGain, t.HasTrackGain = float64(q)/256+5, true
		}
	case "R128_ALBUM_GAIN":
		if q, err := strconv.Atoi(value); err == nil && !t.HasAlbumGain {
			t.AlbumGain, t.HasAlbumGain = float64(q)/256+5, true
		}
	case "CUESHEET":
		t.CueSheet = value
	}
}

// numPair reads "3" or "3/12".
func numPair(v string, total int) (int, int) {
	a, b, ok := strings.Cut(v, "/")
	n, _ := strconv.Atoi(strings.TrimSpace(a))
	if ok {
		if m, err := strconv.Atoi(strings.TrimSpace(b)); err == nil {
			total = m
		}
	}
	return n, total
}

// ParseGain reads "-6.54 dB" (or without the unit).
func ParseGain(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(v, "dB"), "DB"))
	g, err := strconv.ParseFloat(strings.TrimPrefix(v, "+"), 64)
	return g, err == nil && g > -100 && g < 100
}

// VorbisComment reads a Vorbis comment block: FLAC's VORBIS_COMMENT, or
// an Ogg comment packet after its "\x03vorbis" or "OpusTags" prefix.
func VorbisComment(raw []byte) Tags {
	var t Tags
	le := binary.LittleEndian
	if len(raw) < 8 {
		return t
	}
	vendor := int(le.Uint32(raw))
	p := 4 + vendor
	if p+4 > len(raw) || vendor < 0 {
		return t
	}
	n := int(le.Uint32(raw[p:]))
	p += 4
	for i := 0; i < n && p+4 <= len(raw); i++ {
		l := int(le.Uint32(raw[p:]))
		p += 4
		if l < 0 || p+l > len(raw) {
			break
		}
		k, v, ok := strings.Cut(string(raw[p:p+l]), "=")
		p += l
		if !ok {
			continue
		}
		switch strings.ToUpper(k) {
		case "METADATA_BLOCK_PICTURE":
			if b, err := base64.StdEncoding.DecodeString(v); err == nil {
				if c := flacPicture(b); c != nil && (t.Cover == nil || c.front) {
					t.Cover = &c.Cover
				}
			}
		case "COVERART":
			if b, err := base64.StdEncoding.DecodeString(v); err == nil && t.Cover == nil {
				t.Cover = &Cover{MIME: "image/jpeg", Data: b, Size: int64(len(b))}
			}
		default:
			t.Set(k, v)
		}
	}
	return t
}

type picture struct {
	Cover
	front bool
}

// flacPicture reads a FLAC PICTURE block's body.
func flacPicture(b []byte) *picture {
	be := binary.BigEndian
	if len(b) < 32 {
		return nil
	}
	typ := be.Uint32(b)
	ml := int(be.Uint32(b[4:]))
	if 8+ml+4 > len(b) {
		return nil
	}
	mime := string(b[8 : 8+ml])
	p := 8 + ml
	dl := int(be.Uint32(b[p:]))
	p += 4 + dl + 16
	if p+4 > len(b) || dl < 0 {
		return nil
	}
	n := int(be.Uint32(b[p:]))
	p += 4
	if p+n > len(b) || n < 0 {
		return nil
	}
	return &picture{Cover: Cover{MIME: mime, Data: b[p : p+n], Size: int64(n)}, front: typ == 3}
}
