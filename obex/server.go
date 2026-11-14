package obex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// Inbox takes the objects an Object Push client sends.
type Inbox interface {
	// Offer is asked once an object's name, type and size (-1: not said)
	// are known, before its body comes; false refuses it. It may block
	// while the user decides.
	Offer(name, mime string, size int64) bool
	// Received is the whole object; an error refuses it.
	Received(name, mime string, data []byte) error
}

// Server answers one OBEX session (Serve): an Object Push inbox, a phone
// book or both; a client connecting to what is nil is refused.
type Server struct {
	Inbox     Inbox
	Phonebook Phonebook
	// MaxObject bounds what the inbox takes (0: no bound).
	MaxObject int64
}

type put struct {
	name, mime string
	size       int64
	body       bytes.Buffer
}

type session struct {
	s     *Server
	c     *conn
	pbap  bool
	path  []string
	put   *put
	get   *packet // the GET request so far (headers over several packets)
	out   []byte  // the rest of the object being sent
	extra []header
}

// Serve runs the session on rw until the client disconnects or the link
// ends; mtu bounds the packets on L2CAP (0 on RFCOMM).
func (s *Server) Serve(rw io.ReadWriter, mtu int) error {
	ss := &session{s: s, c: newConn(rw, mtu)}
	for {
		p, err := ss.c.read(func(code byte) int {
			switch code {
			case opConnect:
				return 4
			case opSetPath:
				return 2
			}
			return 0
		})
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		resp, done := ss.handle(p)
		if err := ss.c.write(resp); err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func (ss *session) handle(p *packet) (*packet, bool) {
	answer := func(code byte, hs ...header) *packet { return &packet{code: code, headers: hs} }
	switch p.code &^ final {
	case opConnect &^ final:
		if len(p.prefix) < 4 {
			return &packet{code: BadRequest, prefix: connectPrefix()}, false
		}
		ss.c.limit(int(binary.BigEndian.Uint16(p.prefix[2:])))
		target := p.bytes(hTarget)
		switch {
		case target == nil && ss.s.Inbox != nil:
			return &packet{code: Success, prefix: connectPrefix()}, false
		case bytes.Equal(target, PBAPTarget) && ss.s.Phonebook != nil && ss.s.Phonebook.Allowed():
			ss.pbap = true
			return &packet{code: Success, prefix: connectPrefix(), headers: []header{{id: hConnID, n: 1}, {id: hWho, data: PBAPTarget}}}, false
		case target != nil && !bytes.Equal(target, PBAPTarget):
			return &packet{code: NotFound, prefix: connectPrefix()}, false
		}
		return &packet{code: Forbidden, prefix: connectPrefix()}, false
	case opDisconnect &^ final:
		return answer(Success), true
	case opAbort &^ final:
		ss.put, ss.get, ss.out = nil, nil, nil
		return answer(Success), false
	case opPut:
		return ss.handlePut(p), false
	case opGet:
		return ss.handleGet(p), false
	case opSetPath &^ final:
		return answer(ss.setPath(p)), false
	}
	return answer(NotImplemented), false
}

func (ss *session) handlePut(p *packet) *packet {
	in := ss.s.Inbox
	if in == nil || ss.pbap {
		return &packet{code: Forbidden}
	}
	if ss.put == nil {
		name, _ := p.text(hName)
		if name == "" {
			return &packet{code: BadRequest}
		}
		size := int64(-1)
		if h := p.get(hLength); h != nil {
			size = int64(h.n)
		}
		if ss.s.MaxObject > 0 && size > ss.s.MaxObject {
			return &packet{code: EntityTooLarge}
		}
		if !in.Offer(name, typeOf(p), size) {
			return &packet{code: Forbidden}
		}
		ss.put = &put{name: name, mime: typeOf(p), size: size}
	}
	pt := ss.put
	pt.body.Write(p.bytes(hBody))
	pt.body.Write(p.bytes(hEndOfBody))
	if ss.s.MaxObject > 0 && int64(pt.body.Len()) > ss.s.MaxObject {
		ss.put = nil
		return &packet{code: EntityTooLarge}
	}
	if p.code&final == 0 {
		return &packet{code: Continue}
	}
	ss.put = nil
	if p.get(hEndOfBody) == nil {
		return &packet{code: BadRequest}
	}
	if err := in.Received(pt.name, pt.mime, pt.body.Bytes()); err != nil {
		return &packet{code: NotAcceptable}
	}
	return &packet{code: Success}
}

// handleGet gathers a GET request (it may take several packets), then
// sends the object in as many responses as it needs.
func (ss *session) handleGet(p *packet) *packet {
	if ss.out == nil {
		if ss.get == nil {
			ss.get = &packet{}
		}
		ss.get.headers = append(ss.get.headers, p.headers...)
		if p.code&final == 0 {
			return &packet{code: Continue}
		}
		req := ss.get
		ss.get = nil
		if !ss.pbap {
			return &packet{code: NotFound}
		}
		obj, params, code := ss.pbapGet(req)
		if code != Success {
			return &packet{code: code}
		}
		ss.out, ss.extra = obj, nil
		if params != nil {
			ss.extra = []header{{id: hAppParams, data: params}}
		}
	}
	room := ss.c.room(0, ss.extra)
	chunk := ss.out[:min(len(ss.out), max(room, 0))]
	ss.out = ss.out[len(chunk):]
	hs := ss.extra
	ss.extra = nil
	if len(ss.out) > 0 {
		return &packet{code: Continue, headers: append(hs, header{id: hBody, data: chunk})}
	}
	ss.out = nil
	return &packet{code: Success, headers: append(hs, header{id: hEndOfBody, data: chunk})}
}

func (ss *session) setPath(p *packet) byte {
	if !ss.pbap || len(p.prefix) < 1 {
		return Forbidden
	}
	name, has := p.text(hName)
	switch {
	case p.prefix[0]&0x01 != 0:
		if len(ss.path) == 0 {
			return NotFound
		}
		ss.path = ss.path[:len(ss.path)-1]
		if name == "" {
			return Success
		}
	case !has || name == "":
		ss.path = nil
		return Success
	}
	next := append(append([]string{}, ss.path...), name)
	if !folderExists(next) {
		return NotFound
	}
	ss.path = next
	return Success
}
