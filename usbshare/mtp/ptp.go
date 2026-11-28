// Package mtp is USB file sharing as the Media Transfer Protocol (MTP, on
// PTP's container format over USB bulk endpoints), the way Windows
// Explorer, Linux file managers (gvfs, KIO through libmtp) and macOS tools
// (Android File Transfer, OpenMTP) expect a phone (27-usb-typec.md §3.3).
// The Responder runs in phone-usbshared over a usbshare.Library, the same
// one the WebDAV server uses, so the Shell's checks and the folders' rules
// are the same: four storages, Photos, Videos, Music and Files; the
// computer reads everything and adds files under a free name, makes
// folders only in Music, and never deletes, renames or overwrites what is
// on the phone. The Initiator is the computer's side, for tests and the
// selftest.
package mtp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
	"unicode/utf16"
)

// Container types.
const (
	typeCommand  = 1
	typeData     = 2
	typeResponse = 3
	typeEvent    = 4
)

const headerLen = 12

// Operations.
const (
	OpGetDeviceInfo           = 0x1001
	OpOpenSession             = 0x1002
	OpCloseSession            = 0x1003
	OpGetStorageIDs           = 0x1004
	OpGetStorageInfo          = 0x1005
	OpGetNumObjects           = 0x1006
	OpGetObjectHandles        = 0x1007
	OpGetObjectInfo           = 0x1008
	OpGetObject               = 0x1009
	OpGetThumb                = 0x100A
	OpDeleteObject            = 0x100B
	OpSendObjectInfo          = 0x100C
	OpSendObject              = 0x100D
	OpGetDevicePropDesc       = 0x1014
	OpGetDevicePropValue      = 0x1015
	OpMoveObject              = 0x1019
	OpCopyObject              = 0x101A
	OpGetPartialObject        = 0x101B
	OpGetObjectPropsSupported = 0x9801
	OpGetObjectPropDesc       = 0x9802
	OpGetObjectPropValue      = 0x9803
	OpSetObjectPropValue      = 0x9804
	OpGetObjectPropList       = 0x9805
	OpSendObjectPropList      = 0x9808
	OpGetObjectReferences     = 0x9810
	OpSetObjectReferences     = 0x9811
	// OpGetPartialObject64 is Android's extension with a 64-bit offset;
	// libmtp (gvfs) reads files in pieces with it.
	OpGetPartialObject64 = 0x95C1
)

// Responses.
const (
	RespOK                      = 0x2001
	RespGeneralError            = 0x2002
	RespSessionNotOpen          = 0x2003
	RespInvalidTransactionID    = 0x2004
	RespOperationNotSupported   = 0x2005
	RespParameterNotSupported   = 0x2006
	RespIncompleteTransfer      = 0x2007
	RespInvalidStorageID        = 0x2008
	RespInvalidObjectHandle     = 0x2009
	RespDevicePropNotSupported  = 0x200A
	RespInvalidObjectFormatCode = 0x200B
	RespStoreFull               = 0x200C
	RespObjectWriteProtected    = 0x200D
	RespStoreReadOnly           = 0x200E
	RespAccessDenied            = 0x200F
	RespNoThumbnailPresent      = 0x2010
	RespNoValidObjectInfo       = 0x2015
	RespDeviceBusy              = 0x2019
	RespInvalidParentObject     = 0x201A
	RespInvalidParameter        = 0x201D
	RespSessionAlreadyOpen      = 0x201E
	RespTransactionCancelled    = 0x201F
	RespInvalidObjectPropCode   = 0xA801
	RespInvalidObjectPropFormat = 0xA802
	RespInvalidObjectReference  = 0xA804
	RespInvalidDataset          = 0xA806
	RespGroupUnsupported        = 0xA807
	RespDepthUnsupported        = 0xA808
	RespObjectTooLarge          = 0xA809
	RespObjectPropNotSupported  = 0xA80A
)

// Events.
const (
	EventObjectAdded        = 0x4002
	EventObjectRemoved      = 0x4003
	EventObjectInfoChanged  = 0x4007
	EventStorageInfoChanged = 0x400C
)

// Object formats.
const (
	FormatUndefined      = 0x3000
	FormatAssociation    = 0x3001
	FormatText           = 0x3004
	FormatHTML           = 0x3005
	FormatAIFF           = 0x3007
	FormatWAV            = 0x3008
	FormatMP3            = 0x3009
	FormatJPEG           = 0x3801
	FormatBMP            = 0x3804
	FormatGIF            = 0x3807
	FormatPNG            = 0x380B
	FormatUndefinedAudio = 0xB900
	FormatOGG            = 0xB902
	FormatAAC            = 0xB903
	FormatFLAC           = 0xB906
	FormatUndefinedVideo = 0xB980
	FormatMP4            = 0xB982
	// FormatAbstractAVPlaylist is a playlist that is only its references
	// (Windows Media Player's and libmtp's, "name.pla").
	FormatAbstractAVPlaylist = 0xBA05
	FormatM3U                = 0xBA11
	FormatPLS                = 0xBA14
)

