package mtp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Initiator is the computer's side: what the tests and the selftest copy
// files with, over a simulated cable or, on the emulator, over usbfs to
// the phone's own gadget (dummy_hcd).
type Initiator struct {
	t      *HostTransport
	rd     *bulkReader
	mu     sync.Mutex
	tid    uint32
	events chan Event
	// Iface is the MTP interface number, for class requests.
	Iface uint16
}

// Event is an event from the device: its code and parameters.
type Event struct {
	Code   uint16
	Params []uint32
}

func NewInitiator(t *HostTransport) *Initiator {
	if t.Packet <= 0 {
		t.Packet = Packet
	}
	i := &Initiator{t: t, rd: &bulkReader{r: t.In, packet: t.Packet}, events: make(chan Event, maxEvents)}
	if t.Intr != nil {
		go i.readEvents()
	}
	return i
}

// Close unplugs the computer's side.
func (i *Initiator) Close() error {
	if i.t.Close != nil {
		return i.t.Close()
	}
	return nil
}

func (i *Initiator) readEvents() {
	rd := &bulkReader{r: i.t.Intr, packet: i.t.Packet}
	for {
		h, err := rd.header()
		if err != nil {
			close(i.events)
			return
		}
		raw, _ := rd.payload(h).all(4 * 5)
		ev := Event{Code: h.code}
		for j := 0; j+4 <= len(raw); j += 4 {
			ev.Params = append(ev.Params, binary.LittleEndian.Uint32(raw[j:]))
		}
		if h.typ == typeEvent {
			select {
			case i.events <- ev:
			default:
			}
		}
	}
}

// Event waits for the next event the device sent.
func (i *Initiator) Event(wait time.Duration) (Event, error) {
	select {
	case ev, ok := <-i.events:
		if !ok {
			return Event{}, io.EOF
		}
		return ev, nil
	case <-time.After(wait):
		return Event{}, errors.New("mtp: no event")
	}
}

// WaitEvent waits for an event with that code, skipping others.
func (i *Initiator) WaitEvent(code uint16, wait time.Duration) (Event, error) {
	end := time.Now().Add(wait)
	for {
		ev, err := i.Event(time.Until(end))
		if err != nil || ev.Code == code {
			return ev, err
		}
	}
}

func (i *Initiator) small(c container) error {
	b := c.encode()
	if _, err := i.t.Out.Write(b); err != nil {
		return err
	}
	if len(b)%i.t.Packet == 0 {
		_, err := i.t.Out.Write(nil)
		return err
	}
	return nil
}

// transact runs one operation: the command, data to the device (out, of
// size bytes) or from it (into in), and the response, whose parameters it
// returns.
func (i *Initiator) transact(op uint16, params []uint32, out io.Reader, size int64, in io.Writer) ([]uint32, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	tid := i.tid
	if op != OpOpenSession {
		i.tid++
		tid = i.tid
	}
	if err := i.small(container{typ: typeCommand, code: op, tid: tid, params: params}); err != nil {
		return nil, err
	}
	if out != nil {
		length := uint32(unknownLength)
		if size+headerLen < unknownLength {
			length = uint32(size + headerLen)
		}
		w := &packetWriter{w: i.t.Out, packet: i.t.Packet}
		hdr := make([]byte, headerLen)
		binary.LittleEndian.PutUint32(hdr, length)
		binary.LittleEndian.PutUint16(hdr[4:], typeData)
		binary.LittleEndian.PutUint16(hdr[6:], op)
		binary.LittleEndian.PutUint32(hdr[8:], tid)
		w.Write(hdr)
		if _, err := io.CopyN(w, out, size); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	}
	for {
		h, err := i.rd.header()
		if err != nil {
			return nil, err
		}
		p := i.rd.payload(h)
		switch h.typ {
		case typeData:
			dst := in
			if dst == nil {
				dst = io.Discard
			}
			if _, err := io.Copy(dst, p); err != nil {
				return nil, err
			}
		case typeResponse:
			raw, err := p.all(4 * 5)
			if err != nil {
				return nil, err
			}
			var res []uint32
			for j := 0; j+4 <= len(raw); j += 4 {
				res = append(res, binary.LittleEndian.Uint32(raw[j:]))
			}
			if h.tid != tid {
				return nil, fmt.Errorf("mtp: the response is to transaction %d, not %d", h.tid, tid)
			}
			if h.code != RespOK {
				return res, &RespError{Op: op, Code: h.code}
			}
			return res, nil
		default:
			p.drain()
		}
	}
}

