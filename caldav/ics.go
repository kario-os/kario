package caldav

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// What the system reads of an iCalendar object (RFC 5545), and changes in
// one: its UID, whether it holds events, a copy under another UID, and the
// phone's edit of an event written into the server's object (Patch) so the
// properties and components the calendar app does not keep (attendees'
// details, categories, other programs' X- properties, e-mail alarms) stay
// as they are. Everything else of iCalendar is the calendar app's.

// component is a component read into its content lines (unfolded): its
// own properties in order, and the components in it.
type component struct {
	name  string
	props []string
	subs  []*component
}

var errNotCalendar = errors.New("not an iCalendar object")

const maxLines = 200000

// parse reads an object: one VCALENDAR (what is outside it is dropped).
func parse(data []byte) (*component, error) {
	text := strings.TrimPrefix(string(data), "\uFEFF")
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
		} else if l != "" {
			lines = append(lines, l)
		}
		if len(lines) > maxLines {
			return nil, errNotCalendar
		}
	}
	var stack []*component
	var root *component
	for _, l := range lines {
		n, v := propName(l), strings.ToUpper(strings.TrimSpace(propValue(l)))
		switch {
		case n == "BEGIN":
			c := &component{name: v}
			if len(stack) == 0 {
				if v != "VCALENDAR" || root != nil {
					return nil, errNotCalendar
				}
				root = c
			} else {
				stack[len(stack)-1].subs = append(stack[len(stack)-1].subs, c)
			}
			stack = append(stack, c)
			if len(stack) > 8 {
				return nil, errNotCalendar
			}
		case n == "END":
			if len(stack) == 0 || stack[len(stack)-1].name != v {
				return nil, errNotCalendar
			}
			stack = stack[:len(stack)-1]
		case len(stack) > 0:
			stack[len(stack)-1].props = append(stack[len(stack)-1].props, l)
		}
	}
	if root == nil || len(stack) > 0 {
		return nil, errNotCalendar
	}
	return root, nil
}

// propName is a content line's name, upper case.
func propName(l string) string {
	i := strings.IndexAny(l, ";:")
	if i < 0 {
		return strings.ToUpper(l)
	}
	return strings.ToUpper(l[:i])
}

// valueAt is where a content line's value starts: after the first colon
// outside a quoted parameter.
func valueAt(l string) int {
	quoted := false
	for i := 0; i < len(l); i++ {
		switch l[i] {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				return i + 1
			}
		}
	}
	return len(l)
}

func propValue(l string) string { return l[valueAt(l):] }

