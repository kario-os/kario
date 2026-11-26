package mtp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Packet is the bulk endpoints' packet size at high speed (USB 2, the
// phone's port). A transfer is a run of full packets ended by a shorter
// one, a zero-length packet when its length is a multiple of this.
const Packet = 512

// chunk is how much goes to the endpoint at a time: a multiple of the
// packet size, so only a transfer's last write is short.
const chunk = 64 << 10

// The helper's environment when the Shell starts it for MTP: EnvMode is
// ModeMTP, EnvTransport says whose files it holds (FunctionFS's, or a
// simulated cable's), EnvPacket the bulk packet size, EnvDevice the
// DeviceInfo naming the phone (JSON).
const (
	EnvMode      = "PHONE_USB_MODE"
	ModeMTP      = "mtp"
	EnvTransport = "PHONE_USB_TRANSPORT"
	TransportFFS = "ffs"
	TransportSim = "sim"
	EnvPacket    = "PHONE_USB_PACKET"
	EnvDevice    = "PHONE_USB_DEVICE"
)

// Transport is the device's side of the function's endpoints.
type Transport struct {
	// In and Intr carry transfers to the computer: a Write sends its bytes
	// as packets, a Write of nothing a zero-length packet.
	In, Intr io.Writer
	// Out carries transfers from the computer: a Read returns once p is
	// full or a short packet ended the transfer (0 bytes: a zero-length
	// packet).
	Out io.Reader
	// Ctl is the control endpoint (nil: none).
	Ctl Control
	// Packet is the bulk packet size (Packet when 0).
	Packet int
}

// Setup is a control request (USB 2.0 §9.3).
type Setup struct {
	RequestType, Request uint8
	Value, Index, Length uint16
}

func (s Setup) in() bool { return s.RequestType&0x80 != 0 }

// Event kinds on the control endpoint, FunctionFS's.
const (
	EvBind    = 0
	EvUnbind  = 1
	EvEnable  = 2
	EvDisable = 3
	EvSetup   = 4
	EvSuspend = 5
	EvResume  = 6
)

const eventLen = 12

// CtlEvent is what the control endpoint reports: the function enabled or
// disabled by the computer, or a request to it.
type CtlEvent struct {
	Kind  uint8
	Setup Setup
}

func parseEvent(b []byte) CtlEvent {
	return CtlEvent{Kind: b[8], Setup: Setup{b[0], b[1], binary.LittleEndian.Uint16(b[2:]), binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:])}}
}

func (e CtlEvent) encode() []byte {
	b := make([]byte, eventLen)
	b[0], b[1] = e.Setup.RequestType, e.Setup.Request
	binary.LittleEndian.PutUint16(b[2:], e.Setup.Value)
	binary.LittleEndian.PutUint16(b[4:], e.Setup.Index)
	binary.LittleEndian.PutUint16(b[6:], e.Setup.Length)
	b[8] = e.Kind
	return b
}

// Control is the device's control endpoint: each request Next gives is
// answered once, with Reply (data to the computer), Data (data from it,
// none for a bare acknowledgement) or Stall.
type Control interface {
	Next() (CtlEvent, error)
	Reply(b []byte) error
	Data(n int) ([]byte, error)
	Stall(s Setup) error
}

// --- FunctionFS ---

// endpoint is a FunctionFS endpoint file: each read and write one request
// to the controller, which is what Transport asks of In, Intr and Out.
// The files are not pollable, so they are used with plain blocking calls;
// a zero-length write is a zero-length packet, which os.File may skip.
type endpoint struct{ f *os.File }

func retry(f func() (int, error)) (int, error) {
	for {
		n, err := f()
		if !errors.Is(err, unix.EINTR) {
			return n, err
		}
	}
}

func (e endpoint) Read(p []byte) (int, error) {
	n, err := retry(func() (int, error) { return unix.Read(int(e.f.Fd()), p) })
	return max(n, 0), err
}