func (i *Initiator) get(op uint16, params ...uint32) ([]byte, []uint32, error) {
	var b bytes.Buffer
	res, err := i.transact(op, params, nil, 0, &b)
	return b.Bytes(), res, err
}

func (i *Initiator) DeviceInfo() (DeviceInfo, error) {
	b, _, err := i.get(OpGetDeviceInfo)
	if err != nil {
		return DeviceInfo{}, err
	}
	d := dec{b: b}
	var di DeviceInfo
	d.u16()
	d.u32()
	d.u16()
	di.VendorExtension = d.str()
	d.u16()
	di.Operations = d.a16()
	di.Events = d.a16()
	di.DeviceProps = d.a16()
	d.a16()
	di.Formats = d.a16()
	di.Manufacturer, di.Model, di.Version, di.Serial = d.str(), d.str(), d.str(), d.str()
	return di, d.err
}

// OpenSession opens session 1 and starts transactions from 1.
func (i *Initiator) OpenSession() error {
	_, err := i.transact(OpOpenSession, []uint32{1}, nil, 0, nil)
	return err
}

func (i *Initiator) CloseSession() error {
	_, err := i.transact(OpCloseSession, nil, nil, 0, nil)
	return err
}

func (i *Initiator) StorageIDs() ([]uint32, error) {
	b, _, err := i.get(OpGetStorageIDs)
	if err != nil {
		return nil, err
	}
	d := dec{b: b}
	ids := d.a32()
	return ids, d.err
}

func (i *Initiator) StorageInfo(id uint32) (StorageInfo, error) {
	b, _, err := i.get(OpGetStorageInfo, id)
	if err != nil {
		return StorageInfo{}, err
	}
	d := dec{b: b}
	si := StorageInfo{Type: d.u16(), Filesystem: d.u16(), Access: d.u16(), Capacity: d.u64(), Free: d.u64()}
	d.u32()
	si.Description, si.Volume = d.str(), d.str()
	return si, d.err
}

// Handles lists a folder (parent), a storage's root (0xFFFFFFFF) or all
// of it (0).
func (i *Initiator) Handles(storage, format, parent uint32) ([]uint32, error) {
	b, _, err := i.get(OpGetObjectHandles, storage, format, parent)
	if err != nil {
		return nil, err
	}
	d := dec{b: b}
	hs := d.a32()
	return hs, d.err
}

func (i *Initiator) ObjectInfo(h uint32) (ObjectInfo, error) {
	b, _, err := i.get(OpGetObjectInfo, h)
	if err != nil {
		return ObjectInfo{}, err
	}
	return parseObjectInfo(b)
}

// Get copies an object to w.
func (i *Initiator) Get(h uint32, w io.Writer) error {
	_, err := i.transact(OpGetObject, []uint32{h}, nil, 0, w)
	return err
}

// Partial reads n bytes at off: with GetPartialObject64 when wide, else
// GetPartialObject.
func (i *Initiator) Partial(h uint32, off int64, n uint32, wide bool) ([]byte, error) {
	var b []byte
	var res []uint32
	var err error
	if wide {
		b, res, err = i.get(OpGetPartialObject64, h, uint32(off), uint32(off>>32), n)
	} else {
		b, res, err = i.get(OpGetPartialObject, h, uint32(off), n)
	}
	if err == nil && (len(res) == 0 || int(res[0]) != len(b)) {
		err = fmt.Errorf("mtp: %d bytes sent, the response says %v", len(b), res)
	}
	return b, err
}

