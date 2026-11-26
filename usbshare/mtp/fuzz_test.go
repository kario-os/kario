package mtp

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path"
	"reflect"
	"testing"
	"time"

	"github.com/kario-os/kario/usbshare"
)

// fuzzLib is the tests' library without files on disk, so that a fuzzed
// session costs no file system calls.
type fuzzLib struct{ *memLib }

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (l fuzzLib) Open(folder, name string) (io.ReadSeekCloser, usbshare.Item, error) {
	b, ok := l.get(folder + "/" + name)
	if !ok {
		return nil, usbshare.Item{}, os.ErrNotExist
	}
	return nopCloser{bytes.NewReader(b)}, usbshare.Item{Name: path.Base(name), Size: int64(len(b))}, nil
}

func newFuzzLib() fuzzLib {
	return fuzzLib{&memLib{files: map[string][]byte{
		"photos/beach.jpg":    bytes.Repeat([]byte("J"), 1000),
		"music/Album/01.flac": []byte("flac"),
		"files/notes.txt":     []byte("0123456789abcdef"),
		"files/big.bin":       bytes.Repeat([]byte("0123456789"), 3000),
	}, dirs: map[string]bool{"music/Album": true}}}
}

// hostStream writes what a computer sends on bulk-out, framed as the
// simulated cable frames packets.
type hostStream struct {
	buf    bytes.Buffer
	packet int
	tid    uint32
}

func (s *hostStream) out() *packetWriter {
	return &packetWriter{w: &framedWriter{w: &s.buf, packet: s.packet}, packet: s.packet}
}

func (s *hostStream) cmd(op uint16, params ...uint32) *hostStream {
	s.tid++
	w := s.out()
	w.Write(container{typ: typeCommand, code: op, tid: s.tid, params: params}.encode())
	w.Close()
	return s
}

func (s *hostStream) data(op uint16, b []byte) *hostStream {
	w := s.out()
	hdr := make([]byte, headerLen)
	binary.LittleEndian.PutUint32(hdr, uint32(len(b)+headerLen))
	binary.LittleEndian.PutUint16(hdr[4:], typeData)
	binary.LittleEndian.PutUint16(hdr[6:], op)
	binary.LittleEndian.PutUint32(hdr[8:], s.tid)
	w.Write(hdr)
	w.Write(b)
	w.Close()
	return s
}

// fuzzPackets are the packet sizes a fuzzed session picks from (its first
// byte): full and low speed, and one smaller than a container's header.
var fuzzPackets = []int{512, 64, 8}

// rawPackets is bulk-out as one long transfer: every read is filled.
type rawPackets struct{ b []byte }

func (r *rawPackets) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

const storeMusic, storeFiles = 0x00030001, 0x00040001

func sessionSeeds(packet int) [][]byte {
	name := func(n string) []byte { var e enc; e.str(n); return e.b }
	put := ObjectInfo{StorageID: storeFiles, Format: FormatText, CompressedSize: 5, Filename: "new.txt"}.encode()
	dir := ObjectInfo{StorageID: storeMusic, Format: FormatAssociation, AssociationType: 1, Filename: "Live"}.encode()
	var out [][]byte
	for _, s := range []*hostStream{
		(&hostStream{packet: packet}).cmd(OpGetDeviceInfo).cmd(OpOpenSession, 1).cmd(OpGetStorageIDs).
			cmd(OpGetStorageInfo, storeMusic).cmd(OpGetObjectHandles, allStores, 0, 0).cmd(OpGetObjectInfo, 1).
			cmd(OpGetObject, 2).cmd(OpGetPartialObject, 3, 4, 8).cmd(OpGetPartialObject64, 4, 10, 0, 100).cmd(OpCloseSession),
		(&hostStream{packet: packet}).cmd(OpOpenSession, 1).cmd(OpSendObjectInfo, storeFiles, rootParent).data(OpSendObjectInfo, put).
			cmd(OpSetObjectPropValue, 1, PropObjectFileName).data(OpSetObjectPropValue, name("renamed.txt")).
			cmd(OpSendObject).data(OpSendObject, []byte("hello")),
		(&hostStream{packet: packet}).cmd(OpOpenSession, 1).cmd(OpGetObjectHandles, storeMusic, 0, rootParent).
			cmd(OpSendObjectInfo, storeMusic, 1).data(OpSendObjectInfo, dir).cmd(OpGetObjectPropList, 1, 0, 0xFFFFFFFF, 0, 1).
			cmd(OpGetObjectPropList, allObjects, 0, PropObjectFileName, 0, 0xFFFFFFFF).cmd(OpCopyObject, 2, storeFiles, 0).
			cmd(OpDeleteObject, 2, 0).cmd(OpMoveObject, 2, storeFiles, 0).cmd(OpGetObjectPropValue, 2, PropPersistentUID).
			cmd(OpGetObjectPropDesc, PropDateModified, 0).cmd(OpGetDevicePropDesc, DevicePropFriendlyName),
	} {
		out = append(out, s.buf.Bytes())
	}
	return out
}

