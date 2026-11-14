package tags

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func frame23(id string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString(id)
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.Write([]byte{0, 0})
	b.Write(data)
	return b.Bytes()
}

func tag(ver byte, frames ...[]byte) []byte {
	body := bytes.Join(frames, nil)
	n := len(body)
	h := []byte{'I', 'D', '3', ver, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	return append(h, body...)
}

func utf16le(s string) []byte {
	out := []byte{0xff, 0xfe}
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func gbk(t *testing.T, s string) []byte {
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestID3v23(t *testing.T) {
	img := []byte("\xff\xd8\xff\xe0jpeg")
	raw := tag(3,
		frame23("TIT2", append([]byte{1}, utf16le("\u6625\u6c5f\u82b1\u6708\u591c")...)),
		frame23("TPE1", append([]byte{0}, gbk(t, "\u5f6d\u4e3d\u5a9b")...)),
		frame23("TALB", append([]byte{0}, "Caf\xe9"...)),
		frame23("TRCK", []byte("\x003/12")),
		frame23("TPOS", []byte("\x002")),
		frame23("TCON", []byte("\x00(8)Jazz")),
		frame23("TXXX", []byte("\x00REPLAYGAIN_TRACK_GAIN\x00-6.54 dB")),
		frame23("TXXX", []byte("\x00replaygain_album_gain\x00+1.20 dB")),
		frame23("TXXX", []byte("\x00REPLAYGAIN_TRACK_PEAK\x000.988")),
		frame23("APIC", append([]byte("\x00image/jpeg\x00\x03cover\x00"), img...)),
	)
	tg, ok := ID3v2(raw)
	if !ok {
		t.Fatal("not read")
	}
	if tg.Title != "\u6625\u6c5f\u82b1\u6708\u591c" || tg.Artist != "\u5f6d\u4e3d\u5a9b" || tg.Album != "Caf\u00e9" {
		t.Fatalf("text %q %q %q", tg.Title, tg.Artist, tg.Album)
	}
	if tg.Track != 3 || tg.TrackTotal != 12 || tg.Disc != 2 || tg.Genre != "Jazz" {
		t.Fatalf("numbers %+v", tg)
	}
	if !tg.HasTrackGain || tg.TrackGain != -6.54 || tg.TrackPeak != 0.988 || !tg.HasAlbumGain || tg.AlbumGain != 1.2 {
		t.Fatalf("replaygain %+v", tg)
	}
	if tg.Cover == nil || tg.Cover.MIME != "image/jpeg" || !bytes.Equal(tg.Cover.Data, img) {
		t.Fatalf("cover %+v", tg.Cover)
	}
	if n := ID3v2Size(raw); n != len(raw) {
		t.Fatalf("size %d of %d", n, len(raw))
	}
}

func TestID3v24AndV22(t *testing.T) {
	// 2.4: syncsafe frame sizes, UTF-8, several values, a data length
	// indicator on an unsynchronised frame.
	f := func(id string, flags uint16, data []byte) []byte {
		n := len(data)
		return append([]byte{id[0], id[1], id[2], id[3], byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f), byte(flags >> 8), byte(flags)}, data...)
	}
	unsynced := []byte{0, 0, 0, 7, 3, 'A', 0xff, 0x00, 0xfe, 'B', 'C'}
	raw := tag(4, f("TIT2", 0, []byte("\x03Title\x00Subtitle")), f("TPE1", 0x0003, unsynced))
	tg, _ := ID3v2(raw)
	if tg.Title != "Title, Subtitle" {
		t.Fatalf("title %q", tg.Title)
	}
	if tg.Artist == "" {
		t.Fatal("unsynchronised frame not read")
	}
	v22 := func(id string, data []byte) []byte {
		n := len(data)
		return append([]byte{id[0], id[1], id[2], byte(n >> 16), byte(n >> 8), byte(n)}, data...)
	}
	tg, _ = ID3v2(tag(2, v22("TT2", []byte("\x00Old")), v22("PIC", append([]byte("\x00PNG\x03\x00"), "\x89PNGdata"...))))
	if tg.Title != "Old" || tg.Cover == nil || tg.Cover.MIME != "image/png" {
		t.Fatalf("v2.2 %+v", tg)
	}
}

func TestVorbisComment(t *testing.T) {
	var pic bytes.Buffer
	be := func(v uint32) { binary.Write(&pic, binary.BigEndian, v) }
	be(3)
	be(9)
	pic.WriteString("image/png")
	be(0)
	be(1)
	be(1)
	be(24)
	be(0)
	be(4)
	pic.WriteString("\x89PNG")
	var b bytes.Buffer
	le := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }
	le(6)
	b.WriteString("vendor")
	comments := []string{"TITLE=\u6625\u6653", "artist=A", "ARTIST=B", "ALBUMARTIST=Various", "TRACKNUMBER=4", "TRACKTOTAL=10",
		"DISCNUMBER=1/2", "DATE=1999", "REPLAYGAIN_ALBUM_GAIN=-3.1 dB", "REPLAYGAIN_ALBUM_PEAK=1.0",
		"METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString(pic.Bytes())}
	le(uint32(len(comments)))
	for _, c := range comments {
		le(uint32(len(c)))
		b.WriteString(c)
	}
	tg := VorbisComment(b.Bytes())
	if tg.Title != "\u6625\u6653" || tg.Artist != "A, B" || tg.AlbumArtist != "Various" || tg.Track != 4 || tg.TrackTotal != 10 ||
		tg.Disc != 1 || tg.DiscTotal != 2 || tg.Date != "1999" || !tg.HasAlbumGain || tg.AlbumGain != -3.1 || tg.AlbumPeak != 1 {
		t.Fatalf("%+v", tg)
	}
	if tg.Cover == nil || tg.Cover.MIME != "image/png" || string(tg.Cover.Data) != "\x89PNG" {
		t.Fatalf("cover %+v", tg.Cover)
	}
}

func TestOpusR128(t *testing.T) {
	var tg Tags
	tg.Set("R128_TRACK_GAIN", "-1280")
	if !tg.HasTrackGain || tg.TrackGain != 0 {
		t.Fatalf("R128 -5 dB to -23 LUFS is ReplayGain 0 dB, got %v", tg.TrackGain)
	}
}

func apeTag(items map[string]string, cover []byte) []byte {
	var body bytes.Buffer
	n := 0
	for k, v := range items {
		binary.Write(&body, binary.LittleEndian, uint32(len(v)))
		binary.Write(&body, binary.LittleEndian, uint32(0))
		body.WriteString(k + "\x00" + v)
		n++
	}
	if cover != nil {
		v := append([]byte("cover.jpg\x00"), cover...)
		binary.Write(&body, binary.LittleEndian, uint32(len(v)))
		binary.Write(&body, binary.LittleEndian, uint32(2))
		body.WriteString("Cover Art (Front)\x00")
		body.Write(v)
		n++
	}
	footer := make([]byte, 32)
	copy(footer, "APETAGEX")
	binary.LittleEndian.PutUint32(footer[8:], 2000)
	binary.LittleEndian.PutUint32(footer[12:], uint32(body.Len()+32))
	binary.LittleEndian.PutUint32(footer[16:], uint32(n))
	return append(body.Bytes(), footer...)
}

func TestAPEv2AndID3v1(t *testing.T) {
	audio := bytes.Repeat([]byte{0x55}, 100)
	ape := apeTag(map[string]string{"Title": "Sonata", "Artist": "X", "Album Artist": "Y", "Track": "2/9", "REPLAYGAIN_TRACK_GAIN": "-2.00 dB"}, []byte("\xff\xd8img"))
	v1 := make([]byte, 128)
	copy(v1, "TAGOld title")
	copy(v1[33:], "Old artist")
	file := append(append(append([]byte{}, audio...), ape...), v1...)
	tg, start := ReadTail(bytes.NewReader(file), int64(len(file)))
	if start != int64(len(audio)) {
		t.Fatalf("tags start at %d, the audio ends at %d", start, len(audio))
	}
	if tg.Title != "Sonata" || tg.Artist != "X" || tg.AlbumArtist != "Y" || tg.Track != 2 || tg.TrackTotal != 9 || tg.TrackGain != -2 {
		t.Fatalf("%+v", tg)
	}
	if tg.Cover == nil || string(tg.Cover.Data) != "\xff\xd8img" {
		t.Fatalf("cover %+v", tg.Cover)
	}
	only1 := append(append([]byte{}, audio...), v1...)
	tg, start = ReadTail(bytes.NewReader(only1), int64(len(only1)))
	if tg.Title != "Old title" || tg.Artist != "Old artist" || start != 100 {
		t.Fatalf("ID3v1: %+v at %d", tg, start)
	}
}

// ffmpeg writes ID3v2.4 into MP3s: a cross-check with someone else's
// writer.
func TestFFmpegID3(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	p := filepath.Join(t.TempDir(), "a.mp3")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=d=0.2", "-metadata", "title=\u6c34\u8c03\u6b4c\u5934",
		"-metadata", "artist=\u738b\u83f2", "-metadata", "album=Faye", "-metadata", "track=7/11", p).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot write MP3 here: %v %s", err, out)
	}
	f, _ := os.Open(p)
	defer f.Close()
	tg, _, ok := ReadID3v2(f)
	if !ok || tg.Title != "\u6c34\u8c03\u6b4c\u5934" || tg.Artist != "\u738b\u83f2" || tg.Album != "Faye" || tg.Track != 7 || tg.TrackTotal != 11 {
		t.Fatalf("%v %+v", ok, tg)
	}
}
