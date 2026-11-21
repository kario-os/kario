package mtp

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/usbshare"
)

// Config is what a Responder serves.
type Config struct {
	Lib usbshare.Library
	// Info names the device: Manufacturer, Model, Version, Serial.
	Info DeviceInfo
	Log  func(format string, a ...any)
	// Poll is how often the folders the computer listed are listed again,
	// to tell it with events what was added on the phone or removed from
	// it meanwhile (0: never).
	Poll time.Duration
}

// The operations the responder answers; DeviceInfo lists them.
var operations = []uint16{
	OpGetDeviceInfo, OpOpenSession, OpCloseSession, OpGetStorageIDs, OpGetStorageInfo, OpGetNumObjects,
	OpGetObjectHandles, OpGetObjectInfo, OpGetObject, OpDeleteObject, OpSendObjectInfo, OpSendObject,
	OpGetDevicePropDesc, OpGetDevicePropValue, OpMoveObject, OpCopyObject, OpGetPartialObject,
	OpGetObjectPropsSupported, OpGetObjectPropDesc, OpGetObjectPropValue, OpSetObjectPropValue,
	OpGetObjectPropList, OpGetPartialObject64,
}

var events = []uint16{EventObjectAdded, EventObjectRemoved, EventObjectInfoChanged, EventStorageInfoChanged}

// vendorExtensions: Microsoft's MTP set, which tells Windows this is an
// MTP device and not a camera, and Android's, whose GetPartialObject64
// libmtp uses (only the operations listed are claimed).
const vendorExtensions = "microsoft.com: 1.0; android.com: 1.0;"

// props are the object properties of every format, with their types.
var props = []struct{ code, typ uint16 }{
	{PropStorageID, typeUint32}, {PropObjectFormat, typeUint16}, {PropProtectionStatus, typeUint16},
	{PropObjectSize, typeUint64}, {PropObjectFileName, typeString}, {PropDateCreated, typeString},
	{PropDateModified, typeString}, {PropParentObject, typeUint32}, {PropPersistentUID, typeUint128},
	{PropName, typeString},
}

func propType(code uint16) (uint16, bool) {
	for _, p := range props {
		if p.code == code {
			return p.typ, true
		}
	}
	return 0, false
}

// Bounds on what one computer can make the responder hold: a dataset it
// sends, the objects one listing returns, the folders polled.
const (
	maxDataset = 64 << 10
	maxListed  = 1 << 16
	maxWatched = 64
	maxDepth   = 9
	maxEvents  = 64
)

// Responder answers one computer over one Transport.
type Responder struct {
	cfg    Config
	t      Transport
	rd     *bulkReader
	packet int

	mu      sync.Mutex
	session uint32
	folders []usbshare.Folder
	objs    *objects
	pending *object
	watched map[watch]bool
	open    struct {
		h uint32
		f io.ReadSeekCloser
	}

	busy, cancelled, reset atomic.Bool
	events                 chan container
}

type watch struct {
	store  int
	dir    string
	parent uint32
}

func NewResponder(cfg Config) *Responder {
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	return &Responder{cfg: cfg, objs: newObjects(), watched: map[watch]bool{}, events: make(chan container, maxEvents)}
}