// SendObjectInfo announces a file (or, with FormatAssociation, makes a
// folder); it returns the new object's handle.
func (i *Initiator) SendObjectInfo(storage, parent uint32, oi ObjectInfo) (uint32, error) {
	b := oi.encode()
	res, err := i.transact(OpSendObjectInfo, []uint32{storage, parent}, bytes.NewReader(b), int64(len(b)), nil)
	if err != nil {
		return 0, err
	}
	if len(res) < 3 {
		return 0, errors.New("mtp: SendObjectInfo without a handle")
	}
	return res[2], nil
}

// SendObject sends the file the last SendObjectInfo announced.
func (i *Initiator) SendObject(r io.Reader, size int64) error {
	_, err := i.transact(OpSendObject, nil, r, size, nil)
	return err
}

// Put announces and sends a file; parent is 0xFFFFFFFF for the root.
func (i *Initiator) Put(storage, parent uint32, name string, data []byte) (uint32, error) {
	size := uint32(len(data))
	h, err := i.SendObjectInfo(storage, parent, ObjectInfo{StorageID: storage, Format: formatOf(name), CompressedSize: size, Filename: name})
	if err != nil {
		return 0, err
	}
	return h, i.SendObject(bytes.NewReader(data), int64(len(data)))
}

// MakeDir makes a folder.
func (i *Initiator) MakeDir(storage, parent uint32, name string) (uint32, error) {
	return i.SendObjectInfo(storage, parent, ObjectInfo{StorageID: storage, Format: FormatAssociation, AssociationType: 1, Filename: name})
}

func (i *Initiator) Delete(h uint32) error {
	_, err := i.transact(OpDeleteObject, []uint32{h, 0}, nil, 0, nil)
	return err
}

func (i *Initiator) Move(h, storage, parent uint32) error {
	_, err := i.transact(OpMoveObject, []uint32{h, storage, parent}, nil, 0, nil)
	return err
}

func (i *Initiator) Copy(h, storage, parent uint32) (uint32, error) {
	res, err := i.transact(OpCopyObject, []uint32{h, storage, parent}, nil, 0, nil)
	if err != nil || len(res) == 0 {
		return 0, err
	}
	return res[0], nil
}

func (i *Initiator) PropsSupported(format uint16) ([]uint16, error) {
	b, _, err := i.get(OpGetObjectPropsSupported, uint32(format))
	if err != nil {
		return nil, err
	}
	d := dec{b: b}
	codes := d.a16()
	return codes, d.err
}

// PropDesc is an object property's description: its type and whether the
// computer may set it.
type PropDesc struct {
	Code, Type uint16
	Settable   bool
	Form       uint8
}

func (i *Initiator) PropDesc(code, format uint16) (PropDesc, error) {
	b, _, err := i.get(OpGetObjectPropDesc, uint32(code), uint32(format))
	if err != nil {
		return PropDesc{}, err
	}
	d := dec{b: b}
	pd := PropDesc{Code: d.u16(), Type: d.u16()}
	pd.Settable = d.u8() == 1
	value(&d, pd.Type)
	d.u32()
	pd.Form = d.u8()
	return pd, d.err
}

// value reads one property value: an integer as uint64, a UINT128 as its
// 16 bytes, a string as a string.
func value(d *dec, typ uint16) any {
	switch typ {
	case typeUint16:
		return uint64(d.u16())
	case typeUint32:
		return uint64(d.u32())
	case typeUint64:
		return d.u64()
	case typeUint128:
		return append([]byte(nil), d.take(16)...)
	case typeString:
		return d.str()
	}
	d.err = fmt.Errorf("mtp: property type 0x%04x", typ)
	return nil
}

// PropEntry is one element of an object property list.
type PropEntry struct {
	Handle uint32
	Code   uint16
	Type   uint16
	Value  any
}

// PropList is GetObjectPropList; code 0xFFFFFFFF asks for every property.
func (i *Initiator) PropList(h, format, code, group, depth uint32) ([]PropEntry, error) {
	b, _, err := i.get(OpGetObjectPropList, h, format, code, group, depth)
	if err != nil {
		return nil, err
	}
	d := dec{b: b}
	n := d.u32()
	out := []PropEntry{}
	for j := uint32(0); j < n && d.err == nil; j++ {
		e := PropEntry{Handle: d.u32(), Code: d.u16(), Type: d.u16()}
		e.Value = value(&d, e.Type)
		out = append(out, e)
	}
	return out, d.err
}