// FuzzResponder is a computer on the cable: whatever it sends on bulk-out
// must end in answers or refusals, never a panic or a responder that does
// not stop when the cable does.
func FuzzResponder(f *testing.F) {
	for i, p := range fuzzPackets {
		for _, s := range sessionSeeds(p) {
			f.Add(append([]byte{byte(i)}, s...))
		}
	}
	f.Add([]byte{0x80, 16, 0, 0, 0, 1, 0, 0x02, 0x10, 1, 0, 0, 0, 1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 || len(data) > 1<<16 {
			return
		}
		packet := fuzzPackets[int(data[0]&0x7f)%len(fuzzPackets)]
		var out io.Reader = &framedReader{r: bufio.NewReader(bytes.NewReader(data[1:])), packet: packet}
		if data[0]&0x80 != 0 {
			out = &rawPackets{b: data[1:]}
		}
		r := NewResponder(Config{Lib: newFuzzLib(), Info: DeviceInfo{Model: "Kario phone"}})
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Serve(Transport{In: io.Discard, Out: out, Packet: packet})
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the responder did not stop at the end of the cable's data")
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.session != 0 || r.pending != nil || r.open.f != nil || len(r.watched) > maxWatched {
			t.Fatalf("after the cable: session %d, pending %v, open %v, %d watched", r.session, r.pending, r.open.f, len(r.watched))
		}
	})
}

// scriptedControl plays control requests from bytes: FunctionFS's 12-byte
// events, an OUT request's data after it.
type scriptedControl struct {
	b       []byte
	replies int
}

func (c *scriptedControl) Next() (CtlEvent, error) {
	if len(c.b) < eventLen {
		return CtlEvent{}, io.EOF
	}
	e := parseEvent(c.b)
	c.b = c.b[eventLen:]
	return e, nil
}

func (c *scriptedControl) Reply(b []byte) error {
	c.replies++
	return nil
}

func (c *scriptedControl) Data(n int) ([]byte, error) {
	n = min(n, len(c.b))
	b := c.b[:n]
	c.b = c.b[n:]
	return b, nil
}

func (c *scriptedControl) Stall(Setup) error { return nil }

// FuzzControl is a computer's class requests on the control endpoint.
func FuzzControl(f *testing.F) {
	cancel := CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0x21, Request: reqCancel, Length: 6}}.encode()
	f.Add(append(cancel, 0x01, 0x40, 7, 0, 0, 0))
	f.Add(CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0xA1, Request: reqGetDeviceStatus, Length: 4}}.encode())
	f.Add(append(CtlEvent{Kind: EvSetup, Setup: Setup{RequestType: 0x21, Request: reqDeviceReset}}.encode(), CtlEvent{Kind: EvDisable}.encode()...))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewResponder(Config{Lib: newFuzzLib()})
		r.t = Transport{Ctl: &scriptedControl{b: data}}
		r.control()
	})
}

// FuzzObjectInfo is the dataset a computer announces a file or a folder
// with. What parses encodes to a dataset that parses the same, once the
// names are as long as PTP strings may be.
func FuzzObjectInfo(f *testing.F) {
	f.Add(ObjectInfo{StorageID: storeFiles, Format: FormatText, CompressedSize: 5, Filename: "new.txt", Created: "20260101T000000"}.encode())
	f.Add(ObjectInfo{StorageID: storeMusic, Format: FormatAssociation, AssociationType: 1, Filename: "\u97f3\u4e50"}.encode())
	f.Add(ObjectInfo{Filename: "x"}.encode()[:53])
	f.Fuzz(func(t *testing.T, data []byte) {
		o, err := parseObjectInfo(data)
		if err != nil {
			return
		}
		once, err := parseObjectInfo(o.encode())
		if err != nil {
			t.Fatalf("%+v encodes to a dataset that does not parse: %v", o, err)
		}
		twice, err := parseObjectInfo(once.encode())
		if err != nil || !reflect.DeepEqual(once, twice) {
			t.Fatalf("round trip: %+v, then %+v (%v)", once, twice, err)
		}
	})
}