// Serve answers the computer until the transport ends (the cable is out,
// or the Shell let go). The computer letting go of the function closes the
// session; it may open another when it comes back.
func (r *Responder) Serve(t Transport) error {
	r.t = t
	r.packet = t.Packet
	if r.packet <= 0 {
		r.packet = Packet
	}
	r.rd = &bulkReader{r: t.Out, packet: r.packet}
	done := make(chan struct{})
	defer close(done)
	if t.Ctl != nil {
		go r.control()
	}
	if t.Intr != nil {
		go r.sendEvents(done)
	}
	if r.cfg.Poll > 0 {
		go r.poll(done)
	}
	defer r.closeSession()
	for {
		h, err := r.rd.header()
		if err != nil {
			if Shutdown(err) {
				r.reset.Store(true)
				r.rd.buf = nil
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		if h.typ != typeCommand || h.length < headerLen || h.length > headerLen+4*5 {
			if err := r.rd.payload(h).drain(); err != nil && !Shutdown(err) {
				return err
			}
			continue
		}
		raw, err := r.rd.payload(h).all(4 * 5)
		if err != nil {
			continue
		}
		params := make([]uint32, len(raw)/4)
		for i := range params {
			params[i] = binary.LittleEndian.Uint32(raw[4*i:])
		}
		r.transaction(h, params)
	}
}

func (r *Responder) logf(format string, a ...any) { r.cfg.Log(format, a...) }

func (r *Responder) transaction(h header, params []uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reset.Swap(false) {
		r.closeSessionLocked()
	}
	r.cancelled.Store(false)
	r.busy.Store(true)
	defer r.busy.Store(false)
	code, out := r.dispatch(h.code, h.tid, params)
	if code == 0 || r.cancelled.Load() {
		return
	}
	r.small(container{typ: typeResponse, code: code, tid: h.tid, params: out})
}

// small sends a container shorter than a chunk: a command's response or
// an event.
func (r *Responder) small(c container) error {
	b := c.encode()
	if _, err := r.t.In.Write(b); err != nil {
		return err
	}
	if len(b)%r.packet == 0 {
		_, err := r.t.In.Write(nil)
		return err
	}
	return nil
}

// dataOut starts a data container of size bytes to the computer.
func (r *Responder) dataOut(op uint16, tid uint32, size int64) *packetWriter {
	length := uint32(unknownLength)
	if size+headerLen < unknownLength {
		length = uint32(size + headerLen)
	}
	w := &packetWriter{w: r.t.In, packet: r.packet, stop: r.cancelled.Load}
	hdr := make([]byte, headerLen)
	binary.LittleEndian.PutUint32(hdr, length)
	binary.LittleEndian.PutUint16(hdr[4:], typeData)
	binary.LittleEndian.PutUint16(hdr[6:], op)
	binary.LittleEndian.PutUint32(hdr[8:], tid)
	w.Write(hdr)
	return w
}

func (r *Responder) sendData(op uint16, tid uint32, b []byte) uint16 {
	w := r.dataOut(op, tid, int64(len(b)))
	w.Write(b)
	if err := w.Close(); err != nil {
		return 0
	}
	return RespOK
}

// dataIn reads the data container that follows a command.
func (r *Responder) dataIn(op uint16) (*payload, bool) {
	h, err := r.rd.header()
	if err != nil {
		return nil, false
	}
	p := r.rd.payload(h)
	if h.typ != typeData || h.code != op {
		p.drain()
		return nil, false
	}
	return p, true
}

func (r *Responder) dataset(op uint16) ([]byte, bool) {
	p, ok := r.dataIn(op)
	if !ok {
		return nil, false
	}
	b, err := p.all(maxDataset)
	return b, err == nil
}

func arg(p []uint32, i int) uint32 {
	if i < len(p) {
		return p[i]
	}
	return 0
}

func (r *Responder) dispatch(op uint16, tid uint32, p []uint32) (uint16, []uint32) {
	if r.session == 0 && op != OpGetDeviceInfo && op != OpOpenSession {
		return RespSessionNotOpen, nil
	}
	switch op {
	case OpGetDeviceInfo:
		return r.sendData(op, tid, r.deviceInfo()), nil
	case OpOpenSession:
		switch {
		case arg(p, 0) == 0:
			return RespInvalidParameter, nil
		case r.session != 0:
			return RespSessionAlreadyOpen, []uint32{r.session}
		}
		r.session = arg(p, 0)
		r.folders = r.cfg.Lib.Folders()
		return RespOK, nil
	case OpCloseSession:
		r.closeSessionLocked()
		return RespOK, nil
	case OpGetStorageIDs:
		r.folders = r.cfg.Lib.Folders()
		ids := make([]uint32, len(r.folders))
		for i := range r.folders {
			ids[i] = storageID(i)
		}
		var e enc
		e.a32(ids)
		return r.sendData(op, tid, e.b), nil
	case OpGetStorageInfo:
		r.folders = r.cfg.Lib.Folders()
		i := storeIndex(arg(p, 0))
		if i < 0 || i >= len(r.folders) {
			return RespInvalidStorageID, nil
		}
		return r.sendData(op, tid, storageInfo(r.folders[i])), nil
	case OpGetNumObjects, OpGetObjectHandles:
		list, code := r.handles(arg(p, 0), arg(p, 1), arg(p, 2))
		if code != RespOK {
			return code, nil
		}
		if op == OpGetNumObjects {
			return RespOK, []uint32{uint32(len(list))}
		}
		var e enc
		e.a32(list)
		return r.sendData(op, tid, e.b), nil
	case OpGetObjectInfo:
		o := r.objs.byH[arg(p, 0)]
		if o == nil {
			return RespInvalidObjectHandle, nil
		}
		return r.sendData(op, tid, r.objectInfo(o).encode()), nil
	case OpGetObject:
		code, _ := r.getObject(op, tid, arg(p, 0), 0, -1)
		return code, nil
	case OpGetPartialObject:
		return r.partial(op, tid, arg(p, 0), int64(arg(p, 1)), int64(arg(p, 2)))
	case OpGetPartialObject64:
		return r.partial(op, tid, arg(p, 0), int64(arg(p, 1))|int64(arg(p, 2))<<32, int64(arg(p, 3)))
	case OpDeleteObject:
		return r.deleteObject(arg(p, 0)), nil
	case OpSendObjectInfo:
		return r.sendObjectInfo(op, arg(p, 0), arg(p, 1))
	case OpSendObject:
		return r.sendObject(op), nil
	case OpMoveObject:
		if o := r.objs.byH[arg(p, 0)]; o == nil || o.pending {
			return RespInvalidObjectHandle, nil
		}
		return RespObjectWriteProtected, nil
	case OpCopyObject:
		return r.copyObject(arg(p, 0), arg(p, 1), arg(p, 2))
	case OpGetDevicePropDesc, OpGetDevicePropValue:
		if arg(p, 0) != DevicePropFriendlyName {
			return RespDevicePropNotSupported, nil
		}
		var e enc
		if op == OpGetDevicePropDesc {
			e.u16(DevicePropFriendlyName)
			e.u16(typeString)
			e.u8(0)
			e.str(r.cfg.Info.Model)
			e.str(r.cfg.Info.Model)
			e.u8(0)
		} else {
			e.str(r.cfg.Info.Model)
		}
		return r.sendData(op, tid, e.b), nil
	case OpGetObjectPropsSupported:
		codes := make([]uint16, len(props))
		for i, pr := range props {
			codes[i] = pr.code
		}
		var e enc
		e.a16(codes)
		return r.sendData(op, tid, e.b), nil
	case OpGetObjectPropDesc:
		b, ok := propDesc(uint16(arg(p, 0)))
		if !ok {
			return RespObjectPropNotSupported, nil
		}
		return r.sendData(op, tid, b), nil
	case OpGetObjectPropValue:
		o := r.objs.byH[arg(p, 0)]
		if o == nil {
			return RespInvalidObjectHandle, nil
		}
		var e enc
		if !r.propValue(o, uint16(arg(p, 1)), &e) {
			return RespObjectPropNotSupported, nil
		}
		return r.sendData(op, tid, e.b), nil
	case OpSetObjectPropValue:
		return r.setProp(op, arg(p, 0), uint16(arg(p, 1))), nil
	case OpGetObjectPropList:
		return r.propList(op, tid, arg(p, 0), arg(p, 1), arg(p, 2), arg(p, 3), arg(p, 4)), nil
	}
	return RespOperationNotSupported, nil
}

func (r *Responder) deviceInfo() []byte {
	var e enc
	e.u16(100)
	e.u32(6)
	e.u16(100)
	e.str(vendorExtensions)
	e.u16(0)
	e.a16(operations)
	e.a16(events)
	e.a16([]uint16{DevicePropFriendlyName})
	e.a16(nil)
	e.a16(playbackFormats)
	e.str(r.cfg.Info.Manufacturer)
	e.str(r.cfg.Info.Model)
	e.str(r.cfg.Info.Version)
	e.str(r.cfg.Info.Serial)
	return e.b
}

func storageInfo(f usbshare.Folder) []byte {
	var e enc
	e.u16(0x0003) // fixed RAM
	e.u16(0x0002) // generic hierarchical
	if f.Writable {
		e.u16(0x0000)
	} else {
		e.u16(0x0001) // read-only, without deleting
	}
	e.u64(uint64(max(f.Capacity, 0)))
	e.u64(uint64(max(f.Free, 0)))
	e.u32(0xFFFFFFFF)
	e.str(f.Name)
	e.str(f.ID)
	return e.b
}

func (r *Responder) closeSession() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeSessionLocked()
}

func (r *Responder) closeSessionLocked() {
	r.session, r.pending = 0, nil
	r.closeOpen()
}

func (r *Responder) closeOpen() {
	if r.open.f != nil {
		r.open.f.Close()
	}
	r.open.h, r.open.f = 0, nil
}

// --- Listing ---

func (r *Responder) folder(store int) (usbshare.Folder, bool) {
	if store < 0 || store >= len(r.folders) {
		return usbshare.Folder{}, false
	}
	return r.folders[store], true
}

// children lists a folder of a storage ("" its root) and brings the
// objects up to it; it is watched from then on.
func (r *Responder) children(store int, dir string, parent uint32) ([]*object, bool) {
	list, _, _, _, ok := r.relist(store, dir, parent)
	if ok && len(r.watched) < maxWatched {
		r.watched[watch{store, dir, parent}] = true
	}
	return list, ok
}

func (r *Responder) relist(store int, dir string, parent uint32) (list []*object, added, removed, changed []uint32, ok bool) {
	f, ok := r.folder(store)
	if !ok {
		return nil, nil, nil, nil, false
	}
	var items []usbshare.Item
	if dir == "" {
		items = r.cfg.Lib.List(f.ID)
	} else {
		d, isDirs := r.cfg.Lib.(usbshare.Dirs)
		if !isDirs || !f.Nested {
			return nil, nil, nil, nil, false
		}
		if items, ok = d.ListDir(f.ID, dir); !ok {
			return nil, nil, nil, nil, false
		}
	}
	if !f.Nested {
		flat := items[:0:0]
		for _, it := range items {
			if !it.Dir {
				flat = append(flat, it)
			}
		}
		items = flat
	}
	if len(items) > maxListed {
		items = items[:maxListed]
	}
	list, added, removed, changed = r.objs.update(store, dir, parent, items)
	return list, added, removed, changed, true
}

// handles is GetObjectHandles: the objects in a folder (parent), in a
// storage's root (rootParent) or in all of it (0), of one format or any.
func (r *Responder) handles(storage, format, parent uint32) ([]uint32, uint16) {
	if r.folders == nil {
		r.folders = r.cfg.Lib.Folders()
	}
	stores := []int{}
	switch {
	case parent != 0 && parent != rootParent:
		o := r.objs.byH[parent]
		if o == nil || !o.item.Dir || storage != allStores && storageID(o.store) != storage {
			return nil, RespInvalidParentObject
		}
		list, _ := r.children(o.store, o.path, o.handle)
		return filter(list, format), RespOK
	case storage == allStores:
		for i := range r.folders {
			stores = append(stores, i)
		}
	default:
		i := storeIndex(storage)
		if _, ok := r.folder(i); !ok {
			return nil, RespInvalidStorageID
		}
		stores = append(stores, i)
	}
	var out []uint32
	for _, s := range stores {
		list, _ := r.children(s, "", 0)
		if parent == 0 {
			list = r.below(s, list)
		}
		out = append(out, filter(list, format)...)
	}
	return out, RespOK
}

// below is list with everything inside its folders, as deep as Music
// lets the computer make them.
func (r *Responder) below(store int, list []*object) []*object {
	out := append([]*object(nil), list...)
	level := list
	for depth := 0; depth < maxDepth && len(out) < maxListed; depth++ {
		var next []*object
		for _, o := range level {
			if o.item.Dir {
				kids, _, _, _, _ := r.relist(store, o.path, o.handle)
				next = append(next, kids...)
			}
		}
		out = append(out, next...)
		level = next
	}
	if len(out) > maxListed {
		out = out[:maxListed]
	}
	return out
}

func filter(list []*object, format uint32) []uint32 {
	out := []uint32{}
	for _, o := range list {
		if format == 0 || uint32(o.format()) == format {
			out = append(out, o.handle)
		}
	}
	return out
}

func (r *Responder) objectInfo(o *object) ObjectInfo {
	oi := ObjectInfo{StorageID: storageID(o.store), Format: o.format(), Protection: o.protection(), Parent: o.parent,
		Filename: o.item.Name, Created: dateString(o.item.ModTime), Modified: dateString(o.item.ModTime)}
	size := o.item.Size
	if o.pending {
		size = o.size
	}
	oi.CompressedSize = uint32(unknownLength)
	if size >= 0 && size < unknownLength {
		oi.CompressedSize = uint32(size)
	}
	if o.item.Dir {
		oi.CompressedSize, oi.AssociationType = 0, 1
	}
	return oi
}

// --- Properties ---

func propDesc(code uint16) ([]byte, bool) {
	typ, ok := propType(code)
	if !ok {
		return nil, false
	}
	var e enc
	e.u16(code)
	e.u16(typ)
	e.u8(0) // get only: names and everything else are the phone's
	zero(&e, typ)
	e.u32(0)
	if code == PropDateCreated || code == PropDateModified {
		e.u8(0x03) // DateTime form
	} else {
		e.u8(0)
	}
	return e.b, true
}

func zero(e *enc, typ uint16) {
	switch typ {
	case typeUint16:
		e.u16(0)
	case typeUint32:
		e.u32(0)
	case typeUint64:
		e.u64(0)
	case typeUint128:
		e.u64(0)
		e.u64(0)
	case typeString:
		e.str("")
	}
}

func (r *Responder) propValue(o *object, code uint16, e *enc) bool {
	switch code {
	case PropStorageID:
		e.u32(storageID(o.store))
	case PropObjectFormat:
		e.u16(o.format())
	case PropProtectionStatus:
		e.u16(o.protection())
	case PropObjectSize:
		size := o.item.Size
		if o.pending {
			size = max(o.size, 0)
		}
		e.u64(uint64(size))
	case PropObjectFileName, PropName:
		e.str(o.item.Name)
	case PropDateCreated, PropDateModified:
		e.str(dateString(o.item.ModTime))
	case PropParentObject:
		e.u32(o.parent)
	case PropPersistentUID:
		f, _ := r.folder(o.store)
		e.b = append(e.b, o.puid(f.ID)...)
	default:
		return false
	}
	return true
}

// propList is GetObjectPropList, the way Windows reads a folder, read as
// Android does: an object (depth 0), an object and its contents (depth
// 1), every storage's root (the handle 0), everything (0xFFFFFFFF); all
// properties (0xFFFFFFFF) or one. Groups and deeper lists are not offered.
func (r *Responder) propList(op uint16, tid, h, format, code, group, depth uint32) uint16 {
	switch {
	case code == 0 && group != 0:
		return RespGroupUnsupported
	case code == 0:
		return RespParameterNotSupported
	case code != 0xFFFFFFFF:
		if _, ok := propType(uint16(code)); !ok {
			return RespObjectPropNotSupported
		}
	}
	if depth == 0xFFFFFFFF && (h == 0 || h == allObjects) {
		h, depth = allObjects, 0
	}
	if depth > 1 {
		return RespDepthUnsupported
	}
	if r.folders == nil {
		r.folders = r.cfg.Lib.Folders()
	}
	var objs []*object
	switch h {
	case allObjects:
		for i := range r.folders {
			list, _ := r.children(i, "", 0)
			objs = append(objs, r.below(i, list)...)
		}
	case 0:
		for i := range r.folders {
			list, _ := r.children(i, "", 0)
			objs = append(objs, list...)
		}
	default:
		o := r.objs.byH[h]
		if o == nil {
			return RespInvalidObjectHandle
		}
		objs = []*object{o}
		if depth == 1 && o.item.Dir {
			kids, _ := r.children(o.store, o.path, o.handle)
			objs = append(objs, kids...)
		}
	}
	var e enc
	e.u32(0)
	n := uint32(0)
	for _, o := range objs {
		if format != 0 && uint32(o.format()) != format {
			continue
		}
		for _, pr := range props {
			if code != 0xFFFFFFFF && uint32(pr.code) != code {
				continue
			}
			e.u32(o.handle)
			e.u16(pr.code)
			e.u16(pr.typ)
			r.propValue(o, pr.code, &e)
			n++
		}
	}
	binary.LittleEndian.PutUint32(e.b, n)
	return r.sendData(op, tid, e.b)
}

// setProp is SetObjectPropValue: only a file announced and not yet sent
// takes a new name; what is on the phone is not the computer's to rename.
func (r *Responder) setProp(op uint16, h uint32, code uint16) uint16 {
	b, ok := r.dataset(op)
	o := r.objs.byH[h]
	switch {
	case !ok:
		return RespInvalidDataset
	case o == nil:
		return RespInvalidObjectHandle
	case code != PropObjectFileName && code != PropName:
		if _, known := propType(code); !known {
			return RespObjectPropNotSupported
		}
		return RespAccessDenied
	case !o.pending:
		r.logf("usb sharing: the computer may not rename %s", o.path)
		return RespObjectWriteProtected
	}
	d := dec{b: b}
	name := d.str()
	if d.err != nil || !validName(name) {
		return RespInvalidObjectPropFormat
	}
	o.item.Name = name
	o.path = path.Join(path.Dir(o.path), name)
	return RespOK
}

// --- Files ---

// opened is the object's file, kept open for the next piece of it.
func (r *Responder) opened(o *object) (io.ReadSeekCloser, int64, error) {
	if r.open.h == o.handle && r.open.f != nil {
		size := o.item.Size
		if f, ok := r.open.f.(*os.File); ok {
			if fi, err := f.Stat(); err == nil {
				size = fi.Size()
			}
		}
		return r.open.f, size, nil
	}
	r.closeOpen()
	f, _ := r.folder(o.store)
	rc, it, err := r.cfg.Lib.Open(f.ID, o.path)
	if err != nil {
		return nil, 0, err
	}
	size := it.Size
	if file, ok := rc.(*os.File); ok {
		if fi, err := file.Stat(); err == nil {
			size = fi.Size()
		}
	}
	r.open.h, r.open.f = o.handle, rc
	return rc, size, nil
}

// getObject sends n bytes of an object from off (n -1: to its end); it
// returns how many the data phase held.
func (r *Responder) getObject(op uint16, tid, h uint32, off, n int64) (uint16, int64) {
	o := r.objs.byH[h]
	if o == nil || o.pending || o.item.Dir {
		return RespInvalidObjectHandle, 0
	}
	rc, size, err := r.opened(o)
	if err != nil {
		r.logf("usb sharing: %s: %v", o.path, err)
		return RespInvalidObjectHandle, 0
	}
	if off > size {
		return RespInvalidParameter, 0
	}
	if n < 0 || off+n > size {
		n = size - off
	}
	if _, err := rc.Seek(off, io.SeekStart); err != nil {
		r.closeOpen()
		return RespGeneralError, 0
	}
	w := r.dataOut(op, tid, n)
	got, err := io.CopyN(w, rc, n)
	if errors.Is(err, errCancelled) {
		r.closeOpen()
		return 0, 0
	}
	if got < n && w.err == nil {
		// The file shrank: the container still has the length it said.
		io.CopyN(w, zeros{}, n-got)
	}
	if err := w.Close(); err != nil {
		r.closeOpen()
		return 0, 0
	}
	if off == 0 {
		f, _ := r.folder(o.store)
		r.logf("usb sharing: %s/%s copied to the computer", f.ID, o.path)
	}
	if got < n {
		r.closeOpen()
		return RespIncompleteTransfer, n
	}
	return RespOK, n
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func (r *Responder) partial(op uint16, tid, h uint32, off, n int64) (uint16, []uint32) {
	code, sent := r.getObject(op, tid, h, off, n)
	if code != RespOK {
		return code, nil
	}
	return code, []uint32{uint32(sent)}
}

// deleteObject: the computer deletes nothing on the phone; a file it
// announced and has not sent is only forgotten.
func (r *Responder) deleteObject(h uint32) uint16 {
	o := r.objs.byH[h]
	switch {
	case h == allObjects:
		return RespObjectWriteProtected
	case o == nil:
		return RespInvalidObjectHandle
	case o.pending:
		if r.pending == o {
			r.pending = nil
		}
		r.objs.forget(o)
		return RespOK
	}
	r.logf("usb sharing: the computer may not delete %s", o.path)
	return RespObjectWriteProtected
}

// parentDir is the folder a SendObjectInfo or CopyObject names: the
// storage's root (0 or 0xFFFFFFFF) or a folder in it.
func (r *Responder) parentDir(store int, parent uint32) (string, uint32, uint16) {
	if parent == 0 || parent == rootParent {
		return "", 0, RespOK
	}
	o := r.objs.byH[parent]
	if o == nil || !o.item.Dir || o.store != store {
		return "", 0, RespInvalidParentObject
	}
	return o.path, o.handle, RespOK
}

// sendObjectInfo is the first half of an upload: the computer says what
// it will send and where. A folder is made at once (only Music keeps
// folders); a file gets a handle and waits for SendObject. What a folder
// would refuse anyway (its kinds of file, its largest file, the free
// space) is refused here, before the bytes come.
func (r *Responder) sendObjectInfo(op uint16, storage, parent uint32) (uint16, []uint32) {
	b, ok := r.dataset(op)
	if !ok {
		return RespInvalidDataset, nil
	}
	if r.pending != nil {
		r.objs.forget(r.pending)
		r.pending = nil
	}
	if r.folders == nil {
		r.folders = r.cfg.Lib.Folders()
	}
	store := storeIndex(storage)
	f, ok := r.folder(store)
	if !ok {
		return RespInvalidStorageID, nil
	}
	if !f.Writable {
		return RespStoreReadOnly, nil
	}
	dir, parentH, code := r.parentDir(store, parent)
	if code != RespOK {
		return code, nil
	}
	oi, err := parseObjectInfo(b)
	if err != nil || !validName(oi.Filename) {
		return RespInvalidDataset, nil
	}
	p := oi.Filename
	if dir != "" {
		p = dir + "/" + oi.Filename
	}
	outParent := parentH
	if parentH == 0 {
		outParent = rootParent
	}
	if oi.Format == FormatAssociation {
		d, isDirs := r.cfg.Lib.(usbshare.Dirs)
		if !isDirs || !f.Nested {
			return RespAccessDenied, nil
		}
		if err := d.MakeDir(f.ID, p); err != nil {
			r.logf("usb sharing: making the folder %s/%s: %v", f.ID, p, err)
			return RespAccessDenied, nil
		}
		r.logf("usb sharing: the computer made the folder %s/%s", f.ID, p)
		o := r.objs.byKey[objKey(store, p)]
		if o == nil {
			o = r.objs.add(&object{store: store, path: p, parent: parentH, item: usbshare.Item{Name: oi.Filename, Dir: true, ModTime: time.Now()}})
		}
		return RespOK, []uint32{storage, outParent, o.handle}
	}
	size := int64(oi.CompressedSize)
	if oi.CompressedSize == unknownLength {
		size = -1
	}
	switch {
	case !accepts(f, oi.Filename):
		return RespInvalidObjectFormatCode, nil
	case f.MaxBytes > 0 && size > f.MaxBytes:
		return RespObjectTooLarge, nil
	case size < 0 && f.MaxBytes > 0 && f.MaxBytes < unknownLength:
		return RespObjectTooLarge, nil
	case f.Free > 0 && size > f.Free:
		return RespStoreFull, nil
	}
	o := r.objs.add(&object{store: store, path: p, parent: parentH, pending: true, size: size,
		item: usbshare.Item{Name: oi.Filename, ModTime: time.Now()}})
	r.pending = o
	return RespOK, []uint32{storage, outParent, o.handle}
}

// upload is SendObject's data on its way to the library: it ends with an
// error, never a clean end, if the computer sent less than it announced,
// so the Shell does not keep a file cut short.
type upload struct {
	p    *payload
	want int64 // -1: what the container brings
	stop func() bool
}

func (u *upload) Read(b []byte) (int, error) {
	if u.stop() {
		return 0, errCancelled
	}
	n, err := u.p.Read(b)
	if err == io.EOF && u.want >= 0 && u.p.n != u.want {
		return n, errTruncated
	}
	return n, err
}

func (r *Responder) sendObject(op uint16) uint16 {
	o := r.pending
	r.pending = nil
	p, ok := r.dataIn(op)
	if !ok {
		if o != nil {
			r.objs.forget(o)
		}
		return RespInvalidDataset
	}
	if o == nil {
		p.drain()
		return RespNoValidObjectInfo
	}
	if o.size >= 0 && p.left != o.size {
		p.drain()
		r.objs.forget(o)
		return RespIncompleteTransfer
	}
	f, _ := r.folder(o.store)
	got, err := r.cfg.Lib.Save(f.ID, o.path, &upload{p: p, want: o.size, stop: r.cancelled.Load})
	derr := p.drain()
	switch {
	case r.cancelled.Load():
		r.objs.forget(o)
		return 0
	case err == nil && derr != nil:
		err = derr
	}
	if err != nil {
		r.objs.forget(o)
		r.logf("usb sharing: %s/%s not added: %v", f.ID, o.path, err)
		switch {
		case errors.Is(err, usbshare.ErrTooLarge):
			return RespObjectTooLarge
		case errors.Is(err, errTruncated), errors.Is(err, io.ErrUnexpectedEOF):
			return RespIncompleteTransfer
		}
		return RespGeneralError
	}
	name := path.Base(got)
	dir := path.Dir(o.path)
	stored := name
	if dir != "." {
		stored = dir + "/" + name
	}
	renamed := stored != o.path
	r.objs.stored(o, stored, usbshare.Item{Name: name, Size: p.n, ModTime: time.Now()})
	r.logf("usb sharing: the computer added %s/%s", f.ID, stored)
	if renamed {
		r.event(EventObjectInfoChanged, o.handle)
	}
	r.event(EventStorageInfoChanged, storageID(o.store))
	return RespOK
}

// copyObject copies a file the phone has into a folder that takes it,
// as if the computer had downloaded and sent it again.
func (r *Responder) copyObject(h, storage, parent uint32) (uint16, []uint32) {
	src := r.objs.byH[h]
	if src == nil || src.pending || src.item.Dir {
		return RespInvalidObjectHandle, nil
	}
	store := storeIndex(storage)
	f, ok := r.folder(store)
	if !ok {
		return RespInvalidStorageID, nil
	}
	if !f.Writable {
		return RespStoreReadOnly, nil
	}
	dir, parentH, code := r.parentDir(store, parent)
	if code != RespOK {
		return code, nil
	}
	if !accepts(f, src.item.Name) {
		return RespInvalidObjectFormatCode, nil
	}
	if f.MaxBytes > 0 && src.item.Size > f.MaxBytes {
		return RespObjectTooLarge, nil
	}
	sf, _ := r.folder(src.store)
	r.closeOpen()
	rc, _, err := r.cfg.Lib.Open(sf.ID, src.path)
	if err != nil {
		return RespInvalidObjectHandle, nil
	}
	defer rc.Close()
	p := src.item.Name
	if dir != "" {
		p = dir + "/" + p
	}
	got, err := r.cfg.Lib.Save(f.ID, p, rc)
	if err != nil {
		r.logf("usb sharing: copying %s/%s to %s: %v", sf.ID, src.path, f.ID, err)
		if errors.Is(err, usbshare.ErrTooLarge) {
			return RespObjectTooLarge, nil
		}
		return RespGeneralError, nil
	}
	name := path.Base(got)
	stored := name
	if dir != "" {
		stored = dir + "/" + name
	}
	o := r.objs.add(&object{store: store, path: stored, parent: parentH, item: usbshare.Item{Name: name, Size: src.item.Size, ModTime: time.Now()}})
	r.logf("usb sharing: the computer added %s/%s", f.ID, stored)
	r.event(EventStorageInfoChanged, storageID(store))
	return RespOK, []uint32{o.handle}
}

// --- The control endpoint, events ---

func (r *Responder) control() {
	for {
		ev, err := r.t.Ctl.Next()
		if err != nil {
			if Shutdown(err) {
				continue
			}
			return
		}
		switch ev.Kind {
		case EvDisable, EvUnbind:
			r.reset.Store(true)
			r.cancelled.Store(true)
		case EvSetup:
			r.request(ev.Setup)
		}
	}
}

// request answers PTP's class requests: Cancel ends the transaction in
// progress (its data is abandoned between chunks), Device Reset closes
// the session, Get Device Status says whether it is still busy with a
// cancelled one. Anything else is stalled.
func (r *Responder) request(s Setup) {
	const classOut, classIn = 0x21, 0xA1
	switch {
	case s.RequestType == classOut && s.Request == reqCancel:
		b, err := r.t.Ctl.Data(int(s.Length))
		if err == nil && len(b) >= 6 && binary.LittleEndian.Uint16(b) == cancelCode {
			r.cancelled.Store(true)
			r.logf("usb sharing: the computer cancelled transaction %d", binary.LittleEndian.Uint32(b[2:]))
		}
	case s.RequestType == classOut && s.Request == reqDeviceReset:
		r.t.Ctl.Data(0)
		r.cancelled.Store(true)
		r.reset.Store(true)
	case s.RequestType == classIn && s.Request == reqGetDeviceStatus:
		code := uint16(RespOK)
		if r.busy.Load() && r.cancelled.Load() {
			code = RespDeviceBusy
		}
		b := binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, 4), code)
		r.t.Ctl.Reply(b[:min(len(b), int(s.Length))])
	default:
		r.t.Ctl.Stall(s)
	}
}

