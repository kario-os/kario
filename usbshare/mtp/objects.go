package mtp

import (
	"crypto/sha256"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kario-os/kario/usbshare"
)

// Each of the library's folders is a storage of its own, so the computer
// shows Photos, Videos, Music and Files side by side, as the web page and
// the WebDAV drive do; its root is the folder's top.
func storageID(store int) uint32 { return uint32(store+1)<<16 | 1 }

func storeIndex(id uint32) int {
	if id&0xFFFF != 1 {
		return -1
	}
	return int(id>>16) - 1
}

// object is a file or a folder the computer has been told about. Handles
// last for the responder's lifetime; one whose file is gone from its
// folder's listing is forgotten.
type object struct {
	handle uint32
	store  int
	path   string // inside the storage: "Album/01.flac"
	parent uint32 // 0 in the storage's root
	item   usbshare.Item
	// pending: announced by SendObjectInfo, not yet sent; it is not on
	// the phone and only its own SendObject (or DeleteObject) may follow.
	pending bool
	size    int64 // pending: the size announced, -1 when 4 GiB or more
}

func (o *object) format() uint16 {
	if o.item.Dir {
		return FormatAssociation
	}
	return formatOf(o.item.Name)
}

// protection: what is on the phone is read-only to the computer, which
// may add files and folders but never delete, rename or overwrite one
// (27-usb-typec.md §3.1); a folder stays open to files added into it.
func (o *object) protection() uint16 {
	if o.item.Dir || o.pending {
		return 0
	}
	return 1
}

// puid is the persistent unique object identifier Windows tracks objects
// by: the same file has the same one in every connection.
func (o *object) puid(folder string) []byte {
	sum := sha256.Sum256([]byte(folder + "\x00" + o.path))
	return sum[:16]
}

type objects struct {
	next  uint32
	byKey map[string]*object
	byH   map[uint32]*object
}

func newObjects() *objects {
	return &objects{byKey: map[string]*object{}, byH: map[uint32]*object{}}
}

func objKey(store int, p string) string { return strconv.Itoa(store) + "\x00" + p }

func (t *objects) add(o *object) *object {
	t.next++
	o.handle = t.next
	t.byH[o.handle] = o
	if !o.pending {
		t.byKey[objKey(o.store, o.path)] = o
	}
	return o
}

// stored puts a pending object at the path its file got.
func (t *objects) stored(o *object, p string, it usbshare.Item) {
	if old := t.byKey[objKey(o.store, p)]; old != nil && old != o {
		t.forget(old)
	}
	o.pending, o.path, o.item = false, p, it
	t.byKey[objKey(o.store, p)] = o
}

// forget drops an object and, for a folder, what is inside it.
func (t *objects) forget(o *object) {
	delete(t.byH, o.handle)
	if t.byKey[objKey(o.store, o.path)] == o {
		delete(t.byKey, objKey(o.store, o.path))
	}
	if !o.item.Dir {
		return
	}
	prefix := o.path + "/"
	for k, c := range t.byKey {
		if c.store == o.store && strings.HasPrefix(c.path, prefix) {
			delete(t.byKey, k)
			delete(t.byH, c.handle)
		}
	}
}

// in is the objects known in a folder of a storage, pending ones left out.
func (t *objects) in(store int, parent uint32) map[uint32]*object {
	out := map[uint32]*object{}
	for _, o := range t.byKey {
		if o.store == store && o.parent == parent {
			out[o.handle] = o
		}
	}
	return out
}

// update brings a folder's objects up to its listing: new files get
// handles, files gone are forgotten. added and removed are the handles
// that came and went, changed those whose size or time is not what it was.
func (t *objects) update(store int, dir string, parent uint32, items []usbshare.Item) (list []*object, added, removed, changed []uint32) {
	before := t.in(store, parent)
	for _, it := range items {
		if it.Name == "" || strings.ContainsAny(it.Name, "/\\") {
			continue
		}
		p := it.Name
		if dir != "" {
			p = dir + "/" + it.Name
		}
		o := t.byKey[objKey(store, p)]
		if o != nil && o.item.Dir != it.Dir {
			t.forget(o)
			o = nil
		}
		if o == nil {
			o = t.add(&object{store: store, path: p, parent: parent, item: it})
			added = append(added, o.handle)
		} else {
			if o.item.Size != it.Size || !o.item.ModTime.Equal(it.ModTime) {
				changed = append(changed, o.handle)
			}
			o.item = it
			delete(before, o.handle)
		}
		list = append(list, o)
	}
	for h, o := range before {
		t.forget(o)
		removed = append(removed, h)
	}
	return list, added, removed, changed
}

// formats maps file extensions to object formats; anything else is
// undefined, which every initiator takes as a plain file.
var formats = map[string]uint16{
	".jpg": FormatJPEG, ".jpeg": FormatJPEG, ".png": FormatPNG, ".gif": FormatGIF, ".bmp": FormatBMP,
	".txt": FormatText, ".lrc": FormatText, ".cue": FormatText, ".log": FormatText, ".html": FormatHTML, ".htm": FormatHTML,
	".wav": FormatWAV, ".mp3": FormatMP3, ".aif": FormatAIFF, ".aiff": FormatAIFF, ".flac": FormatFLAC,
	".ogg": FormatOGG, ".oga": FormatOGG, ".opus": FormatOGG, ".aac": FormatAAC, ".m4a": FormatAAC,
	".ape": FormatUndefinedAudio, ".wv": FormatUndefinedAudio, ".dsf": FormatUndefinedAudio, ".dff": FormatUndefinedAudio,
	".webm": FormatUndefinedVideo, ".mp4": FormatMP4, ".m3u": FormatM3U, ".m3u8": FormatM3U, ".pls": FormatPLS,
}

func formatOf(name string) uint16 {
	if f, ok := formats[strings.ToLower(path.Ext(name))]; ok {
		return f
	}
	return FormatUndefined
}

// playbackFormats are the formats DeviceInfo lists.
var playbackFormats = []uint16{FormatUndefined, FormatAssociation, FormatText, FormatHTML, FormatAIFF, FormatWAV, FormatMP3,
	FormatJPEG, FormatBMP, FormatGIF, FormatPNG, FormatUndefinedAudio, FormatOGG, FormatAAC, FormatFLAC,
	FormatUndefinedVideo, FormatMP4, FormatM3U, FormatPLS}

// validName is a file or folder name the computer may give: one element,
// valid UTF-8, no control characters, not "." or "..", at most 255 bytes.
// The Shell checks again, with each folder's own rules.
func validName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 255 || !utf8.ValidString(n) || strings.ContainsAny(n, "/\\") {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func accepts(f usbshare.Folder, name string) bool {
	if len(f.Accepts) == 0 {
		return true
	}
	ext := strings.ToLower(path.Ext(name))
	for _, a := range f.Accepts {
		if a == ext {
			return true
		}
	}
	return false
}
