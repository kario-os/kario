// Package davtest is a CalDAV server in memory to test the client against
// (package caldav's tests, the Shell's): one user, one calendar for events
// laid out as the Kario account server lays out its own
// (/.well-known/caldav, /dav/, a principal, a calendar home, the calendar),
// beside a read-only calendar and one of tasks only that discovery must
// pass over; sync tokens with tombstones, sync-collection in pages,
// calendar-multiget, GET, PUT and DELETE with strong ETags and If-Match /
// If-None-Match, one UID per object. Switches make it behave as other
// servers do (no sync-collection, rewriting what it stores) or badly
// (down, a sync token forgotten, another device's edit between a read and
// a write).
package davtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	nsDAV = "DAV:"
	nsCal = "urn:ietf:params:xml:ns:caldav"
)

// The server's paths.
const (
	Root      = "/dav/"
	Principal = "/dav/principals/user/"
	Home      = "/dav/calendars/user/"
	Calendar  = "/dav/calendars/user/calendar/"
)

type object struct {
	data []byte
	etag string
	seq  int
}

// Server is the server. Its exported fields are the switches; set them
// before the requests that should see them, or under Lock.
type Server struct {
	mu   sync.Mutex
	User string
	Pass string
	// NoSync: no sync-collection (the client compares ETags); Rewrite: the
	// server changes an object as it stores it (it adds an X- property)
	// and answers a PUT without an ETag; MaxSize: max-resource-size (0:
	// none); Quota: at most that many objects (0: no limit).
	NoSync  bool
	Rewrite bool
	MaxSize int
	Quota   int
	// Down answers every request 503 with Retry-After: RetryAfter seconds.
	Down       bool
	RetryAfter int
	// BeforeWrite runs (unlocked) before a PUT or DELETE of an object is
	// checked: another device's edit in between.
	BeforeWrite func(name string)
	// Requests counts requests by kind ("PUT", "REPORT sync").
	Requests map[string]int

	epoch   int
	seq     int
	objects map[string]*object
	deleted map[string]int
}

// New is a server for one user.
func New(user, pass string) *Server {
	return &Server{User: user, Pass: pass, Requests: map[string]int{}, epoch: 1, objects: map[string]*object{}, deleted: map[string]int{}}
}

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Lock and Unlock hold the server still while a test reads or sets it.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

// Put stores an object as another client would (no conditions).
func (s *Server) Put(name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(name, data)
}

func (s *Server) put(name string, data []byte) {
	s.seq++
	s.objects[name] = &object{data: append([]byte(nil), data...), etag: etagOf(data), seq: s.seq}
	delete(s.deleted, name)
}

// Delete deletes an object as another client would.
func (s *Server) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove(name)
}

func (s *Server) remove(name string) bool {
	if _, ok := s.objects[name]; !ok {
		return false
	}
	s.seq++
	delete(s.objects, name)
	s.deleted[name] = s.seq
	return true
}

// Object is an object's content (nil without one).
func (s *Server) Object(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.objects[name]; o != nil {
		return append([]byte(nil), o.data...)
	}
	return nil
}

// Objects are all the objects, by name.
func (s *Server) Objects() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for n, o := range s.objects {
		out[n] = append([]byte(nil), o.data...)
	}
	return out
}

// Find is the name of the first object (by name) whose content holds text.
func (s *Server) Find(text string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.names() {
		if bytes.Contains(s.objects[n].data, []byte(text)) {
			return n, true
		}
	}
	return "", false
}

// ForgetTokens makes every sync token given so far invalid.
func (s *Server) ForgetTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	s.deleted = map[string]int{}
}

// Count is how many requests of that kind came.
func (s *Server) Count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Requests[key]
}

func (s *Server) count(key string) {
	s.mu.Lock()
	s.Requests[key]++
	s.mu.Unlock()
}

func (s *Server) token() string {
	return fmt.Sprintf("http://davtest.invalid/calsync/%d/%d", s.epoch, s.seq)
}

func (s *Server) since(token string) (int, bool) {
	if token == "" {
		return 0, true
	}
	rest, ok := strings.CutPrefix(token, "http://davtest.invalid/calsync/")
	e, n, _ := strings.Cut(rest, "/")
	seq, err := strconv.Atoi(n)
	if !ok || err != nil || e != strconv.Itoa(s.epoch) || seq > s.seq {
		return 0, false
	}
	return seq, true
}

