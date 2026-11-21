// Package btphone is a simulated old phone over Bluetooth, for the tests
// and the selftest of moving from an old phone (docs/36): the phone book
// server (PBAP) and the message server (MAP) of an Android phone or an
// iPhone. They serve fixtures (fixtures/) written the way those phones
// write their objects: Android's vCard 2.1 with quoted-printable names
// and its own card first, its bMessages and listings; an iPhone's vCard
// 3.0, whatever version is asked for, without an own card, and a message
// server that lists nothing and has no sent folder. Written apart from
// the obex package's clients, from the profiles; it serves the files as
// they are, a page at a time, and keeps what it was asked.
package btphone

import (
	"embed"
	"encoding/binary"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kario-os/kario/obex"
)

//go:embed fixtures
var fixtures embed.FS

// Shapes are the phones there are.
var Shapes = []string{"android", "iphone"}

// Phone is one simulated old phone; it takes sessions side by side.
type Phone struct {
	Shape string
	// Ask is the phone's user asked whether Kario may read the phone book
	// ("pbap") or the messages ("map"), each time a session opens; it may
	// block. Nil allows.
	Ask func(service string) bool
	// IgnoreOffset: the phone book server sends the whole object
	// whatever part was asked for (some phones do).
	IgnoreOffset bool

	book  map[string][]string // pb, ich, och, mch: vCards in handle order
	owner bool                // pb's first card is handle 0, the phone's own
	lists map[string][]string // inbox, sent: the listing's msg elements
	msgs  map[string][]byte   // bMessages by handle

	mu    sync.Mutex
	asked []string
}

var (
	msgElem  = regexp.MustCompile(`(?s)<msg\s.*?/>`)
	typeAttr = regexp.MustCompile(`\btype="([^"]*)"`)
)

// New is a phone of that shape.
func New(shape string) (*Phone, error) {
	p := &Phone{Shape: shape, book: map[string][]string{}, lists: map[string][]string{}, msgs: map[string][]byte{}}
	dir := "fixtures/" + shape
	if _, err := fixtures.ReadDir(dir); err != nil {
		return nil, fmt.Errorf("btphone: no phone shaped %q", shape)
	}
	for _, f := range []string{"pb", "ich", "och", "mch"} {
		raw, err := fixtures.ReadFile(dir + "/" + f + ".vcf")
		if err != nil {
			return nil, err
		}
		p.book[f] = cards(string(raw))
	}
	p.owner = shape == "android"
	for _, f := range []string{"inbox", "sent"} {
		if raw, err := fixtures.ReadFile(dir + "/" + f + ".xml"); err == nil {
			p.lists[f] = msgElem.FindAllString(string(raw), -1)
		}
	}
	ms, _ := fixtures.ReadDir(dir + "/msg")
	for _, m := range ms {
		raw, _ := fixtures.ReadFile(dir + "/msg/" + m.Name())
		p.msgs[strings.TrimSuffix(m.Name(), ".bmsg")] = raw
	}
	return p, nil
}

// cards cuts a .vcf into its cards, as they are.
func cards(s string) []string {
	var out []string
	for _, c := range strings.SplitAfter(s, "END:VCARD\r\n") {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out
}

// HasMessages: the phone has a message server.
func (p *Phone) HasMessages() bool { return len(p.lists) > 0 }

// Services are the phone's servers for an obex.Server.
func (p *Phone) Services() map[string]func() obex.Service {
	s := map[string]func() obex.Service{string(obex.PBAPTarget): func() obex.Service {
		if !p.allow("pbap") {
			return nil
		}
		return &pse{p: p}
	}}
	if p.HasMessages() {
		s[string(obex.MASTarget)] = func() obex.Service {
			if !p.allow("map") {
				return nil
			}
			return &mse{p: p}
		}
	}
	return s
}

func (p *Phone) allow(service string) bool {
	p.record(service + " CONNECT")
	return p.Ask == nil || p.Ask(service)
}

func (p *Phone) record(s string) {
	p.mu.Lock()
	p.asked = append(p.asked, s)
	p.mu.Unlock()
}

// Asked is what the phone was asked, in order: CONNECTs, SETPATHs and
// GETs with their application parameters as tag=hex.
func (p *Phone) Asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...)
}