// param is a parameter's value ("" for none), its quotes taken off.
func param(l, name string) string {
	head := l[:max(0, valueAt(l)-1)]
	for _, p := range splitParams(head)[1:] {
		k, v, _ := strings.Cut(p, "=")
		if strings.EqualFold(k, name) {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// splitParams splits a line's head (name and parameters) at the semicolons
// outside quotes.
func splitParams(head string) []string {
	var out []string
	quoted, from := false, 0
	for i := 0; i < len(head); i++ {
		switch head[i] {
		case '"':
			quoted = !quoted
		case ';':
			if !quoted {
				out = append(out, head[from:i])
				from = i + 1
			}
		}
	}
	return append(out, head[from:])
}

// setParam is the line with a parameter set to v (added when missing).
func setParam(l, name, v string) string {
	at := valueAt(l)
	head, value := l[:max(0, at-1)], l[at:]
	parts := splitParams(head)
	done := false
	for i, p := range parts[1:] {
		if k, _, _ := strings.Cut(p, "="); strings.EqualFold(k, name) {
			parts[i+1], done = name+"="+v, true
		}
	}
	if !done {
		parts = append(parts, name+"="+v)
	}
	return strings.Join(parts, ";") + ":" + value
}

func (c *component) write(b *bytes.Buffer) {
	b.WriteString(fold("BEGIN:" + c.name))
	for _, l := range c.props {
		b.WriteString(fold(l))
	}
	for _, s := range c.subs {
		s.write(b)
	}
	b.WriteString(fold("END:" + c.name))
}

func (c *component) bytes() []byte {
	var b bytes.Buffer
	c.write(&b)
	return b.Bytes()
}

// fold is a content line folded at 75 octets (never inside a character),
// with its CRLF.
func fold(l string) string {
	var b strings.Builder
	n := 0
	for _, r := range l {
		w := utf8.RuneLen(r)
		if w < 0 {
			w = 3
		}
		if n+w > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += w
	}
	b.WriteString("\r\n")
	return b.String()
}

func (c *component) get(name string) string {
	for _, l := range c.props {
		if propName(l) == name {
			return l
		}
	}
	return ""
}

func (c *component) events() []*component {
	var out []*component
	for _, s := range c.subs {
		if s.name == "VEVENT" {
			out = append(out, s)
		}
	}
	return out
}

// UID is the UID of an object's first event ("" for none or not an object).
func UID(data []byte) string {
	c, err := parse(data)
	if err != nil {
		return ""
	}
	for _, e := range c.events() {
		if l := e.get("UID"); l != "" {
			return strings.TrimSpace(propValue(l))
		}
	}
	return ""
}

// Method is an iTIP message's METHOD (RFC 5546), upper case ("" for none).
func Method(data []byte) string {
	c, err := parse(data)
	if err != nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(propValue(c.get("METHOD"))))
}

// Invitation: an event of the object has an ORGANIZER (one the user
// organizes or attends).
func Invitation(data []byte) bool {
	c, err := parse(data)
	if err != nil {
		return false
	}
	for _, e := range c.events() {
		if e.get("ORGANIZER") != "" {
			return true
		}
	}
	return false
}

// isMine: an address is one of the user's (me), "mailto:" and case aside.
func isMine(addr string, me []string) bool {
	a := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(addr)), "mailto:")
	return slices.ContainsFunc(me, func(m string) bool { return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(m)), "mailto:") == a })
}

// Attends: the user (me) is an attendee of an invitation someone else
// organizes.
func Attends(data []byte, me []string) bool {
	c, err := parse(data)
	if err != nil {
		return false
	}
	for _, e := range c.events() {
		if o := e.get("ORGANIZER"); o == "" || isMine(propValue(o), me) {
			return false
		}
		for _, l := range e.props {
			if propName(l) == "ATTENDEE" && isMine(propValue(l), me) {
				return true
			}
		}
	}
	return false
}

// Answer is the user's answer to an invitation (phone, the calendar app's
// writing of it) put into the organizer's event as the server has it
// (server): on each of the server's events, the user's attendee line (me)
// takes the PARTSTAT of the phone's event for the same occurrence, and the
// alarms the phone shows (display and sound) replace the server's; all
// else stays the organizer's, which an attendee does not change (RFC 6638
// §3.2.2.1).
func Answer(server, phone []byte, me []string) ([]byte, error) {
	p, err := parse(phone)
	if err != nil {
		return nil, err
	}
	s, err := parse(server)
	if err != nil {
		return nil, err
	}
	ours := map[string]*component{}
	for _, e := range p.events() {
		ours[occurrenceKey(e)] = e
	}
	for _, e := range s.events() {
		pe := ours[occurrenceKey(e)]
		if pe == nil {
			continue
		}
		answer := ""
		for _, l := range pe.props {
			if propName(l) == "ATTENDEE" && isMine(propValue(l), me) {
				answer = param(l, "PARTSTAT")
			}
		}
		for i, l := range e.props {
			if propName(l) == "ATTENDEE" && isMine(propValue(l), me) && answer != "" && !strings.EqualFold(param(l, "PARTSTAT"), answer) {
				e.props[i] = setParam(l, "PARTSTAT", answer)
			}
		}
		e.subs = slices.DeleteFunc(e.subs, func(c *component) bool {
			a := strings.ToUpper(strings.TrimSpace(propValue(c.get("ACTION"))))
			return c.name == "VALARM" && (a == "DISPLAY" || a == "AUDIO")
		})
		for _, sub := range pe.subs {
			if sub.name == "VALARM" {
				e.subs = append(e.subs, sub)
			}
		}
	}
	s.props = slices.DeleteFunc(s.props, func(l string) bool { return propName(l) == "METHOD" })
	return s.bytes(), nil
}

