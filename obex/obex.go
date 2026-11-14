// Package obex is OBEX, the object exchange the Bluetooth file and phone
// book profiles run on (GOEP), with two of them: Object Push (OPP), which
// sends and receives files, and the server side of Phone Book Access
// (PBAP), which a car kit reads the contacts and the call log through.
// BlueZ hands the shell connected sockets (its Profile1 API), RFCOMM or
// L2CAP, so no obexd runs: BlueZ's obexd needs libical, and libical ICU,
// 39 MB in the image. Single Response Mode (GOEP 2.0) and OBEX
// authentication are not implemented; a peer asking for SRM gets the
// classic exchange, one response per request.
package obex

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// Request opcodes; final marks the last packet of a request.
const (
	opConnect    = 0x80
	opDisconnect = 0x81
	opPut        = 0x02
	opGet        = 0x03
	opSetPath    = 0x85
	opAbort      = 0xff
	final        = 0x80
)

// Response codes (with the final bit).
const (
	Continue       = 0x90
	Success        = 0xa0
	BadRequest     = 0xc0
	Forbidden      = 0xc3
	NotFound       = 0xc4
	NotAcceptable  = 0xc6
	EntityTooLarge = 0xcd
	InternalError  = 0xd0
	NotImplemented = 0xd1
)

// Header IDs; the top two bits are the encoding: UTF-16 text, bytes, one
// byte, four bytes.
const (
	hName      = 0x01
	hType      = 0x42
	hLength    = 0xc3
	hTarget    = 0x46
	hBody      = 0x48
	hEndOfBody = 0x49
	hWho       = 0x4a
	hConnID    = 0xcb
	hAppParams = 0x4c
)

// MaxPacket is the largest packet this side takes (and offers in CONNECT).
const MaxPacket = 0x7fff

// minPacket is the smallest a peer may offer (OBEX 1.5: 255).
const minPacket = 255

// ResponseError is a peer's answer other than success.
type ResponseError byte

func (e ResponseError) Error() string {
	name := map[ResponseError]string{BadRequest: "bad request", Forbidden: "forbidden", NotFound: "not found",
		NotAcceptable: "not acceptable", EntityTooLarge: "too large", InternalError: "internal error", NotImplemented: "not implemented"}[e]
	if name == "" {
		name = "failed"
	}
	return fmt.Sprintf("obex: %s (0x%02x)", name, byte(e))
}

type header struct {
	id   byte
	data []byte // the bytes of a text or byte-sequence header
	n    uint32 // a one- or four-byte header's value
}

type packet struct {
	code    byte
	prefix  []byte // CONNECT's version, flags and size; SETPATH's flags
	headers []header
}

func (p *packet) get(id byte) *header {
	for i := range p.headers {
		if p.headers[i].id == id {
			return &p.headers[i]
		}
	}
	return nil
}

func (p *packet) text(id byte) (string, bool) {
	h := p.get(id)
	if h == nil {
		return "", false
	}
	return decodeText(h.data), true
}

func (p *packet) bytes(id byte) []byte {
	if h := p.get(id); h != nil {
		return h.data
	}
	return nil
}

func textHeader(id byte, s string) header {
	if s == "" {
		return header{id: id}
	}
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(u)+2)
	for _, c := range u {
		b = binary.BigEndian.AppendUint16(b, c)
	}
	return header{id: id, data: append(b, 0, 0)}
}

func decodeText(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.BigEndian.Uint16(b[i:]))
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u))
}

// typeHeader is a Type: ASCII with a terminating NUL.
func typeHeader(t string) header { return header{id: hType, data: append([]byte(t), 0)} }

func typeOf(p *packet) string {
	b := p.bytes(hType)
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return string(b)
}

func (h header) size() int {
	switch h.id >> 6 {
	case 2:
		return 2
	case 3:
		return 5
	}
	return 3 + len(h.data)
}