func (e endpoint) Write(p []byte) (int, error) {
	n, err := retry(func() (int, error) { return unix.Write(int(e.f.Fd()), p) })
	return max(n, 0), err
}

// ffsControl is FunctionFS's ep0: events are read from it; a request's
// data stage is a read (from the computer) or write (to it), and the
// other direction stalls it.
type ffsControl struct {
	ep    endpoint
	queue []CtlEvent
}

func (c *ffsControl) Next() (CtlEvent, error) {
	for len(c.queue) == 0 {
		buf := make([]byte, eventLen*8)
		n, err := c.ep.Read(buf)
		if err != nil {
			return CtlEvent{}, err
		}
		for i := 0; i+eventLen <= n; i += eventLen {
			c.queue = append(c.queue, parseEvent(buf[i:]))
		}
	}
	e := c.queue[0]
	c.queue = c.queue[1:]
	return e, nil
}

func (c *ffsControl) Reply(b []byte) error {
	_, err := c.ep.Write(b)
	return err
}

func (c *ffsControl) Data(n int) ([]byte, error) {
	b := make([]byte, n)
	got, err := c.ep.Read(b)
	return b[:got], err
}

func (c *ffsControl) Stall(s Setup) error {
	var err error
	if s.in() {
		_, err = c.ep.Read(nil)
	} else {
		_, err = c.ep.Write(nil)
	}
	if errors.Is(err, unix.EL2HLT) {
		return nil
	}
	return err
}

// FFS is the transport over a FunctionFS function's files: ep0, then
// the bulk-in, bulk-out and interrupt endpoints, in the order the
// descriptors list them (hal/usbgadget).
func FFS(ep0, in, out, intr *os.File, packet int) Transport {
	return Transport{In: endpoint{in}, Out: endpoint{out}, Intr: endpoint{intr}, Ctl: &ffsControl{ep: endpoint{ep0}}, Packet: packet}
}

// Shutdown: the computer let go of the function (FunctionFS's ESHUTDOWN):
// it is unconfigured, or the cable is out; the next read waits for it.
func Shutdown(err error) bool { return errors.Is(err, unix.ESHUTDOWN) }

// --- A simulated cable ---

// The simulated cable (tests, and the developer machine's selftest) keeps
// USB's framing on byte streams: each packet is a little-endian uint16
// length and its bytes, so short and zero-length packets end transfers as
// on the wire. The control endpoint is a socket: FunctionFS's 12-byte
// events one way (a request's data from the computer after it), and one
// answer per request the other: a byte (0 done, 1 stalled), a uint16
// length and the data.

type framedWriter struct {
	w      io.Writer
	packet int
	buf    []byte
}

func (f *framedWriter) Write(p []byte) (int, error) {
	buf := f.buf[:0]
	if len(p) == 0 {
		buf = append(buf, 0, 0)
	}
	for off := 0; off < len(p); off += f.packet {
		n := min(f.packet, len(p)-off)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(n))
		buf = append(buf, p[off:off+n]...)
	}
	f.buf = buf
	if _, err := f.w.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

type framedReader struct {
	r      *bufio.Reader
	packet int
	// rest is what of a packet did not fit the last Read; restShort: that
	// packet ended its transfer.
	rest      []byte
	restShort bool
}

var errBabble = errors.New("mtp: a packet longer than the packet size")

func (f *framedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := 0
	if len(f.rest) > 0 {
		n = copy(p, f.rest)
		f.rest = f.rest[n:]
		if len(f.rest) > 0 || f.restShort {
			return n, nil
		}
	}
	for n < len(p) {
		var h [2]byte
		if _, err := io.ReadFull(f.r, h[:]); err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		l := int(binary.LittleEndian.Uint16(h[:]))
		if l > f.packet {
			return n, errBabble
		}
		room := p[n:]
		if l > len(room) {
			pkt := make([]byte, l)
			if _, err := io.ReadFull(f.r, pkt); err != nil {
				return n, io.ErrUnexpectedEOF
			}
			n += copy(room, pkt)
			f.rest, f.restShort = pkt[len(room):], l < f.packet
			return n, nil
		}
		if _, err := io.ReadFull(f.r, room[:l]); err != nil {
			return n, io.ErrUnexpectedEOF
		}
		n += l
		if l < f.packet {
			return n, nil
		}
	}
	return n, nil
}