// Object properties.
const (
	PropStorageID        = 0xDC01
	PropObjectFormat     = 0xDC02
	PropProtectionStatus = 0xDC03
	PropObjectSize       = 0xDC04
	PropObjectFileName   = 0xDC07
	PropDateCreated      = 0xDC08
	PropDateModified     = 0xDC09
	PropParentObject     = 0xDC0B
	PropPersistentUID    = 0xDC41
	PropName             = 0xDC44
)

// DevicePropFriendlyName is what Windows Explorer calls the phone.
const DevicePropFriendlyName = 0xD402

// Data types of properties.
const (
	typeInt8    = 0x0001
	typeUint8   = 0x0002
	typeInt16   = 0x0003
	typeUint16  = 0x0004
	typeInt32   = 0x0005
	typeUint32  = 0x0006
	typeInt64   = 0x0007
	typeUint64  = 0x0008
	typeInt128  = 0x0009
	typeUint128 = 0x000A
	// typeArray marks an array of the type in the low bits (0x4004 an
	// array of UINT16), its length first.
	typeArray  = 0x4000
	typeString = 0xFFFF
)

// typeSize is the bytes of one value of an integer type, 0 for others.
func typeSize(typ uint16) int {
	if typ < typeInt8 || typ > typeUint128 {
		return 0
	}
	return 1 << ((typ - 1) / 2)
}

// Object handles and storage IDs with a meaning of their own.
const (
	allObjects = 0xFFFFFFFF
	rootParent = 0xFFFFFFFF
	allStores  = 0xFFFFFFFF
)

// Class requests on the control endpoint (PTP over USB, §5.2).
const (
	reqCancel          = 0x64
	reqExtendedEvent   = 0x65
	reqDeviceReset     = 0x66
	reqGetDeviceStatus = 0x67
	cancelCode         = 0x4001
)

var respNames = map[uint16]string{
	RespOK: "OK", RespGeneralError: "General_Error", RespSessionNotOpen: "Session_Not_Open",
	RespInvalidTransactionID: "Invalid_TransactionID", RespOperationNotSupported: "Operation_Not_Supported",
	RespParameterNotSupported: "Parameter_Not_Supported", RespIncompleteTransfer: "Incomplete_Transfer",
	RespInvalidStorageID: "Invalid_StorageID", RespInvalidObjectHandle: "Invalid_ObjectHandle",
	RespDevicePropNotSupported: "DeviceProp_Not_Supported", RespInvalidObjectFormatCode: "Invalid_ObjectFormatCode",
	RespStoreFull: "Store_Full", RespObjectWriteProtected: "Object_WriteProtected", RespStoreReadOnly: "Store_Read_Only",
	RespAccessDenied: "Access_Denied", RespNoThumbnailPresent: "No_Thumbnail_Present", RespNoValidObjectInfo: "No_Valid_ObjectInfo",
	RespDeviceBusy: "Device_Busy", RespInvalidParentObject: "Invalid_ParentObject", RespInvalidParameter: "Invalid_Parameter",
	RespSessionAlreadyOpen: "Session_Already_Open", RespTransactionCancelled: "Transaction_Cancelled",
	RespInvalidObjectPropCode: "Invalid_ObjectPropCode", RespInvalidObjectPropFormat: "Invalid_ObjectProp_Format",
	RespInvalidObjectReference: "Invalid_ObjectReference", RespInvalidDataset: "Invalid_Dataset", RespGroupUnsupported: "Specification_By_Group_Unsupported",
	RespDepthUnsupported: "Specification_By_Depth_Unsupported", RespObjectTooLarge: "Object_Too_Large",
	RespObjectPropNotSupported: "ObjectProp_Not_Supported",
}

// RespName is a response code's name in the specification.
func RespName(code uint16) string {
	if n, ok := respNames[code]; ok {
		return n
	}
	return fmt.Sprintf("0x%04x", code)
}

// RespError is a response other than OK.
type RespError struct {
	Op   uint16
	Code uint16
}

func (e *RespError) Error() string { return fmt.Sprintf("mtp: 0x%04x: %s", e.Op, RespName(e.Code)) }

// Code is err's response code, 0 when it is not a RespError.
func Code(err error) uint16 {
	var re *RespError
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}

// container is a command, a response or an event.
type container struct {
	typ    uint16
	code   uint16
	tid    uint32
	params []uint32
}