func (i *Initiator) PropValue(h uint32, code uint16) (any, error) {
	b, _, err := i.get(OpGetObjectPropValue, h, uint32(code))
	if err != nil {
		return nil, err
	}
	typ, _ := propType(code)
	d := dec{b: b}
	v := value(&d, typ)
	return v, d.err
}

// SetName is SetObjectPropValue of the file name: a rename.
func (i *Initiator) SetName(h uint32, name string) error {
	var e enc
	e.str(name)
	_, err := i.transact(OpSetObjectPropValue, []uint32{h, PropObjectFileName}, bytes.NewReader(e.b), int64(len(e.b)), nil)
	return err
}

// FriendlyName is the device's name as Windows shows it.
func (i *Initiator) FriendlyName() (string, error) {
	b, _, err := i.get(OpGetDevicePropValue, DevicePropFriendlyName)
	if err != nil {
		return "", err
	}
	d := dec{b: b}
	s := d.str()
	return s, d.err
}

// DeviceStatus is the class request Get Device Status: OK, or busy with
// a cancelled transaction.
func (i *Initiator) DeviceStatus() (uint16, error) {
	if i.t.Ctl == nil {
		return 0, errors.New("mtp: no control endpoint")
	}
	b, err := i.t.Ctl.Control(Setup{RequestType: 0xA1, Request: reqGetDeviceStatus, Index: i.Iface, Length: 20}, nil)
	if err != nil {
		return 0, err
	}
	if len(b) < 4 {
		return 0, errors.New("mtp: a short device status")
	}
	return binary.LittleEndian.Uint16(b[2:]), nil
}

// Cancel is the class request Cancel for the transaction in progress.
func (i *Initiator) Cancel() error {
	if i.t.Ctl == nil {
		return errors.New("mtp: no control endpoint")
	}
	b := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint16(nil, cancelCode), i.tid)
	_, err := i.t.Ctl.Control(Setup{RequestType: 0x21, Request: reqCancel, Index: i.Iface, Length: 6}, b)
	return err
}

// Found is an object Find found.
type Found struct {
	Storage, Handle uint32
	Info            ObjectInfo
}

// Find walks a path whose first element is a storage's description
// ("/Music/Album/01.flac"); the storage alone has Handle 0xFFFFFFFF.
func (i *Initiator) Find(p string) (Found, error) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	ids, err := i.StorageIDs()
	if err != nil {
		return Found{}, err
	}
	var at Found
	for _, id := range ids {
		si, err := i.StorageInfo(id)
		if err != nil {
			return Found{}, err
		}
		if si.Description == parts[0] {
			at = Found{Storage: id, Handle: rootParent}
		}
	}
	if at.Storage == 0 {
		return Found{}, fmt.Errorf("mtp: no storage %q", parts[0])
	}
	for _, name := range parts[1:] {
		hs, err := i.Handles(at.Storage, 0, at.Handle)
		if err != nil {
			return Found{}, err
		}
		next := Found{}
		for _, h := range hs {
			oi, err := i.ObjectInfo(h)
			if err != nil {
				return Found{}, err
			}
			if oi.Filename == name {
				next = Found{Storage: at.Storage, Handle: h, Info: oi}
			}
		}
		if next.Handle == 0 {
			return Found{}, fmt.Errorf("mtp: no %q in %s", name, p)
		}
		at = next
	}
	return at, nil
}

// Names are the file names in a folder found by Find.
func (i *Initiator) Names(at Found) ([]string, error) {
	hs, err := i.Handles(at.Storage, 0, at.Handle)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, h := range hs {
		oi, err := i.ObjectInfo(h)
		if err != nil {
			return nil, err
		}
		out = append(out, oi.Filename)
	}
	return out, nil
}