// HasEvents reports whether the object is one with events (not a task's
// or a journal's).
func HasEvents(data []byte) bool {
	c, err := parse(data)
	return err == nil && len(c.events()) > 0
}

// Clean is an object of the calendar app's as a calendar holds it: every
// event's UID uid, no METHOD (RFC 4791 §4.1).
func Clean(data []byte, uid string) ([]byte, error) {
	c, err := parse(data)
	if err != nil {
		return nil, err
	}
	if len(c.events()) == 0 {
		return nil, errNotCalendar
	}
	c.props = slices.DeleteFunc(c.props, func(l string) bool { return propName(l) == "METHOD" })
	for _, e := range c.events() {
		if strings.TrimSpace(propValue(e.get("UID"))) != uid {
			return nil, errors.New("an event of another UID")
		}
	}
	return c.bytes(), nil
}

// WithUID is the object with every event's UID uid: a copy of it.
func WithUID(data []byte, uid string) []byte {
	c, err := parse(data)
	if err != nil {
		return data
	}
	for _, e := range c.events() {
		for i, l := range e.props {
			if propName(l) == "UID" {
				e.props[i] = "UID:" + uid
			}
		}
	}
	return c.bytes()
}

// The properties of an event the calendar app shows and edits: the phone's
// edit replaces them; the server's others stay.
var phoneOwned = map[string]bool{"DTSTART": true, "DTEND": true, "DURATION": true, "SUMMARY": true, "LOCATION": true,
	"DESCRIPTION": true, "RRULE": true, "RDATE": true, "EXDATE": true, "RECURRENCE-ID": true}

// Those of them that, changed, make it another event for the people
// invited (SEQUENCE goes up, RFC 5546 §2.1.4).
var timing = []string{"DTSTART", "DTEND", "DURATION", "RRULE", "RDATE", "EXDATE", "RECURRENCE-ID"}

// MaxAttendees is the most attendees the calendar app keeps of an event
// (its MAX_ATTENDEES): an event of the phone's with an organizer and fewer
// has all it read, so one it lacks was taken out on the phone.
const MaxAttendees = 50

// occurrenceKey is the occurrence an event changes (its RECURRENCE-ID as
// an instant where the zone is known; "" for the repeating event itself),
// so the phone's and the server's are matched however each writes it.
func occurrenceKey(e *component) string {
	l := e.get("RECURRENCE-ID")
	if l == "" {
		return ""
	}
	v := strings.TrimSpace(propValue(l))
	tz := param(l, "TZID")
	if len(v) == 8 {
		return "D" + v
	}
	if t, err := time.Parse("20060102T150405Z", v); err == nil {
		return "U" + strconv.FormatInt(t.Unix(), 10)
	}
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			if t, err := time.ParseInLocation("20060102T150405", v, loc); err == nil {
				return "U" + strconv.FormatInt(t.Unix(), 10)
			}
		}
		return "L" + tz + "|" + v
	}
	return "F" + v
}

func address(l string) string { return strings.ToLower(strings.TrimSpace(propValue(l))) }

