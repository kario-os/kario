package mtp

import (
	"bytes"
	"errors"
	"io"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kario-os/kario/usbshare"
)

// A playlist the computer makes is an abstract one (Windows Media Player's
// and libmtp's "name.pla"): an object that is only its references. It is
// stored as what the phone's music library takes from anyone, an M3U8 file
// beside the tracks with their paths relative to it, so it is the same
// playlist after the cable, a restart and on any other computer, and the
// music app reads it as it would one copied over. A playlist file on the
// phone (M3U, PLS) answers GetObjectReferences with the tracks it names.
const (
	maxPlaylist = 1 << 20
	maxRefs     = 1 << 14
	// maxFind bounds the folders one GetObjectReferences lists to find
	// what a playlist names.
	maxFind = 64
)

// playlistFile is the file an abstract playlist called name is stored as.
func playlistFile(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".pla", ".zpl", ".wpl", ".m3u", ".m3u8", ".pls":
		name = strings.TrimSuffix(name, path.Ext(name))
	}
	return name + ".m3u8"
}

func (r *Responder) dropDraft(o *object) {
	if i := slices.Index(r.drafts, o); i >= 0 {
		r.drafts = slices.Delete(r.drafts, i, i+1)
	}
}

// setReferences is SetObjectReferences. A playlist the computer announced
// is written with the tracks given; what is on the phone already is not
// the computer's to change.
func (r *Responder) setReferences(op uint16, h uint32) uint16 {
	b, ok := r.dataset(op)
	o := r.objs.byH[h]
	switch {
	case !ok:
		return RespInvalidDataset
	case o == nil:
		return RespInvalidObjectHandle
	case !o.pending:
		r.logf("usb sharing: the computer may not change %s", o.path)
		return RespObjectWriteProtected
	case !o.playlist:
		return RespAccessDenied
	}
	d := dec{b: b}
	refs := d.a32()
	if d.err != nil {
		return RespInvalidDataset
	}
	dir := path.Dir(o.path)
	if dir == "." {
		dir = ""
	}
	var body bytes.Buffer
	body.WriteString("#EXTM3U\n")
	for _, ref := range refs {
		t := r.objs.byH[ref]
		if t == nil || t.pending || t.item.Dir || t.store != o.store {
			return RespInvalidObjectReference
		}
		line, ok := m3uLine(dir, t.path)
		if !ok {
			return RespInvalidObjectReference
		}
		body.WriteString(line + "\n")
		if body.Len() > maxPlaylist {
			return RespObjectTooLarge
		}
	}
	f, _ := r.folder(o.store)
	p := path.Join(dir, playlistFile(o.item.Name))
	got, err := r.cfg.Lib.Save(f.ID, p, bytes.NewReader(body.Bytes()))
	if err != nil {
		r.logf("usb sharing: the playlist %s/%s not added: %v", f.ID, p, err)
		if errors.Is(err, usbshare.ErrTooLarge) {
			return RespObjectTooLarge
		}
		return RespGeneralError
	}
	r.dropDraft(o)
	name := path.Base(got)
	stored := path.Join(dir, name)
	r.objs.stored(o, stored, usbshare.Item{Name: name, Size: int64(body.Len()), ModTime: time.Now()})
	r.logf("usb sharing: the computer added the playlist %s/%s with %d tracks", f.ID, stored, len(refs))
	r.event(EventObjectInfoChanged, o.handle)
	r.event(EventStorageInfoChanged, storageID(o.store))
	return RespOK
}

// m3uLine is the line naming p (in a storage) in a playlist in the folder
// dir ("" its root): its path from there, which playlistPaths reads back
// as p; false for a path no line can name.
func m3uLine(dir, p string) (string, bool) {
	if !utf8.ValidString(p) || strings.ContainsAny(p, "\r\n") {
		return "", false
	}
	line := p
	if dir != "" {
		ds, ps := strings.Split(dir, "/"), strings.Split(p, "/")
		i := 0
		for i < len(ds) && i < len(ps)-1 && ds[i] == ps[i] {
			i++
		}
		line = strings.Repeat("../", len(ds)-i) + strings.Join(ps[i:], "/")
	}
	if back, ok := inStorage(dir, line); strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" || !ok || back != p {
		line = "./" + line
	}
	if back, ok := inStorage(dir, line); !ok || back != p {
		return "", false
	}
	return line, true
}