func (p *packet) encode() []byte {
	b := []byte{p.code, 0, 0}
	b = append(b, p.prefix...)
	for _, h := range p.headers {
		b = append(b, h.id)
		switch h.id >> 6 {
		case 2:
			b = append(b, byte(h.n))
		case 3:
			b = binary.BigEndian.AppendUint32(b, h.n)
		default:
			b = binary.BigEndian.AppendUint16(b, uint16(3+len(h.data)))
			b = append(b, h.data...)
		}
	}
	binary.BigEndian.PutUint16(b[1:], uint16(len(b)))
	return b
}

var errMalformed = errors.New("obex: malformed packet")

func decode(b []byte, prefix int) (*packet, error) {
	if len(b) < 3+prefix {
		return nil, errMalformed
	}
	p := &packet{code: b[0], prefix: b[3 : 3+prefix]}
	for rest := b[3+prefix:]; len(rest) > 0; {
		h := header{id: rest[0]}
		switch h.id >> 6 {
		case 2:
			if len(rest) < 2 {
				return nil, errMalformed
			}
			h.n, rest = uint32(rest[1]), rest[2:]
		case 3:
			if len(rest) < 5 {
				return nil, errMalformed
			}
			h.n, rest = binary.BigEndian.Uint32(rest[1:]), rest[5:]
		default:
			if len(rest) < 3 {
				return nil, errMalformed
			}
			n := int(binary.BigEndian.Uint16(rest[1:]))
			if n < 3 || n > len(rest) {
				return nil, errMalformed
			}
			h.data, rest = rest[3:n], rest[n:]
		}
		p.headers = append(p.headers, h)
	}
	return p, nil
}

// conn carries packets: on a stream each has its length; on L2CAP each
// read and write is one packet, which the buffer (as big as any packet)
// takes whole.
type conn struct {
	w io.Writer
	r *bufio.Reader
	// max: the largest packet to send, the smaller of the peer's and the
	// link's; mtu: the link's (L2CAP), 0 on a stream.
	max, mtu int
}

func newConn(rw io.ReadWriter, mtu int) *conn {
	c := &conn{w: rw, r: bufio.NewReaderSize(rw, 0x10000), max: minPacket, mtu: mtu}
	return c
}

// limit takes the peer's maximum packet size from its CONNECT.
func (c *conn) limit(peer int) {
	c.max = max(minPacket, min(peer, MaxPacket))
	if c.mtu > 0 {
		c.max = min(c.max, c.mtu)
	}
}

func (c *conn) read(prefix func(code byte) int) (*packet, error) {
	var hdr [3]byte
	if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[1:]))
	if n < 3 {
		return nil, errMalformed
	}
	b := make([]byte, n)
	copy(b, hdr[:])
	if _, err := io.ReadFull(c.r, b[3:]); err != nil {
		return nil, err
	}
	return decode(b, prefix(b[0]))
}

func (c *conn) write(p *packet) error {
	b := p.encode()
	if len(b) > max(c.max, minPacket) {
		return fmt.Errorf("obex: a %d-byte packet, the peer takes %d", len(b), c.max)
	}
	_, err := c.w.Write(b)
	return err
}

// room is how much body fits in a packet beside these headers.
func (c *conn) room(prefix int, hs []header) int {
	n := 3 + prefix + 3
	for _, h := range hs {
		n += h.size()
	}
	return c.max - n
}

func connectPrefix() []byte { return []byte{0x10, 0, MaxPacket >> 8, MaxPacket & 0xff} }

// Client is one OBEX session a client opened: Object Push (no target) or
// a profile's service (PBAP's target).
type Client struct {
	c      *conn
	id     uint32
	hasID  bool
	closed bool
}

