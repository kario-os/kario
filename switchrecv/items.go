package switchrecv

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Bounds of one item; a longer field rejects the item (it is not cut).
const (
	maxVCard  = 512 << 10 // a photo inline is at most 256 KB, base64 makes it a third more
	maxEvent  = 64 << 10
	maxBody   = 64 << 10
	maxNumber = 64
	maxName   = 128     // characters
	maxCall   = 1 << 20 // seconds
)

// idPattern is what a file's id, and an item's optional one, may be.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// item is one parsed item: what goes to the sink, the hash it is known by
// in this session, and the id the app gave it (optional).
type item struct {
	value any
	hash  [32]byte
	id    string
}

var errItem = errors.New("bad item")

func itemError(what string) error { return errors.New(what) }

// parseItem reads one item of an item kind. The hash covers what the sink
// gets, so the same item sent with its fields in another order, or again
// in a later batch, is the same item.
func parseItem(kind string, raw json.RawMessage) (item, error) {
	var it item
	var head struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return it, errItem
	}
	if head.ID != "" && idPattern.MatchString(head.ID) {
		it.id = head.ID
	}
	switch kind {
	case KindContacts:
		var v struct {
			VCard string `json:"vcard"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return it, errItem
		}
		switch {
		case len(v.VCard) > maxVCard:
			return it, itemError("vcard too long")
		case !strings.Contains(strings.ToUpper(v.VCard), "BEGIN:VCARD"):
			return it, itemError("not a vcard")
		}
		it.value = v.VCard
	case KindCallLog:
		var v struct {
			Number   string `json:"number"`
			Name     string `json:"name"`
			Type     string `json:"type"`
			At       int64  `json:"at"`
			Duration int64  `json:"duration"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return it, errItem
		}
		switch {
		case !textField(v.Number, maxNumber) || strings.TrimSpace(v.Number) == "":
			return it, itemError("bad number")
		case !textField(v.Name, 4*maxName) || utf8.RuneCountInString(v.Name) > maxName:
			return it, itemError("bad name")
		case v.Type != "in" && v.Type != "out" && v.Type != "missed" && v.Type != "rejected":
			return it, itemError("bad type")
		case v.At <= 0:
			return it, itemError("bad at")
		case v.Duration < 0 || v.Duration > maxCall:
			return it, itemError("bad duration")
		}
		it.value = CallEntry{Number: strings.TrimSpace(v.Number), Name: strings.TrimSpace(v.Name), Type: v.Type, At: v.At, Duration: v.Duration}
	case KindSMS:
		var v struct {
			Address string `json:"address"`
			Body    string `json:"body"`
			Type    string `json:"type"`
			At      int64  `json:"at"`
			Read    bool   `json:"read"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return it, errItem
		}
		switch {
		case !textField(v.Address, maxNumber) || strings.TrimSpace(v.Address) == "":
			return it, itemError("bad address")
		case len(v.Body) > maxBody || !utf8.ValidString(v.Body):
			return it, itemError("bad body")
		case strings.TrimSpace(v.Body) == "":
			return it, itemError("empty body")
		case v.Type != "in" && v.Type != "out":
			return it, itemError("bad type")
		case v.At <= 0:
			return it, itemError("bad at")
		}
		it.value = Message{Address: strings.TrimSpace(v.Address), Body: v.Body, Type: v.Type, At: v.At, Read: v.Read}
	case KindCalendar:
		var v struct {
			ICS string `json:"ics"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return it, errItem
		}
		ev, err := cleanEvent(v.ICS)
		if err != nil {
			return it, err
		}
		it.value = ev
	default:
		return it, errItem
	}
	canon, _ := json.Marshal(it.value)
	it.hash = sha256.Sum256(canon)
	return it, nil
}

// textField: valid UTF-8 of at most n bytes, without control characters.
func textField(s string, n int) bool {
	if len(s) > n || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// cleanEvent is one VEVENT with CRLF line ends and none after END:VEVENT
// (the app may or may not send one): its components nest properly, and it
// holds no other VEVENT and no calendar of its own.
func cleanEvent(ics string) (string, error) {
	if len(ics) > maxEvent || !utf8.ValidString(ics) {
		return "", itemError("bad ics")
	}
	ics = strings.ReplaceAll(strings.ReplaceAll(ics, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(strings.TrimRight(ics, "\n \t"), "\n")
	var stack []string
	for i, l := range lines {
		// A folded line starts with a space: part of the property before.
		up := strings.ToUpper(strings.TrimRight(l, " \t"))
		name, isBegin := strings.CutPrefix(up, "BEGIN:")
		end, isEnd := strings.CutPrefix(up, "END:")
		switch {
		case i == 0 && up != "BEGIN:VEVENT", i == len(lines)-1 && up != "END:VEVENT":
			return "", itemError("not a vevent")
		case isBegin && (name == "VEVENT" || name == "VCALENDAR") && i > 0:
			return "", itemError("nested vevent")
		case isBegin:
			stack = append(stack, name)
		case isEnd:
			if len(stack) == 0 || stack[len(stack)-1] != end {
				return "", itemError("unbalanced ics")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 && i != len(lines)-1 {
				return "", itemError("text after the vevent")
			}
		}
		if strings.ContainsFunc(l, func(r rune) bool { return (r < 0x20 && r != '\t') || r == 0x7f }) {
			return "", itemError("control characters in ics")
		}
	}
	if len(stack) != 0 {
		return "", itemError("unbalanced ics")
	}
	return strings.Join(lines, "\r\n"), nil
}