// getReferences is GetObjectReferences: a playlist file's tracks that are
// in its storage, in its order; nothing for anything else.
func (r *Responder) getReferences(op uint16, tid, h uint32) uint16 {
	o := r.objs.byH[h]
	if o == nil {
		return RespInvalidObjectHandle
	}
	refs := []uint32{}
	if format := o.format(); !o.pending && (format == FormatM3U || format == FormatPLS) {
		refs = r.playlistRefs(o, format == FormatPLS)
	}
	var e enc
	e.a32(refs)
	return r.sendData(op, tid, e.b)
}

func (r *Responder) playlistRefs(o *object, pls bool) []uint32 {
	f, _ := r.folder(o.store)
	rc, _, err := r.cfg.Lib.Open(f.ID, o.path)
	if err != nil {
		r.logf("usb sharing: %s/%s: %v", f.ID, o.path, err)
		return []uint32{}
	}
	b, err := io.ReadAll(io.LimitReader(rc, maxPlaylist))
	rc.Close()
	if err != nil {
		return []uint32{}
	}
	dir := path.Dir(o.path)
	if dir == "." {
		dir = ""
	}
	refs := []uint32{}
	listed := map[string]bool{}
	for _, p := range playlistPaths(dir, b, pls) {
		if t := r.find(o.store, p, listed); t != nil && !t.item.Dir {
			refs = append(refs, t.handle)
		}
	}
	return refs
}

// find is the object at a path of a storage, listing the folders on the
// way that have not been (at most maxFind of them, in listed).
func (r *Responder) find(store int, p string, listed map[string]bool) *object {
	if o := r.objs.byKey[objKey(store, p)]; o != nil {
		return o
	}
	dir, parent := path.Dir(p), uint32(0)
	if dir == "." {
		dir = ""
	} else {
		d := r.find(store, dir, listed)
		if d == nil || !d.item.Dir {
			return nil
		}
		parent = d.handle
	}
	if listed[dir] || len(listed) >= maxFind {
		return nil
	}
	listed[dir] = true
	r.refresh(watch{store, dir, parent})
	return r.objs.byKey[objKey(store, p)]
}

// playlistPaths are the files a playlist names, as paths in its storage:
// M3U's lines that are not comments (only the line's end trimmed: a name
// may start or end with a space), PLS's FileN entries, relative to the
// playlist's folder dir ("" the storage's root), "\" read as "/". URLs,
// absolute paths and paths out of the storage are left out. A file that
// is not UTF-8 is read as Latin-1, as old M3U files are.
func playlistPaths(dir string, b []byte, pls bool) []string {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	s := string(b)
	if !utf8.ValidString(s) {
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
		}
		s = string(rs)
	}
	out := []string{}
	for line := range strings.Lines(s) {
		line = strings.TrimRight(line, "\r\n")
		if pls {
			k, v, ok := strings.Cut(line, "=")
			if !ok || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(k)), "file") {
				continue
			}
			line = strings.TrimSpace(v)
		} else if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		if p, ok := inStorage(dir, line); ok {
			out = append(out, p)
			if len(out) == maxRefs {
				break
			}
		}
	}
	return out
}

func inStorage(dir, ref string) (string, bool) {
	ref = strings.ReplaceAll(ref, `\`, "/")
	if ref == "" || strings.HasPrefix(ref, "/") || strings.Contains(ref, "://") || len(ref) >= 2 && ref[1] == ':' {
		return "", false
	}
	p := path.Join(dir, ref)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.Count(p, "/") > maxDepth {
		return "", false
	}
	for _, c := range p {
		if unicode.IsControl(c) {
			return "", false
		}
	}
	return p, true
}