// Dial connects to the service named by target (nil: the Object Push
// inbox). mtu bounds the packets on L2CAP (0 on RFCOMM).
func Dial(rw io.ReadWriter, mtu int, target []byte) (*Client, error) {
	c := newConn(rw, mtu)
	req := &packet{code: opConnect, prefix: connectPrefix()}
	if target != nil {
		req.headers = append(req.headers, header{id: hTarget, data: target})
	}
	if err := c.write(req); err != nil {
		return nil, err
	}
	resp, err := c.read(func(byte) int { return 4 })
	if err != nil {
		return nil, err
	}
	if resp.code != Success {
		return nil, ResponseError(resp.code)
	}
	c.limit(int(binary.BigEndian.Uint16(resp.prefix[2:])))
	cl := &Client{c: c}
	if h := resp.get(hConnID); h != nil {
		cl.id, cl.hasID = h.n, true
	}
	return cl, nil
}

func (cl *Client) withID(hs ...header) []header {
	if cl.hasID {
		return append([]header{{id: hConnID, n: cl.id}}, hs...)
	}
	return hs
}

func (cl *Client) roundTrip(p *packet) (*packet, error) {
	if err := cl.c.write(p); err != nil {
		return nil, err
	}
	return cl.c.read(func(byte) int { return 0 })
}

// Put sends an object: its name, type (MIME, may be empty) and bytes. The
// peer may take its time to answer the first packet (its user accepts).
func (cl *Client) Put(name, mime string, data []byte) error {
	hs := cl.withID(textHeader(hName, name), header{id: hLength, n: uint32(len(data))})
	if mime != "" {
		hs = append(hs, typeHeader(mime))
	}
	for {
		room := cl.c.room(0, hs)
		if room <= 0 {
			return errors.New("obex: the headers do not fit in a packet")
		}
		last := len(data) <= room
		chunk := data[:min(room, len(data))]
		data = data[len(chunk):]
		p := &packet{code: opPut, headers: hs}
		if last {
			p.code |= final
			p.headers = append(p.headers, header{id: hEndOfBody, data: chunk})
		} else {
			p.headers = append(p.headers, header{id: hBody, data: chunk})
		}
		resp, err := cl.roundTrip(p)
		if err != nil {
			return err
		}
		switch {
		case last && resp.code == Success:
			return nil
		case last || resp.code != Continue:
			return ResponseError(resp.code)
		}
		hs = cl.withID()
	}
}

// Get fetches an object by type and name, with the application parameters
// (PBAP's); it returns the body and the parameters of the answer.
func (cl *Client) Get(typ, name string, params []byte) (body, answer []byte, err error) {
	hs := cl.withID(typeHeader(typ))
	if name != "" {
		hs = append(hs, textHeader(hName, name))
	}
	if params != nil {
		hs = append(hs, header{id: hAppParams, data: params})
	}
	for {
		resp, err := cl.roundTrip(&packet{code: opGet | final, headers: hs})
		if err != nil {
			return nil, nil, err
		}
		if a := resp.bytes(hAppParams); a != nil {
			answer = a
		}
		body = append(body, resp.bytes(hBody)...)
		body = append(body, resp.bytes(hEndOfBody)...)
		switch resp.code {
		case Success:
			return body, answer, nil
		case Continue:
			hs = cl.withID()
		default:
			return nil, nil, ResponseError(resp.code)
		}
	}
}

// SetPath moves into the named folder; "" is the root, up the parent.
func (cl *Client) SetPath(name string, up bool) error {
	flags := byte(0x02)
	if up {
		flags |= 0x01
	}
	hs := cl.withID()
	if !up {
		hs = append(hs, textHeader(hName, name))
	}
	resp, err := cl.roundTrip(&packet{code: opSetPath, prefix: []byte{flags, 0}, headers: hs})
	if err != nil {
		return err
	}
	if resp.code != Success {
		return ResponseError(resp.code)
	}
	return nil
}

// Close ends the session (the link stays the caller's).
func (cl *Client) Close() error {
	if cl.closed {
		return nil
	}
	cl.closed = true
	resp, err := cl.roundTrip(&packet{code: opDisconnect, headers: cl.withID()})
	if err != nil {
		return err
	}
	if resp.code != Success {
		return ResponseError(resp.code)
	}
	return nil
}