// event queues an event for the interrupt endpoint; one the computer is
// too slow to take is dropped.
func (r *Responder) event(code uint16, param uint32) {
	select {
	case r.events <- container{typ: typeEvent, code: code, params: []uint32{param}}:
	default:
	}
}

func (r *Responder) sendEvents(done <-chan struct{}) {
	for {
		select {
		case c := <-r.events:
			if _, err := r.t.Intr.Write(c.encode()); err != nil && !Shutdown(err) {
				return
			}
		case <-done:
			return
		}
	}
}

// poll lists again what the computer listed: what the phone added or
// removed meanwhile (a photo taken, a file deleted) reaches it as events.
func (r *Responder) poll(done <-chan struct{}) {
	tick := time.NewTicker(r.cfg.Poll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}
		r.mu.Lock()
		if r.session != 0 {
			for w := range r.watched {
				if w.parent != 0 && r.objs.byH[w.parent] == nil {
					delete(r.watched, w)
					continue
				}
				_, added, removed, changed, ok := r.relist(w.store, w.dir, w.parent)
				if !ok {
					delete(r.watched, w)
					continue
				}
				for _, h := range added {
					r.event(EventObjectAdded, h)
				}
				for _, h := range removed {
					r.event(EventObjectRemoved, h)
				}
				for _, h := range changed {
					r.event(EventObjectInfoChanged, h)
				}
				if len(added)+len(removed) > 0 {
					r.event(EventStorageInfoChanged, storageID(w.store))
				}
			}
		}
		r.mu.Unlock()
	}
}