type simControl struct {
	conn io.ReadWriter
	rd   *bufio.Reader
	// dataLeft: the OUT data of the last request not yet read.
	dataLeft int
}

func (c *simControl) Next() (CtlEvent, error) {
	if c.dataLeft > 0 {
		if _, err := io.CopyN(io.Discard, c.rd, int64(c.dataLeft)); err != nil {
			return CtlEvent{}, err
		}
		c.dataLeft = 0
	}
	b := make([]byte, eventLen)
	if _, err := io.ReadFull(c.rd, b); err != nil {
		return CtlEvent{}, err
	}
	e := parseEvent(b)
	if e.Kind == EvSetup && !e.Setup.in() {
		c.dataLeft = int(e.Setup.Length)
	}
	return e, nil
}

func (c *simControl) answer(kind byte, data []byte) error {
	b := append([]byte{kind}, binary.LittleEndian.AppendUint16(nil, uint16(len(data)))...)
	_, err := c.conn.Write(append(b, data...))
	return err
}

func (c *simControl) Reply(b []byte) error { return c.answer(0, b) }

func (c *simControl) Data(n int) ([]byte, error) {
	n = min(n, c.dataLeft)
	b := make([]byte, n)
	if _, err := io.ReadFull(c.rd, b); err != nil {
		return nil, err
	}
	c.dataLeft -= n
	return b, c.answer(0, nil)
}

func (c *simControl) Stall(Setup) error { return c.answer(1, nil) }

// SimFiles are the device's ends of a simulated cable, to hand to the
// helper: the control endpoint (a socket), then pipes for bulk-in,
// bulk-out and interrupt.
type SimFiles struct{ EP0, In, Out, Intr *os.File }

// List is the files in the order FFS and Sim take them.
func (s SimFiles) List() []*os.File { return []*os.File{s.EP0, s.In, s.Out, s.Intr} }

func (s SimFiles) Close() {
	for _, f := range s.List() {
		if f != nil {
			f.Close()
		}
	}
}

// Sim is the transport over a simulated cable's device ends.
func Sim(f SimFiles, packet int) Transport {
	return Transport{
		In: &framedWriter{w: f.In, packet: packet}, Intr: &framedWriter{w: f.Intr, packet: packet},
		Out: &framedReader{r: bufio.NewReaderSize(f.Out, chunk), packet: packet},
		Ctl: &simControl{conn: f.EP0, rd: bufio.NewReader(f.EP0)}, Packet: packet,
	}
}

// NewSim makes a simulated cable: the device's ends and the computer's
// transport, whose Close unplugs it (the device's reads end).
func NewSim(packet int) (SimFiles, *HostTransport, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return SimFiles{}, nil, err
	}
	dev := SimFiles{EP0: os.NewFile(uintptr(fds[0]), "mtp-ep0")}
	hostCtl := os.NewFile(uintptr(fds[1]), "mtp-host-ep0")
	var host []*os.File
	pipe := func(toDevice bool) (*os.File, error) {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		if toDevice {
			host = append(host, w)
			return r, nil
		}
		host = append(host, r)
		return w, nil
	}
	fail := func(err error) (SimFiles, *HostTransport, error) {
		dev.Close()
		hostCtl.Close()
		for _, f := range host {
			f.Close()
		}
		return SimFiles{}, nil, err
	}
	if dev.In, err = pipe(false); err != nil {
		return fail(err)
	}
	if dev.Out, err = pipe(true); err != nil {
		return fail(err)
	}
	if dev.Intr, err = pipe(false); err != nil {
		return fail(err)
	}
	in, out, intr := host[0], host[1], host[2]
	ht := &HostTransport{
		In: &framedReader{r: bufio.NewReaderSize(in, chunk), packet: packet}, Out: &framedWriter{w: out, packet: packet},
		Intr: &framedReader{r: bufio.NewReader(intr), packet: packet},
		Ctl:  &simHostControl{conn: hostCtl, rd: bufio.NewReader(hostCtl)}, Packet: packet,
		Close: func() error {
			for _, f := range []*os.File{hostCtl, in, out, intr} {
				f.Close()
			}
			return nil
		},
	}
	return dev, ht, nil
}

