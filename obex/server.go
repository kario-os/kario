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

// Service is one session of a directed service other than the phone book
// Kario serves: a target a Server answers for (a simulated old phone's
// phone book and messages in the tests and the selftest).
type Service interface {
	// Get answers a GET of that type and name; params are the request's
	// application parameters, answer goes in the first response.
	Get(typ, name string, params Params) (body []byte, answer Params, code byte)
	// SetPath moves into the named folder; "" without up is the root, up
	// the parent and then name (if any).
	SetPath(name string, up bool) byte
}

// Server answers one OBEX session (Serve): an Object Push inbox, a phone
// book, other services or several of them; a client connecting to what
// is nil is refused.
type Server struct {
	Inbox     Inbox
	Phonebook Phonebook
	// Services open a session of the service a CONNECT's target names, by
	// the target's bytes; one that gives nil refuses it (Forbidden). They
	// come before Phonebook. A Service may block while its user decides.
	Services map[string]func() Service
	// MaxObject bounds what the inbox takes (0: no bound).
	MaxObject int64
	// Auth: with a Password, a CONNECT is challenged for it, and nothing
	// else is served before one with the response; Ask answers a client's
	// challenge in its CONNECT, which without it goes unanswered (the
	// client decides).
	Auth Auth
}

// maxAuthTries is how many wrong responses end a session.
const maxAuthTries = 3

type put struct {
	name, mime string
	size       int64
	body       bytes.Buffer
	// srm: the client asked for Single Response Mode; on: the answer
	// turning it on has gone, so the packets until the last get none.
	srm, on bool
}

type session struct {
	s    *Server
	c    *conn
	pbap bool
	svc  Service
	// id is the Connection ID a directed CONNECT was given (0: none).
	id   uint32
	path []string
	put  *put
	// discard: an SRM PUT was refused midway; the client's packets of it
	// still coming get no answer.
	discard bool
	get     *packet // the GET request so far (headers over several packets)
	out     []byte  // the rest of the object being sent
	extra   []header
	// getSRM: this GET is in Single Response Mode, said once the first
	// response told the client; wait: its last request asked for one
	// response only (SRMP).
	getSRM, said, wait bool
	// nonce: the challenge out (challenged), which a CONNECT with the
	// response to it answers (authed); fails: wrong responses so far.
	nonce              [nonceSize]byte
	challenged, authed bool
	fails              int
}

// connID is the one a directed session gets.
const connID = 1

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
		if resp != nil {
			if err := ss.c.write(resp); err != nil {
				return err
			}
		}
		// Single Response Mode: the rest of the object without requests.
		for ss.out != nil && ss.getSRM && !ss.wait {
			if err := ss.c.write(ss.nextGet()); err != nil {
				return err
			}
		}
		if done {
			return nil
		}
	}
}

