package mtp

import "github.com/kario-os/kario/usbshare"

// Bounds on a thumbnail the library hands over.
const (
	maxThumb     = 1 << 20
	maxThumbSide = 1024
)

// thumbs: the library keeps thumbnails of some folder's files, so
// GetThumb is offered.
func (r *Responder) thumbs() bool {
	if _, ok := r.cfg.Lib.(usbshare.Thumbs); !ok {
		return false
	}
	if r.folders == nil {
		r.folders = r.cfg.Lib.Folders()
	}
	for _, f := range r.folders {
		if f.Thumbs {
			return true
		}
	}
	return false
}

// thumb is an object's thumbnail, a JPEG the library made, nil when it has
// none: a folder, a file in a folder without thumbnails, a video whose
// poster frame is not made yet. What it said is kept on the object (its
// ObjectInfo gives the size) until the file changes, and the last bytes
// for the GetThumb that follows a GetObjectInfo; a failure is asked again.
func (r *Responder) thumb(o *object) []byte {
	if o.pending || o.item.Dir {
		return nil
	}
	lib, ok := r.cfg.Lib.(usbshare.Thumbs)
	f, _ := r.folder(o.store)
	if !ok || !f.Thumbs {
		return nil
	}
	if r.lastThumb.h == o.handle && o.thumb != nil {
		return r.lastThumb.jpeg
	}
	t, err := lib.Thumb(f.ID, o.path)
	j := t.JPEG
	if err != nil || len(j) > maxThumb || len(j) < 2 || j[0] != 0xFF || j[1] != 0xD8 ||
		t.W <= 0 || t.H <= 0 || t.W > maxThumbSide || t.H > maxThumbSide {
		o.thumb = nil
		return nil
	}
	o.thumb = &thumbInfo{size: uint32(len(j)), w: uint32(t.W), h: uint32(t.H)}
	r.lastThumb.h, r.lastThumb.jpeg = o.handle, j
	return j
}

func (r *Responder) getThumb(op uint16, tid, h uint32) uint16 {
	o := r.objs.byH[h]
	if o == nil {
		return RespInvalidObjectHandle
	}
	j := r.thumb(o)
	if j == nil {
		return RespNoThumbnailPresent
	}
	return r.sendData(op, tid, j)
}