func lines(c *component, names ...string) []string {
	var out []string
	for _, l := range c.props {
		if slices.Contains(names, propName(l)) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

// Patch is the phone's edit of an object (phone, the calendar app's
// writing of it) put into the server's (server): each of the phone's
// events takes, from the server's event for the same occurrence, the
// properties the phone does not own and the alarms it does not show
// (e-mail ones), and attendees as the server has them (their other
// parameters too), with the phone's answer (PARTSTAT) and SCHEDULE-AGENT
// where it changed one, the ones the phone added, and without those it
// took out (an organizer's edit: the phone has the whole list); the
// server's events the phone no longer has go; the server's VTIMEZONEs and
// calendar properties stay. An unreadable server object is replaced by
// the phone's.
func Patch(server, phone []byte, now time.Time) ([]byte, error) {
	return patch(server, phone, now, nil)
}

// PatchScheduled is Patch where the server schedules: the attendees'
// answers are the server's (their replies put them there), but for the
// user's own (me).
func PatchScheduled(server, phone []byte, now time.Time, me []string) ([]byte, error) {
	return patch(server, phone, now, me)
}

func patch(server, phone []byte, now time.Time, me []string) ([]byte, error) {
	p, err := parse(phone)
	if err != nil {
		return nil, err
	}
	s, err := parse(server)
	if err != nil {
		return p.bytes(), nil
	}
	stamp := now.UTC().Format("20060102T150405Z")
	out := &component{name: "VCALENDAR", props: slices.DeleteFunc(slices.Clone(s.props), func(l string) bool { return propName(l) == "METHOD" })}
	zones := map[string]bool{}
	theirs := map[string]*component{}
	for _, sub := range s.subs {
		switch sub.name {
		case "VEVENT":
			theirs[occurrenceKey(sub)] = sub
		case "VTIMEZONE":
			zones[strings.TrimSpace(propValue(sub.get("TZID")))] = true
			out.subs = append(out.subs, sub)
		default:
			out.subs = append(out.subs, sub)
		}
	}
	for _, sub := range p.subs {
		if sub.name == "VTIMEZONE" && !zones[strings.TrimSpace(propValue(sub.get("TZID")))] {
			out.subs = append(out.subs, sub)
		}
	}
	for _, e := range p.events() {
		k := occurrenceKey(e)
		if se := theirs[k]; se != nil {
			out.subs = append(out.subs, mergeEvent(se, e, stamp, me))
			delete(theirs, k)
		} else {
			out.subs = append(out.subs, e)
		}
	}
	return out.bytes(), nil
}

func mergeEvent(se, pe *component, stamp string, me []string) *component {
	out := &component{name: "VEVENT"}
	mine := map[string]string{}
	for _, l := range pe.props {
		if propName(l) == "ATTENDEE" {
			mine[address(l)] = l
		}
	}
	whole := pe.get("ORGANIZER") != "" && len(mine) < MaxAttendees
	theirAttendees := map[string]bool{}
	seq := 0
	if uid := pe.get("UID"); uid != "" {
		out.props = append(out.props, uid)
	}
	for _, l := range se.props {
		n := propName(l)
		switch {
		case phoneOwned[n], n == "DTSTAMP", n == "LAST-MODIFIED", n == "UID" && pe.get("UID") != "":
		case n == "SEQUENCE":
			seq, _ = strconv.Atoi(strings.TrimSpace(propValue(l)))
		case n == "ATTENDEE":
			pl, kept := mine[address(l)]
			if whole && !kept {
				continue
			}
			theirAttendees[address(l)] = true
			for _, p := range []string{"PARTSTAT", "SCHEDULE-AGENT"} {
				if p == "PARTSTAT" && me != nil && !isMine(address(l), me) {
					continue
				}
				if v := param(pl, p); v != "" && !strings.EqualFold(v, param(l, p)) {
					l = setParam(l, p, v)
				}
			}
			out.props = append(out.props, l)
		default:
			out.props = append(out.props, l)
		}
	}
	for _, l := range pe.props {
		n := propName(l)
		switch {
		case phoneOwned[n]:
			out.props = append(out.props, l)
		case n == "ATTENDEE" && !theirAttendees[address(l)]:
			out.props = append(out.props, l)
		case n == "ORGANIZER" && se.get("ORGANIZER") == "":
			out.props = append(out.props, l)
		}
	}
	if !slices.Equal(lines(se, timing...), lines(pe, timing...)) {
		seq++
	}
	out.props = append(out.props, "DTSTAMP:"+stamp, "LAST-MODIFIED:"+stamp)
	if seq > 0 {
		out.props = append(out.props, "SEQUENCE:"+strconv.Itoa(seq))
	}
	for _, sub := range se.subs {
		if sub.name == "VALARM" {
			a := strings.ToUpper(strings.TrimSpace(propValue(sub.get("ACTION"))))
			if a == "DISPLAY" || a == "AUDIO" {
				continue
			}
		}
		out.subs = append(out.subs, sub)
	}
	for _, sub := range pe.subs {
		if sub.name == "VALARM" {
			out.subs = append(out.subs, sub)
		}
	}
	return out
}