// handle answers a request; nil when it gets no answer (Single Response
// Mode).
func (ss *session) handle(p *packet) (*packet, bool) {
	answer := func(code byte, hs ...header) *packet { return &packet{code: code, headers: hs} }
	op := p.code &^ final
	if op != opPut&^final || p.get(hName) != nil {
		ss.discard = false
	}
	if h := p.get(hConnID); h != nil && op != opConnect&^final && ss.id != 0 && h.n != ss.id {
		return answer(ServiceUnavailable), false
	}
	if ss.s.Auth.Password != "" && !ss.authed && op != opConnect&^final && op != opDisconnect&^final && op != opAbort&^final {
		return answer(Unauthorized), false
	}
	switch op {
	case opConnect &^ final:
		if len(p.prefix) < 4 {
			return &packet{code: BadRequest, prefix: connectPrefix()}, false
		}
		ss.c.limit(int(binary.BigEndian.Uint16(p.prefix[2:])))
		if refused := ss.authenticate(p); refused != nil {
			return refused, ss.fails >= maxAuthTries
		}
		var theirs *Challenge
		if p.get(hAuthChallenge) != nil && ss.s.Auth.Ask != nil {
			ch, err := parseChallenge(p.bytes(hAuthChallenge))
			if err != nil {
				return &packet{code: BadRequest, prefix: connectPrefix()}, false
			}
			theirs = &ch
		}
		resp := ss.connect(p)
		if theirs != nil && resp.code == Success {
			if user, password, ok := ss.s.Auth.Ask(*theirs); ok {
				resp.headers = append(resp.headers, response(*theirs, user, password))
			}
		}
		return resp, false
	case opDisconnect &^ final:
		return answer(Success), true
	case opAbort &^ final:
		ss.put, ss.get, ss.out, ss.getSRM = nil, nil, nil, false
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

// authenticate challenges a CONNECT while the server asks for a password
// and the session has not proved it; nil once it has. A wrong response
// gets Unauthorized without a challenge, and the next CONNECT a new one.
func (ss *session) authenticate(p *packet) *packet {
	if ss.s.Auth.Password == "" || ss.authed {
		return nil
	}
	if ss.challenged && p.get(hAuthResponse) != nil {
		ss.challenged = false
		if proves(p.bytes(hAuthResponse), ss.nonce, ss.s.Auth.Password) {
			ss.authed = true
			return nil
		}
		ss.fails++
		return &packet{code: Unauthorized, prefix: connectPrefix()}
	}
	c := newChallenge()
	ss.nonce, ss.challenged = c.Nonce, true
	return &packet{code: Unauthorized, prefix: connectPrefix(), headers: []header{{id: hAuthChallenge, data: c.encode()}}}
}

// connect opens the session a CONNECT asks for: the inbox, the phone book
// or a service.
func (ss *session) connect(p *packet) *packet {
	target := p.bytes(hTarget)
	directed := func() *packet {
		ss.id = connID
		return &packet{code: Success, prefix: connectPrefix(), headers: []header{{id: hConnID, n: connID}, {id: hWho, data: target}}}
	}
	if open := ss.s.Services[string(target)]; target != nil && open != nil {
		if ss.svc = open(); ss.svc == nil {
			return &packet{code: Forbidden, prefix: connectPrefix()}
		}
		return directed()
	}
	switch {
	case target == nil && ss.s.Inbox != nil:
		return &packet{code: Success, prefix: connectPrefix()}
	case bytes.Equal(target, PBAPTarget) && ss.s.Phonebook != nil && ss.s.Phonebook.Allowed():
		ss.pbap = true
		return directed()
	case target != nil && !bytes.Equal(target, PBAPTarget):
		return &packet{code: NotFound, prefix: connectPrefix()}
	}
	return &packet{code: Forbidden, prefix: connectPrefix()}
}

func (ss *session) handlePut(p *packet) *packet {
	if ss.discard {
		ss.discard = p.code&final == 0
		return nil
	}
	in := ss.s.Inbox
	if in == nil || ss.pbap || ss.svc != nil {
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
		ss.put = &put{name: name, mime: typeOf(p), size: size, srm: ss.c.mtu > 0 && has1(p, hSRM, srmEnable)}
	}
	pt := ss.put
	fail := func(code byte) *packet {
		ss.put, ss.discard = nil, pt.on && p.code&final == 0
		return &packet{code: code}
	}
	pt.body.Write(p.bytes(hBody))
	pt.body.Write(p.bytes(hEndOfBody))
	if ss.s.MaxObject > 0 && int64(pt.body.Len()) > ss.s.MaxObject {
		return fail(EntityTooLarge)
	}
	if p.code&final == 0 {
		switch {
		case pt.on && !has1(p, hSRMP, srmpWait):
			return nil
		case pt.srm && !pt.on:
			pt.on = true
			return &packet{code: Continue, headers: []header{{id: hSRM, n: srmEnable}}}
		}
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

// handleGet gathers a GET request (it may take several packets, each
// answered), then sends the object in as many responses as it needs: one
// per request, or in Single Response Mode all of them (Serve) unless a
// request asks to wait.
func (ss *session) handleGet(p *packet) *packet {
	if ss.out == nil {
		if ss.get == nil {
			ss.get = &packet{}
			ss.getSRM, ss.said = ss.c.mtu > 0 && has1(p, hSRM, srmEnable), false
		}
		ss.get.headers = append(ss.get.headers, p.headers...)
		if p.code&final == 0 {
			return ss.srmSaid(&packet{code: Continue})
		}
		req := ss.get
		ss.get = nil
		var obj []byte
		var params []byte
		code := byte(NotFound)
		switch {
		case ss.svc != nil:
			var ans Params
			name, _ := req.text(hName)
			obj, ans, code = ss.svc.Get(typeOf(req), name, ParseParams(req.bytes(hAppParams)))
			if len(ans) > 0 {
				params = ans.Encode()
			}
		case ss.pbap:
			obj, params, code = ss.pbapGet(req)
		}
		if code != Success {
			ss.getSRM = false
			return &packet{code: code}
		}
		ss.out, ss.extra = obj, nil
		if ss.out == nil {
			ss.out = []byte{}
		}
		if params != nil {
			ss.extra = []header{{id: hAppParams, data: params}}
		}
	}
	ss.wait = has1(p, hSRMP, srmpWait)
	return ss.nextGet()
}

// srmSaid adds the SRM header to an operation's first response.
func (ss *session) srmSaid(resp *packet) *packet {
	if ss.getSRM && !ss.said {
		ss.said = true
		resp.headers = append([]header{{id: hSRM, n: srmEnable}}, resp.headers...)
	}
	return resp
}

// nextGet is the next response of the object being sent.
func (ss *session) nextGet() *packet {
	hs := ss.extra
	if ss.getSRM && !ss.said {
		hs = append([]header{{id: hSRM, n: srmEnable}}, hs...)
		ss.said = true
	}
	room := ss.c.room(0, hs)
	chunk := ss.out[:min(len(ss.out), max(room, 0))]
	ss.out = ss.out[len(chunk):]
	ss.extra = nil
	if len(ss.out) > 0 {
		return &packet{code: Continue, headers: append(hs, header{id: hBody, data: chunk})}
	}
	ss.out, ss.getSRM = nil, false
	return &packet{code: Success, headers: append(hs, header{id: hEndOfBody, data: chunk})}
}

func (ss *session) setPath(p *packet) byte {
	if len(p.prefix) < 1 || !ss.pbap && ss.svc == nil {
		return Forbidden
	}
	name, has := p.text(hName)
	up := p.prefix[0]&0x01 != 0
	if ss.svc != nil {
		if !has {
			name = ""
		}
		return ss.svc.SetPath(name, up)
	}
	switch {
	case up:
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
