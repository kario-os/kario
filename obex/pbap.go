package obex

import (
	"encoding/binary"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

// PBAPTarget is the Phone Book Access service's target (and Who).
var PBAPTarget = []byte{0x79, 0x61, 0x35, 0xf0, 0xf0, 0xc5, 0x11, 0xd8, 0x09, 0x66, 0x08, 0x00, 0x20, 0x0c, 0x9a, 0x66}

// Phonebook is what the phone book server gives a client (a car kit).
type Phonebook interface {
	// Allowed is asked when a client connects; false refuses it. It may
	// block while the user decides.
	Allowed() bool
	// Owner is the phone's own card (handle 0 of the phone book).
	Owner() Card
	Contacts() []Card
	// Calls is a call history, newest first: "ich" received, "och"
	// dialled, "mch" missed, "cch" all of them.
	Calls(kind string) []Card
}

// Card is a contact or a call history entry.
type Card struct {
	Name, Number string
	// A call's time and kind: RECEIVED, DIALED or MISSED.
	At   time.Time
	Call string
}

// Application parameter tags (PBAP 1.2 §6.2.1).
const (
	tagSearchValue     = 0x02
	tagSearchAttribute = 0x03
	tagMaxListCount    = 0x04
	tagListStartOffset = 0x05
	tagFormat          = 0x07
	tagPhonebookSize   = 0x08
)

var historyFolders = []string{"ich", "och", "mch", "cch"}

func folderExists(path []string) bool {
	switch len(path) {
	case 1:
		return path[0] == "telecom"
	case 2:
		return path[0] == "telecom" && (path[1] == "pb" || contains(historyFolders, path[1]))
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Params are a request's application parameters.
type Params map[byte][]byte

// ParseParams reads tag-length-value parameters.
func ParseParams(b []byte) Params {
	p := Params{}
	for len(b) >= 2 && int(b[1])+2 <= len(b) {
		p[b[0]] = b[2 : 2+int(b[1])]
		b = b[2+int(b[1]):]
	}
	return p
}

// Encode writes them in tag order.
func (p Params) Encode() []byte {
	var b []byte
	for tag := 0; tag < 256; tag++ {
		if v, ok := p[byte(tag)]; ok {
			b = append(append(b, byte(tag), byte(len(v))), v...)
		}
	}
	return b
}

func (p Params) uint16(tag byte, def int) int {
	if v := p[tag]; len(v) == 2 {
		return int(binary.BigEndian.Uint16(v))
	}
	return def
}

// U16 is a two-byte parameter value.
func U16(n int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(n)) }

func (ss *session) folder(name string) (string, bool) {
	path := append([]string{}, ss.path...)
	if name != "" {
		path = append(path, strings.Split(strings.Trim(name, "/"), "/")...)
	}
	if len(path) != 2 || !folderExists(path) {
		return "", false
	}
	return path[1], true
}

func (ss *session) cards(folder string) []Card {
	pb := ss.s.Phonebook
	if folder == "pb" {
		return append([]Card{pb.Owner()}, pb.Contacts()...)
	}
	return pb.Calls(folder)
}

// pbapGet answers PullPhoneBook (an absolute name, telecom/pb.vcf),
// PullvCardListing (a folder below the current one) and PullvCardEntry
// (a handle in the current folder).
func (ss *session) pbapGet(req *packet) ([]byte, []byte, byte) {
	name, _ := req.text(hName)
	params := ParseParams(req.bytes(hAppParams))
	v30 := len(params[tagFormat]) == 1 && params[tagFormat][0] == 1
	maxCount, offset := params.uint16(tagMaxListCount, 0xffff), params.uint16(tagListStartOffset, 0)
	switch typeOf(req) {
	case "x-bt/phonebook":
		saved := ss.path
		ss.path = nil
		folder, ok := ss.folder(strings.TrimSuffix(name, ".vcf"))
		ss.path = saved
		if !ok {
			return nil, nil, NotFound
		}
		cards := ss.cards(folder)
		if maxCount == 0 {
			return []byte{}, Params{tagPhonebookSize: U16(len(cards))}.Encode(), Success
		}
		var b strings.Builder
		for _, c := range page(cards, offset, maxCount) {
			b.WriteString(vcard(c, v30))
		}
		return []byte(b.String()), nil, Success
	case "x-bt/vcard-listing":
		folder, ok := ss.folder(name)
		if !ok {
			return nil, nil, NotFound
		}
		cards := ss.cards(folder)
		if maxCount == 0 {
			return []byte{}, Params{tagPhonebookSize: U16(len(cards))}.Encode(), Success
		}
		value, by := strings.ToLower(string(params[tagSearchValue])), 0
		if v := params[tagSearchAttribute]; len(v) == 1 {
			by = int(v[0])
		}
		var b strings.Builder
		b.WriteString("<?xml version=\"1.0\"?>\r\n<!DOCTYPE vcard-listing SYSTEM \"vcard-listing.dtd\">\r\n<vCard-listing version=\"1.0\">\r\n")
		n := 0
		for i, c := range cards {
			field := c.Name
			if by == 1 {
				field = c.Number
			}
			if value != "" && !strings.Contains(strings.ToLower(field), value) {
				continue
			}
			if n++; n <= offset || n > offset+maxCount {
				continue
			}
			handle := i
			if folder != "pb" {
				handle = i + 1
			}
			fmt.Fprintf(&b, "<card handle=\"%d.vcf\" name=\"%s\"/>\r\n", handle, html.EscapeString(firstNonEmpty(c.Name, c.Number)))
		}
		b.WriteString("</vCard-listing>\r\n")
		return []byte(b.String()), nil, Success
	case "x-bt/vcard":
		folder, ok := ss.folder("")
		h, err := strconv.Atoi(strings.TrimSuffix(name, ".vcf"))
		if !ok || err != nil {
			return nil, nil, NotFound
		}
		cards := ss.cards(folder)
		if folder != "pb" {
			h--
		}
		if h < 0 || h >= len(cards) {
			return nil, nil, NotFound
		}
		return []byte(vcard(cards[h], v30)), nil, Success
	}
	return nil, nil, BadRequest
}

func page(cards []Card, offset, count int) []Card {
	if offset >= len(cards) {
		return nil
	}
	return cards[offset:min(len(cards), offset+count)]
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// vcardEscape escapes a value's separators and line breaks.
func vcardEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`).Replace(s)
}

// vcard writes a card in vCard 2.1 (the default, which car kits read
// best) or 3.0; non-ASCII text in 2.1 says its charset.
func vcard(c Card, v30 bool) string {
	name := vcardEscape(firstNonEmpty(c.Name, c.Number))
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\n")
	charset := ""
	if v30 {
		b.WriteString("VERSION:3.0\r\n")
	} else {
		b.WriteString("VERSION:2.1\r\n")
		for _, r := range name {
			if r > 0x7e {
				charset = ";CHARSET=UTF-8"
				break
			}
		}
	}
	if c.Name == "" {
		name = ""
	}
	fmt.Fprintf(&b, "N%s:%s;;;;\r\nFN%s:%s\r\n", charset, name, charset, name)
	if c.Number != "" {
		tel := "TEL;CELL:"
		if v30 {
			tel = "TEL;TYPE=CELL:"
		}
		b.WriteString(tel + vcardEscape(c.Number) + "\r\n")
	}
	if c.Call != "" && !c.At.IsZero() {
		kind := c.Call
		if v30 {
			kind = "TYPE=" + kind
		}
		fmt.Fprintf(&b, "X-IRMC-CALL-DATETIME;%s:%s\r\n", kind, c.At.Format("20060102T150405"))
	}
	b.WriteString("END:VCARD\r\n")
	return b.String()
}