func (p *Phone) recordGet(service, typ, name string, params obex.Params) {
	tags := make([]int, 0, len(params))
	for t := range params {
		tags = append(tags, int(t))
	}
	sort.Ints(tags)
	var b strings.Builder
	fmt.Fprintf(&b, "%s GET %s %s", service, typ, name)
	for _, t := range tags {
		fmt.Fprintf(&b, " %02x=%x", t, params[byte(t)])
	}
	p.record(b.String())
}

func u16(v []byte, def int) int {
	if len(v) == 2 {
		return int(binary.BigEndian.Uint16(v))
	}
	return def
}

func page[T any](all []T, offset, count int) []T {
	if offset >= len(all) {
		return nil
	}
	return all[offset:min(len(all), offset+count)]
}

// pse is a phone book session: PBAP's virtual folders telecom/pb, ich,
// och, mch and cch.
type pse struct {
	p    *Phone
	path []string
}

func (s *pse) SetPath(name string, up bool) byte {
	s.p.record(fmt.Sprintf("pbap SETPATH %q up=%v", name, up))
	path := s.path
	if up {
		if len(path) == 0 {
			return obex.NotFound
		}
		path = path[:len(path)-1]
	} else if name == "" {
		path = nil
	}
	if name != "" {
		path = append(append([]string{}, path...), name)
	}
	if len(path) > 2 || len(path) >= 1 && path[0] != "telecom" || len(path) == 2 && s.folder(path[1]) == nil {
		return obex.NotFound
	}
	s.path = path
	return obex.Success
}

func (s *pse) folder(f string) []string {
	if f == "cch" {
		return append(append(append([]string{}, s.p.book["ich"]...), s.p.book["och"]...), s.p.book["mch"]...)
	}
	if c, ok := s.p.book[f]; ok {
		return c
	}
	return nil
}

// handle is the handle of a folder's i-th card: pb counts from 0 with
// the phone's own card, from 1 without it, as the histories do.
func (s *pse) handle(f string, i int) int {
	if f == "pb" && s.p.owner {
		return i
	}
	return i + 1
}

func (s *pse) Get(typ, name string, params obex.Params) ([]byte, obex.Params, byte) {
	s.p.recordGet("pbap", typ, name, params)
	count, offset := u16(params[0x04], 0xffff), u16(params[0x05], 0)
	resolve := func(rel string) (string, bool) {
		path := append(append([]string{}, s.path...), strings.Split(strings.Trim(rel, "/"), "/")...)
		if rel == "" {
			path = s.path
		}
		if len(path) != 2 || path[0] != "telecom" || s.folder(path[1]) == nil {
			return "", false
		}
		return path[1], true
	}
	switch typ {
	case "x-bt/phonebook":
		f, ok := strings.CutPrefix(strings.TrimSuffix(strings.TrimPrefix(name, "/"), ".vcf"), "telecom/")
		cs := s.folder(f)
		if !ok || cs == nil {
			return nil, nil, obex.NotFound
		}
		if count == 0 && !s.p.IgnoreOffset {
			return nil, obex.Params{0x08: obex.U16(len(cs))}, obex.Success
		}
		if !s.p.IgnoreOffset {
			cs = page(cs, offset, count)
		}
		return []byte(strings.Join(cs, "")), nil, obex.Success
	case "x-bt/vcard-listing":
		f, ok := resolve(name)
		if !ok {
			return nil, nil, obex.NotFound
		}
		cs := s.folder(f)
		if count == 0 {
			return nil, obex.Params{0x08: obex.U16(len(cs))}, obex.Success
		}
		var b strings.Builder
		b.WriteString("<?xml version=\"1.0\"?>\r\n<!DOCTYPE vcard-listing SYSTEM \"vcard-listing.dtd\">\r\n<vCard-listing version=\"1.0\">\r\n")
		for i := offset; i < len(cs) && i < offset+count; i++ {
			fmt.Fprintf(&b, "<card handle=\"%d.vcf\" name=\"\"/>\r\n", s.handle(f, i))
		}
		b.WriteString("</vCard-listing>\r\n")
		return []byte(b.String()), nil, obex.Success
	case "x-bt/vcard":
		f, ok := resolve("")
		h, err := strconv.Atoi(strings.TrimSuffix(name, ".vcf"))
		if !ok || err != nil {
			return nil, nil, obex.NotFound
		}
		cs := s.folder(f)
		for i := range cs {
			if s.handle(f, i) == h {
				return []byte(cs[i]), nil, obex.Success
			}
		}
		return nil, nil, obex.NotFound
	}
	return nil, nil, obex.BadRequest
}