func (c container) encode() []byte {
	b := make([]byte, headerLen+4*len(c.params))
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	binary.LittleEndian.PutUint16(b[4:], c.typ)
	binary.LittleEndian.PutUint16(b[6:], c.code)
	binary.LittleEndian.PutUint32(b[8:], c.tid)
	for i, p := range c.params {
		binary.LittleEndian.PutUint32(b[headerLen+4*i:], p)
	}
	return b
}

type header struct {
	length uint32
	typ    uint16
	code   uint16
	tid    uint32
}

func parseHeader(b []byte) header {
	return header{binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:]), binary.LittleEndian.Uint32(b[8:])}
}

// enc builds a dataset.
type enc struct{ b []byte }

func (e *enc) u8(v uint8)   { e.b = append(e.b, v) }
func (e *enc) u16(v uint16) { e.b = binary.LittleEndian.AppendUint16(e.b, v) }
func (e *enc) u32(v uint32) { e.b = binary.LittleEndian.AppendUint32(e.b, v) }
func (e *enc) u64(v uint64) { e.b = binary.LittleEndian.AppendUint64(e.b, v) }

func (e *enc) a16(vs []uint16) {
	e.u32(uint32(len(vs)))
	for _, v := range vs {
		e.u16(v)
	}
}

func (e *enc) a32(vs []uint32) {
	e.u32(uint32(len(vs)))
	for _, v := range vs {
		e.u32(v)
	}
}

// maxStrUnits: a PTP string's length byte counts its UTF-16 units and the
// terminating zero.
const maxStrUnits = 254

// str is a PTP string: the number of UTF-16 units with the terminator,
// then the units; the empty string is a single zero.
func (e *enc) str(s string) {
	u := utf16.Encode([]rune(s))
	if len(u) == 0 {
		e.u8(0)
		return
	}
	if len(u) > maxStrUnits {
		u = u[:maxStrUnits]
		if utf16.IsSurrogate(rune(u[len(u)-1])) {
			u = u[:len(u)-1]
		}
	}
	e.u8(uint8(len(u) + 1))
	for _, c := range u {
		e.u16(c)
	}
	e.u16(0)
}

// dec reads a dataset; past its end every value is zero and err is set.
type dec struct {
	b   []byte
	err error
}

var errShort = errors.New("mtp: dataset too short")

func (d *dec) take(n int) []byte {
	if d.err != nil || len(d.b) < n {
		d.err = errShort
		return make([]byte, n)
	}
	v := d.b[:n]
	d.b = d.b[n:]
	return v
}

func (d *dec) u8() uint8   { return d.take(1)[0] }
func (d *dec) u16() uint16 { return binary.LittleEndian.Uint16(d.take(2)) }
func (d *dec) u32() uint32 { return binary.LittleEndian.Uint32(d.take(4)) }
func (d *dec) u64() uint64 { return binary.LittleEndian.Uint64(d.take(8)) }

func (d *dec) str() string {
	n := int(d.u8())
	if n == 0 {
		return ""
	}
	u := make([]uint16, n)
	for i := range u {
		u[i] = d.u16()
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u))
}

func (d *dec) a16() []uint16 {
	n := d.u32()
	if d.err != nil || int64(n)*2 > int64(len(d.b)) {
		d.err = errShort
		return nil
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = d.u16()
	}
	return out
}

func (d *dec) a32() []uint32 {
	n := d.u32()
	if d.err != nil || int64(n)*4 > int64(len(d.b)) {
		d.err = errShort
		return nil
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = d.u32()
	}
	return out
}

const dateLayout = "20060102T150405"

func dateString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(dateLayout)
}

// ObjectInfo is the ObjectInfo dataset (GetObjectInfo, SendObjectInfo),
// without the image fields, which are zero here. ThumbFormat is 0 when
// the object has no thumbnail (GetThumb).
type ObjectInfo struct {
	StorageID           uint32
	Format              uint16
	Protection          uint16
	CompressedSize      uint32 // 0xFFFFFFFF: 4 GiB or more
	ThumbFormat         uint16
	ThumbCompressedSize uint32
	ThumbPixWidth       uint32
	ThumbPixHeight      uint32
	Parent              uint32 // 0: in the storage's root
	AssociationType     uint16 // 1 for a folder
	Filename            string
	Created             string
	Modified            string
}

func (o ObjectInfo) encode() []byte {
	var e enc
	e.u32(o.StorageID)
	e.u16(o.Format)
	e.u16(o.Protection)
	e.u32(o.CompressedSize)
	e.u16(o.ThumbFormat)
	e.u32(o.ThumbCompressedSize)
	e.u32(o.ThumbPixWidth)
	e.u32(o.ThumbPixHeight)
	for range 3 {
		e.u32(0) // image width, height, depth
	}
	e.u32(o.Parent)
	e.u16(o.AssociationType)
	e.u32(0) // association description
	e.u32(0) // sequence number
	e.str(o.Filename)
	e.str(o.Created)
	e.str(o.Modified)
	e.str("") // keywords
	return e.b
}