func (s *Server) names() []string {
	var out []string
	for n := range s.objects {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	down, wait := s.Down, s.RetryAfter
	s.mu.Unlock()
	if down {
		s.count("down")
		if wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(wait))
		}
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/.well-known/caldav" {
		s.count("well-known")
		http.Redirect(w, r, Root, http.StatusMovedPermanently)
		return
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != s.User || pass != s.Pass {
		s.count("unauthorized")
		w.Header().Set("WWW-Authenticate", `Basic realm="davtest"`)
		http.Error(w, "sign in", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	p := r.URL.Path
	name, inCal := strings.CutPrefix(p, Calendar)
	switch {
	case r.Method == "PROPFIND":
		s.count("PROPFIND")
		s.propfind(w, r, p)
	case r.Method == "REPORT" && p == Calendar:
		s.report(w, body)
	case inCal && name != "" && !strings.Contains(name, "/"):
		name, _ = url.PathUnescape(name)
		switch r.Method {
		case http.MethodGet:
			s.count("GET")
			s.get(w, name)
		case http.MethodPut:
			s.count("PUT")
			s.write(w, r, name, body)
		case http.MethodDelete:
			s.count("DELETE")
			s.write(w, r, name, nil)
		default:
			http.Error(w, "no", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

type multistatus struct{ b strings.Builder }

func newMS() *multistatus {
	m := &multistatus{}
	m.b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="` + nsCal + `" xmlns:CS="http://calendarserver.org/ns/">`)
	return m
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (m *multistatus) props(href, props string) {
	m.b.WriteString("<D:response><D:href>" + esc(href) + "</D:href><D:propstat><D:prop>" + props +
		"</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>")
}

func (m *multistatus) status(href string, code int) {
	m.b.WriteString(fmt.Sprintf("<D:response><D:href>%s</D:href><D:status>HTTP/1.1 %d %s</D:status></D:response>", esc(href), code, http.StatusText(code)))
}

func (m *multistatus) send(w http.ResponseWriter, tail string) {
	m.b.WriteString(tail + "</D:multistatus>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, m.b.String())
}

func (s *Server) calendarProps() string {
	reports := "<D:supported-report><D:report><C:calendar-multiget/></D:report></D:supported-report>"
	token := ""
	if !s.NoSync {
		reports += "<D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>"
		token = "<D:sync-token>" + esc(s.token()) + "</D:sync-token>"
	}
	size := ""
	if s.MaxSize > 0 {
		size = "<C:max-resource-size>" + strconv.Itoa(s.MaxSize) + "</C:max-resource-size>"
	}
	return "<D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>Calendar</D:displayname>" +
		"<D:current-user-privilege-set><D:privilege><D:read/></D:privilege><D:privilege><D:write/></D:privilege></D:current-user-privilege-set>" +
		"<D:supported-report-set>" + reports + "</D:supported-report-set>" + token +
		"<CS:getctag>" + strconv.Itoa(s.epoch) + "-" + strconv.Itoa(s.seq) + "</CS:getctag>" +
		`<C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>` + size
}

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	depth := r.Header.Get("Depth")
	m := newMS()
	principal := "<D:current-user-principal><D:href>" + Principal + "</D:href></D:current-user-principal>"
	switch p {
	case "/", Root:
		m.props(p, "<D:resourcetype><D:collection/></D:resourcetype>"+principal)
	case Principal:
		m.props(p, "<D:resourcetype><D:principal/></D:resourcetype>"+principal+"<C:calendar-home-set><D:href>"+Home+"</D:href></C:calendar-home-set>")
	case Home:
		m.props(p, "<D:resourcetype><D:collection/></D:resourcetype>")
		if depth == "1" {
			// A read-only calendar of the system and one of tasks come
			// first by name and must not be chosen.
			m.props(Home+"a-birthdays/", "<D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>Birthdays</D:displayname>"+
				"<D:current-user-privilege-set><D:privilege><D:read/></D:privilege></D:current-user-privilege-set>")
			m.props(Home+"b-tasks/", "<D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>Tasks</D:displayname>"+
				`<C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set>`)
			m.props(Calendar, s.calendarProps())
		}
	case Calendar:
		m.props(Calendar, s.calendarProps())
		if depth == "1" {
			for _, n := range s.names() {
				m.props(Calendar+url.PathEscape(n), `<D:resourcetype/><D:getetag>"`+s.objects[n].etag+`"</D:getetag>`)
			}
		}
	default:
		name, ok := strings.CutPrefix(p, Calendar)
		o := s.objects[name]
		if !ok || o == nil {
			http.NotFound(w, r)
			return
		}
		m.props(p, `<D:resourcetype/><D:getetag>"`+o.etag+`"</D:getetag>`)
	}
	m.send(w, "")
}

type reportReq struct {
	XMLName xml.Name
	Token   string   `xml:"DAV: sync-token"`
	Limit   string   `xml:"DAV: limit>nresults"`
	Hrefs   []string `xml:"DAV: href"`
}

func (s *Server) report(w http.ResponseWriter, body []byte) {
	var req reportReq
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case req.XMLName.Space == nsCal && req.XMLName.Local == "calendar-multiget":
		s.Requests["REPORT multiget"]++
		m := newMS()
		for _, h := range req.Hrefs {
			u, _ := url.Parse(strings.TrimSpace(h))
			name, _ := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), Calendar))
			o := s.objects[name]
			if o == nil {
				m.status(h, http.StatusNotFound)
				continue
			}
			m.props(h, `<D:getetag>"`+o.etag+`"</D:getetag><C:calendar-data>`+esc(string(o.data))+`</C:calendar-data>`)
		}
		m.send(w, "")
	case req.XMLName.Space == nsDAV && req.XMLName.Local == "sync-collection" && !s.NoSync:
		s.Requests["REPORT sync"]++
		since, ok := s.since(strings.TrimSpace(req.Token))
		if !ok {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><D:error xmlns:D="DAV:"><D:valid-sync-token/></D:error>`)
			return
		}
		type change struct {
			name string
			seq  int
			gone bool
		}
		var ch []change
		for n, o := range s.objects {
			if o.seq > since {
				ch = append(ch, change{n, o.seq, false})
			}
		}
		if since > 0 {
			for n, seq := range s.deleted {
				if seq > since {
					ch = append(ch, change{n, seq, true})
				}
			}
		}
		sort.Slice(ch, func(i, j int) bool { return ch[i].seq < ch[j].seq })
		token := s.token()
		limit, _ := strconv.Atoi(strings.TrimSpace(req.Limit))
		more := limit > 0 && len(ch) > limit
		if more {
			ch = ch[:limit]
			token = fmt.Sprintf("http://davtest.invalid/calsync/%d/%d", s.epoch, ch[limit-1].seq)
		}
		m := newMS()
		for _, c := range ch {
			href := Calendar + url.PathEscape(c.name)
			if c.gone {
				m.status(href, http.StatusNotFound)
			} else {
				m.props(href, `<D:getetag>"`+s.objects[c.name].etag+`"</D:getetag>`)
			}
		}
		if more {
			m.status(Calendar, http.StatusInsufficientStorage)
		}
		m.send(w, "<D:sync-token>"+esc(token)+"</D:sync-token>")
	default:
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><D:error xmlns:D="DAV:"><D:supported-report/></D:error>`)
	}
}

func (s *Server) get(w http.ResponseWriter, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objects[name]
	if o == nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("ETag", `"`+o.etag+`"`)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	_, _ = w.Write(o.data)
}

func matches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || t == `"`+etag+`"` {
			return true
		}
	}
	return false
}

var uidLine = regexp.MustCompile(`(?m)^UID:(.*?)\r?$`)

// uidOf is an object's UID ("" when its events have none or several).
func uidOf(data []byte) string {
	uid := ""
	for _, m := range uidLine.FindAllSubmatch(data, -1) {
		v := strings.TrimSpace(string(m[1]))
		if uid != "" && v != uid {
			return ""
		}
		uid = v
	}
	return uid
}

// write is a PUT (data) or a DELETE (nil) of an object.
func (s *Server) write(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	s.mu.Lock()
	before := s.BeforeWrite
	s.mu.Unlock()
	if before != nil {
		before(name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objects[name]
	im, inm := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if im != "" && (o == nil || !matches(im, o.etag)) || inm != "" && o != nil && matches(inm, o.etag) {
		http.Error(w, "the object changed, or is there already", http.StatusPreconditionFailed)
		return
	}
	if data == nil {
		if !s.remove(name) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.MaxSize > 0 && len(data) > s.MaxSize {
		davError(w, http.StatusForbidden, "<C:max-resource-size/>")
		return
	}
	uid := uidOf(data)
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("BEGIN:VCALENDAR")) || !bytes.Contains(data, []byte("BEGIN:VEVENT")) ||
		bytes.Contains(data, []byte("\nMETHOD:")) || uid == "" {
		davError(w, http.StatusForbidden, "<C:valid-calendar-object-resource/>")
		return
	}
	for n, x := range s.objects {
		if n != name && uidOf(x.data) == uid {
			davError(w, http.StatusConflict, "<C:no-uid-conflict><D:href>"+esc(Calendar+url.PathEscape(n))+"</D:href></C:no-uid-conflict>")
			return
		}
	}
	if o == nil && s.Quota > 0 && len(s.objects) >= s.Quota {
		davError(w, http.StatusInsufficientStorage, "<D:quota-not-exceeded/>")
		return
	}
	if s.Rewrite {
		data = bytes.Replace(data, []byte("\r\nBEGIN:VEVENT\r\n"), []byte("\r\nBEGIN:VEVENT\r\nX-DAVTEST:stored\r\n"), 1)
	}
	s.put(name, data)
	if !s.Rewrite {
		w.Header().Set("ETag", `"`+s.objects[name].etag+`"`)
	}
	if o == nil {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func davError(w http.ResponseWriter, code int, cond string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `<?xml version="1.0"?><D:error xmlns:D="DAV:" xmlns:C="`+nsCal+`">`+cond+`</D:error>`)
}