// MemSim is a simulated cable inside one process: the device's transport
// and the computer's.
func MemSim(packet int) (Transport, *HostTransport, error) {
	dev, host, err := NewSim(packet)
	if err != nil {
		return Transport{}, nil, err
	}
	t := Sim(dev, packet)
	close := host.Close
	host.Close = func() error {
		close()
		dev.Close()
		return nil
	}
	return t, host, nil
}

// --- The computer's side ---

// HostTransport is the computer's side of the function.
type HostTransport struct {
	Out    io.Writer // bulk-out: Write sends packets, nothing a zero-length one
	In     io.Reader // bulk-in: Read ends at a short packet
	Intr   io.Reader // interrupt (nil: no events)
	Ctl    HostControl
	Packet int
	Close  func() error
}

// HostControl makes control requests: data goes out with an OUT request,
// an IN request returns what the device answered.
type HostControl interface {
	Control(s Setup, data []byte) ([]byte, error)
}

// ErrStall: the device stalled the request.
var ErrStall = errors.New("mtp: the request was stalled")

type simHostControl struct {
	conn io.ReadWriter
	rd   *bufio.Reader
}

func (c *simHostControl) Control(s Setup, data []byte) ([]byte, error) {
	if !s.in() {
		s.Length = uint16(len(data))
	}
	msg := CtlEvent{Kind: EvSetup, Setup: s}.encode()
	if !s.in() {
		msg = append(msg, data...)
	}
	if _, err := c.conn.Write(msg); err != nil {
		return nil, err
	}
	var h [3]byte
	if _, err := io.ReadFull(c.rd, h[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.LittleEndian.Uint16(h[1:]))
	if _, err := io.ReadFull(c.rd, b); err != nil {
		return nil, err
	}
	if h[0] == 1 {
		return nil, ErrStall
	}
	return b, nil
}

// Enable tells a simulated device the computer configured it.
func (c *simHostControl) Enable() error {
	_, err := c.conn.Write(CtlEvent{Kind: EvEnable}.encode())
	return err
}

// --- Containers on the bulk endpoints, both sides ---

// packetWriter sends one container: in chunks of whole packets, its end
// short or, when it is a multiple of the packet size, followed by a
// zero-length packet. stop, when it says so, abandons it.
type packetWriter struct {
	w      io.Writer
	packet int
	buf    []byte
	total  int64
	stop   func() bool
	err    error
}

var errCancelled = errors.New("mtp: cancelled")

func (p *packetWriter) Write(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	p.buf = append(p.buf, b...)
	p.total += int64(len(b))
	for len(p.buf) >= chunk {
		if p.stop != nil && p.stop() {
			p.err = errCancelled
			return 0, p.err
		}
		if _, err := p.w.Write(p.buf[:chunk]); err != nil {
			p.err = err
			return 0, err
		}
		p.buf = append(p.buf[:0], p.buf[chunk:]...)
	}
	return len(b), nil
}

func (p *packetWriter) Close() error {
	if p.err != nil {
		return p.err
	}
	if len(p.buf) > 0 {
		if _, err := p.w.Write(p.buf); err != nil {
			return err
		}
	}
	if p.total%int64(p.packet) == 0 {
		if _, err := p.w.Write(nil); err != nil {
			return err
		}
	}
	return nil
}

// bulkReader takes containers from transfers. A container's header is read
// a packet at a time (so a read never waits past a transfer's end for
// bytes that are not coming), its payload in reads no longer than what is
// left of it; what one read brought past a container (a stream without
// packets) starts the next.
type bulkReader struct {
	r      io.Reader
	packet int
	buf    []byte
	// short: the transfer the bytes in buf came from has ended.
	short bool
	// headerOnly: the header came alone in its transfer.
	headerOnly bool
}

func (b *bulkReader) header() (header, error) {
	for len(b.buf) < headerLen {
		tmp := make([]byte, b.packet)
		n, err := b.r.Read(tmp)
		if n == 0 && err == nil {
			continue // a zero-length packet ending the last transfer
		}
		b.buf = append(b.buf, tmp[:n]...)
		b.short = n < len(tmp)
		if err != nil && len(b.buf) < headerLen {
			return header{}, err
		}
	}
	h := parseHeader(b.buf)
	b.headerOnly = len(b.buf) == headerLen && b.short
	b.buf = b.buf[headerLen:]
	return h, nil
}

// unknownLength: a data container of 4 GiB or more, which ends with its
// transfer.
const unknownLength = 0xFFFFFFFF

// payload is the rest of the container whose header was just read.
func (b *bulkReader) payload(h header) *payload {
	p := &payload{b: b, left: int64(h.length) - headerLen}
	if h.length == unknownLength {
		p.left = -1
		p.done = b.short && !b.headerOnly && len(b.buf) == 0
	}
	if h.length < headerLen {
		p.left = 0
	}
	return p
}

type payload struct {
	b    *bulkReader
	left int64 // -1: up to the end of the transfer
	done bool
	n    int64
}

// errTruncated: the transfer ended before the container did.
var errTruncated = errors.New("mtp: the transfer ended inside a container")

func (p *payload) Read(dst []byte) (int, error) {
	if p.left == 0 || p.done || len(dst) == 0 {
		if p.left == 0 || p.done {
			return 0, io.EOF
		}
		return 0, nil
	}
	if p.left > 0 && int64(len(dst)) > p.left {
		dst = dst[:p.left]
	}
	if len(p.b.buf) > 0 {
		n := copy(dst, p.b.buf)
		p.b.buf = p.b.buf[n:]
		p.account(n)
		if p.left < 0 && len(p.b.buf) == 0 && p.b.short {
			p.done = true
		}
		return n, nil
	}
	// The endpoint is read in whole packets: FunctionFS rounds a request up
	// to them and older kernels fail one that brings more than the read
	// holds (EOVERFLOW); what a packet brings past dst waits in buf.
	if len(dst) < p.b.packet {
		tmp := make([]byte, p.b.packet)
		n, err := p.b.r.Read(tmp)
		c := copy(dst, tmp[:n])
		p.b.buf, p.b.short = tmp[c:n], n < len(tmp)
		p.account(c)
		switch {
		case err != nil:
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return c, err
		case p.left < 0:
			p.done = p.b.short && len(p.b.buf) == 0
		case n == 0:
			return 0, errTruncated
		}
		return c, nil
	}
	dst = dst[:len(dst)/p.b.packet*p.b.packet]
	n, err := p.b.r.Read(dst)
	p.account(n)
	switch {
	case err != nil:
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return n, err
	case p.left < 0 && n < len(dst):
		p.done = true
	case n == 0:
		return 0, errTruncated
	}
	return n, nil
}

func (p *payload) account(n int) {
	p.n += int64(n)
	if p.left > 0 {
		p.left -= int64(n)
	}
}

// drain reads what is left of the container.
func (p *payload) drain() error {
	_, err := io.Copy(io.Discard, p)
	return err
}

// all reads a small container's payload whole.
func (p *payload) all(limit int64) ([]byte, error) {
	if p.left > limit {
		p.drain()
		return nil, fmt.Errorf("mtp: a dataset of %d bytes", p.left)
	}
	return io.ReadAll(io.LimitReader(p, limit))
}