func parseObjectInfo(b []byte) (ObjectInfo, error) {
	d := dec{b: b}
	var o ObjectInfo
	o.StorageID = d.u32()
	o.Format = d.u16()
	o.Protection = d.u16()
	o.CompressedSize = d.u32()
	o.ThumbFormat = d.u16()
	o.ThumbCompressedSize = d.u32()
	o.ThumbPixWidth = d.u32()
	o.ThumbPixHeight = d.u32()
	for range 3 {
		d.u32()
	}
	o.Parent = d.u32()
	o.AssociationType = d.u16()
	d.u32()
	d.u32()
	o.Filename = d.str()
	if d.err != nil {
		return o, d.err
	}
	// Some initiators end the dataset after the name.
	if len(d.b) > 0 {
		o.Created = d.str()
		o.Modified = d.str()
	}
	return o, nil
}

// value reads one property value: an integer as uint64 (a signed one as
// its bits), a 128-bit one as its 16 bytes, an array of integers as
// []uint64 (of 128-bit ones as their bytes end to end), a string as a
// string.
func value(d *dec, typ uint16) any {
	if typ == typeString {
		return d.str()
	}
	elem := typ &^ typeArray
	size := typeSize(elem)
	switch {
	case size == 0:
		d.err = fmt.Errorf("mtp: property type 0x%04x", typ)
		return nil
	case typ == elem && size == 16:
		return append([]byte(nil), d.take(16)...)
	case typ == elem:
		return d.uint(size)
	}
	n := d.u32()
	if d.err != nil || int64(n)*int64(size) > int64(len(d.b)) {
		d.err = errShort
		return nil
	}
	if size == 16 {
		return append([]byte(nil), d.take(int(n)*16)...)
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = d.uint(size)
	}
	return out
}

func (d *dec) uint(size int) uint64 {
	var b [8]byte
	copy(b[:], d.take(size))
	return binary.LittleEndian.Uint64(b[:])
}

// putValue writes a value as value reads it; nil or a value of another Go
// type is written as the type's zero.
func putValue(e *enc, typ uint16, v any) {
	if typ == typeString {
		s, _ := v.(string)
		e.str(s)
		return
	}
	elem := typ &^ typeArray
	size := typeSize(elem)
	switch {
	case size == 0:
		return
	case typ == elem && size == 16:
		var b [16]byte
		v128, _ := v.([]byte)
		copy(b[:], v128)
		e.b = append(e.b, b[:]...)
		return
	case typ == elem:
		n, _ := v.(uint64)
		e.uint(size, n)
		return
	}
	if size == 16 {
		b, _ := v.([]byte)
		b = b[:len(b)/16*16]
		e.u32(uint32(len(b) / 16))
		e.b = append(e.b, b...)
		return
	}
	ns, _ := v.([]uint64)
	e.u32(uint32(len(ns)))
	for _, n := range ns {
		e.uint(size, n)
	}
}

func (e *enc) uint(size int, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	e.b = append(e.b, b[:size]...)
}

// PropEntry is one element of an object property list.
type PropEntry struct {
	Handle uint32
	Code   uint16
	Type   uint16
	Value  any
}

// parsePropList reads an ObjectPropList dataset: GetObjectPropList's
// answer, or what SendObjectPropList sends (with handles 0).
func parsePropList(b []byte) ([]PropEntry, error) {
	d := dec{b: b}
	n := d.u32()
	out := []PropEntry{}
	for j := uint32(0); j < n && d.err == nil; j++ {
		e := PropEntry{Handle: d.u32(), Code: d.u16(), Type: d.u16()}
		e.Value = value(&d, e.Type)
		if d.err == nil {
			out = append(out, e)
		}
	}
	return out, d.err
}

func encodePropList(list []PropEntry) []byte {
	var e enc
	e.u32(uint32(len(list)))
	for _, p := range list {
		e.u32(p.Handle)
		e.u16(p.Code)
		e.u16(p.Type)
		putValue(&e, p.Type, p.Value)
	}
	return e.b
}

// DeviceInfo is the GetDeviceInfo dataset, as far as an initiator cares.
type DeviceInfo struct {
	VendorExtension string
	Operations      []uint16
	Events          []uint16
	DeviceProps     []uint16
	Formats         []uint16
	Manufacturer    string
	Model           string
	Version         string
	Serial          string
}

// StorageInfo is the GetStorageInfo dataset.
type StorageInfo struct {
	Type        uint16
	Filesystem  uint16
	Access      uint16
	Capacity    uint64
	Free        uint64
	Description string
	Volume      string
}