// mse is a message session: telecom/msg and its folders.
type mse struct {
	p    *Phone
	path []string
}

var mapFolders = []string{"inbox", "outbox", "sent", "deleted", "draft"}

func (s *mse) SetPath(name string, up bool) byte {
	s.p.record(fmt.Sprintf("map SETPATH %q up=%v", name, up))
	path := s.path
	if up {
		if len(path) == 0 {
			return obex.NotFound
		}
		path = path[:len(path)-1]
	} else if name == "" {
		path = nil
	}
	if name != "" {
		path = append(append([]string{}, path...), strings.ToLower(name))
	}
	ok := len(path) <= 3 && (len(path) < 1 || path[0] == "telecom") && (len(path) < 2 || path[1] == "msg")
	if len(path) == 3 {
		ok = ok && slices.Contains(mapFolders, path[2])
	}
	if !ok {
		return obex.NotFound
	}
	s.path = path
	return obex.Success
}

// filterBits are FilterMessageType's: a set bit leaves that type out.
var filterBits = map[string]byte{"SMS_GSM": 0x01, "SMS_CDMA": 0x02, "EMAIL": 0x04, "MMS": 0x08, "IM": 0x10}

func (s *mse) Get(typ, name string, params obex.Params) ([]byte, obex.Params, byte) {
	s.p.recordGet("map", typ, name, params)
	switch typ {
	case "x-bt/MAP-msg-listing":
		path := s.path
		if name != "" {
			path = append(append([]string{}, path...), strings.ToLower(name))
		}
		if len(path) != 3 || path[0] != "telecom" || path[1] != "msg" || !slices.Contains(mapFolders, path[2]) {
			return nil, nil, obex.NotFound
		}
		all, ok := s.p.lists[path[2]]
		if !ok {
			return nil, nil, obex.NotFound
		}
		var filter byte
		if v := params[0x03]; len(v) == 1 {
			filter = v[0]
		}
		var es []string
		for _, e := range all {
			if m := typeAttr.FindStringSubmatch(e); m == nil || filterBits[m[1]]&filter == 0 {
				es = append(es, e)
			}
		}
		count, offset := u16(params[0x01], 1024), u16(params[0x02], 0)
		if count == 0 {
			return nil, obex.Params{0x12: obex.U16(len(es)), 0x0d: {0}}, obex.Success
		}
		var b strings.Builder
		b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\" ?>\r\n<MAP-msg-listing version=\"1.0\">\r\n")
		for _, e := range page(es, offset, count) {
			b.WriteString(e + "\r\n")
		}
		b.WriteString("</MAP-msg-listing>\r\n")
		return []byte(b.String()), obex.Params{0x0d: {0}}, obex.Success
	case "x-bt/message":
		// GetMessage's mandatory parameters: Attachment and Charset.
		if len(params[0x0a]) != 1 || len(params[0x14]) != 1 {
			return nil, nil, obex.BadRequest
		}
		if params[0x14][0] != 1 {
			return nil, nil, obex.NotAcceptable
		}
		m, ok := s.p.msgs[name]
		if !ok {
			return nil, nil, obex.NotFound
		}
		return m, nil, obex.Success
	}
	return nil, nil, obex.BadRequest
}
