// Package davtest is a CardDAV server in memory to test the client
// against (package carddav's tests, the Shell's selftest): one user, an
// address book laid out as the Kario account server lays out its own
// (/.well-known/carddav, /dav/, a principal, a home, the book) and more
// books in the home as other servers have (AddBook), sync tokens with
// tombstones, sync-collection in pages, multiget, GET, PUT and DELETE with
// strong ETags and If-Match / If-None-Match. It keeps cards as they come,
// group cards and photos too; read-only books refuse writes (403), as the
// system book Nextcloud lists ("a-system", always there and empty unless a
// test fills it); other files (a card's photo at a URL) are served by
// path (PutFile). Switches make it behave as other servers do
// (no sync-collection, rewriting the cards it stores) or badly (down, a
// sync token forgotten, another device's edit between a read and a write).
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
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	nsDAV  = "DAV:"
	nsCard = "urn:ietf:params:xml:ns:carddav"
)

type card struct {
	data []byte
	etag string
	seq  int
}

// book is an address book: its displayname, cards and tombstones by name,
// and whether the user may only read it.
type book struct {
	name     string
	cards    map[string]*card
	deleted  map[string]int
	readOnly bool
}

func newBook(name string) *book {
	return &book{name: name, cards: map[string]*card{}, deleted: map[string]int{}}
}

// Server is the server. Its exported fields are the switches; set them
// before the requests that should see them, or under Lock.
type Server struct {
	mu   sync.Mutex
	User string
	Pass string
	// NoSync: no sync-collection (the client compares ETags); Rewrite: the
	// server changes a card as it stores it (it adds a PRODID) and answers
	// a PUT without an ETag; MaxSize: max-resource-size (0: none); Quota:
	// at most that many cards (0: no limit); Version: the vCard version the
	// book says it takes ("" both).
	NoSync  bool
	Rewrite bool
	MaxSize int
	Quota   int
	Version string
	// Down answers every request 503 with Retry-After: RetryAfter seconds.
	Down       bool
	RetryAfter int
	// BeforeWrite runs (unlocked) before a PUT or DELETE of a card is
	// checked: another device's edit in between.
	BeforeWrite func(name string)
	// Requests counts requests by method ("REPORT sync", "REPORT multiget").
	Requests map[string]int

	epoch   int
	seq     int
	horizon int // tokens before it miss forgotten deletions
	books   map[string]*book
	order   []string
	files   map[string][]byte
}

// New is a server for one user, with one book (Book, MainBook) and the
// read-only SystemBook.
func New(user, pass string) *Server {
	sys := newBook("System")
	sys.readOnly = true
	return &Server{User: user, Pass: pass, Requests: map[string]int{}, epoch: 1,
		books: map[string]*book{SystemBook: sys, MainBook: newBook("Contacts")}, order: []string{SystemBook, MainBook}, files: map[string][]byte{}}
}

// The server's paths. MainBook is the book Put and the others without a
// book use; BookPath is where a book is.
const (
	Root      = "/dav/"
	Principal = "/dav/principals/user/"
	Home      = "/dav/addressbooks/user/"
	Book      = "/dav/addressbooks/user/contacts/"
	MainBook  = "contacts"
	// SystemBook is read-only and first by path, as Nextcloud's system
	// book is: discovery must not take it for the user's own.
	SystemBook = "a-system"
)

func BookPath(name string) string { return Home + name + "/" }

// AddBook makes another address book in the home: name in its path,
// display its displayname.
func (s *Server) AddBook(name, display string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.books[name] == nil {
		s.books[name] = newBook(display)
		s.order = append(s.order, name)
	}
}

// AddReadOnlyBook makes an address book the user may only read.
func (s *Server) AddReadOnlyBook(name, display string) {
	s.AddBook(name, display)
	s.mu.Lock()
	s.books[name].readOnly = true
	s.mu.Unlock()
}

// PutFile serves data at path (absolute, outside the books) to the user.
func (s *Server) PutFile(path string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = append([]byte(nil), data...)
}

func (s *Server) main() *book { return s.books[MainBook] }

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Lock and Unlock hold the server still while a test reads or sets it.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

// Put stores a card in MainBook as another client would (no conditions).
func (s *Server) Put(name string, data []byte) { s.PutIn(MainBook, name, data) }

// PutIn stores a card in a book as another client would.
func (s *Server) PutIn(bookName, name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.books[bookName]; b != nil {
		s.put(b, name, data)
	}
}

func (s *Server) put(b *book, name string, data []byte) {
	s.seq++
	b.cards[name] = &card{data: append([]byte(nil), data...), etag: etagOf(data), seq: s.seq}
	delete(b.deleted, name)
}

// Delete deletes a card of MainBook as another client would.
func (s *Server) Delete(name string) bool { return s.DeleteIn(MainBook, name) }

func (s *Server) DeleteIn(bookName, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.books[bookName]
	return b != nil && s.remove(b, name)
}

func (s *Server) remove(b *book, name string) bool {
	if _, ok := b.cards[name]; !ok {
		return false
	}
	s.seq++
	delete(b.cards, name)
	b.deleted[name] = s.seq
	return true
}

// Card is a card's content in MainBook (nil without one).
func (s *Server) Card(name string) []byte { return s.CardIn(MainBook, name) }

func (s *Server) CardIn(bookName, name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.books[bookName]; b != nil && b.cards[name] != nil {
		return append([]byte(nil), b.cards[name].data...)
	}
	return nil
}

// Cards are all the cards of MainBook, by name.
func (s *Server) Cards() map[string][]byte { return s.CardsIn(MainBook) }

func (s *Server) CardsIn(bookName string) map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	if b := s.books[bookName]; b != nil {
		for n, c := range b.cards {
			out[n] = append([]byte(nil), c.data...)
		}
	}
	return out
}

// Find is the name of the first card (by name) of MainBook whose content
// holds text.
func (s *Server) Find(text string) (string, bool) { return s.FindIn(MainBook, text) }

func (s *Server) FindIn(bookName, text string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.books[bookName]
	if b == nil {
		return "", false
	}
	var names []string
	for n := range b.cards {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if bytes.Contains(b.cards[n].data, []byte(text)) {
			return n, true
		}
	}
	return "", false
}

// ForgetTokens makes every sync token given so far invalid (a server that
// lost its history: the client syncs from the start).
func (s *Server) ForgetTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	for _, b := range s.books {
		b.deleted = map[string]int{}
	}
}

func (s *Server) token() string {
	return fmt.Sprintf("http://davtest.invalid/sync/%d/%d", s.epoch, s.seq)
}

func (s *Server) since(token string) (int, bool) {
	if token == "" {
		return 0, true
	}
	rest, ok := strings.CutPrefix(token, "http://davtest.invalid/sync/")
	e, n, _ := strings.Cut(rest, "/")
	seq, err := strconv.Atoi(n)
	if !ok || err != nil || e != strconv.Itoa(s.epoch) || seq < s.horizon || seq > s.seq {
		return 0, false
	}
	return seq, true
}

func (s *Server) count(key string) {
	s.mu.Lock()
	s.Requests[key]++
	s.mu.Unlock()
}

// Count is how many requests of that kind came.
func (s *Server) Count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Requests[key]
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
	if r.URL.Path == "/.well-known/carddav" {
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
	bookName, name := s.split(p)
	s.mu.Lock()
	file, isFile := s.files[p]
	s.mu.Unlock()
	switch {
	case isFile && r.Method == http.MethodGet:
		s.count("GET file")
		_, _ = w.Write(file)
	case r.Method == "PROPFIND":
		s.count("PROPFIND")
		s.propfind(w, r, p, body)
	case r.Method == "REPORT" && bookName != "" && name == "":
		s.report(w, bookName, body)
	case bookName != "" && name != "" && !strings.Contains(name, "/"):
		name, _ = url.PathUnescape(name)
		switch r.Method {
		case http.MethodGet:
			s.count("GET")
			s.get(w, bookName, name)
		case http.MethodPut:
			s.count("PUT")
			s.write(w, r, bookName, name, body)
		case http.MethodDelete:
			s.count("DELETE")
			s.write(w, r, bookName, name, nil)
		default:
			http.Error(w, "no", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

// split is the book a path is in and the card's name ("" for the book
// itself); no book for a path in none.
func (s *Server) split(p string) (bookName, name string) {
	rest, ok := strings.CutPrefix(p, Home)
	if !ok {
		return "", ""
	}
	bookName, name, ok = strings.Cut(rest, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok || s.books[bookName] == nil {
		return "", ""
	}
	return bookName, name
}

type multistatus struct{ b strings.Builder }

func newMS() *multistatus {
	m := &multistatus{}
	m.b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="` + nsCard + `" xmlns:CS="http://calendarserver.org/ns/">`)
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

func (s *Server) bookProps(b *book) string {
	reports := "<D:supported-report><D:report><C:addressbook-multiget/></D:report></D:supported-report>"
	token := ""
	if !s.NoSync {
		reports += "<D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>"
		token = "<D:sync-token>" + esc(s.token()) + "</D:sync-token>"
	}
	types := `<C:address-data-type content-type="text/vcard" version="3.0"/><C:address-data-type content-type="text/vcard" version="4.0"/>`
	if s.Version != "" {
		types = `<C:address-data-type content-type="text/vcard" version="` + s.Version + `"/>`
	}
	size := ""
	if s.MaxSize > 0 {
		size = "<C:max-resource-size>" + strconv.Itoa(s.MaxSize) + "</C:max-resource-size>"
	}
	privileges := "<D:privilege><D:read/></D:privilege><D:privilege><D:write/></D:privilege>"
	if b.readOnly {
		privileges = "<D:privilege><D:read/></D:privilege>"
	}
	return "<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype><D:displayname>" + esc(b.name) + "</D:displayname>" +
		"<D:current-user-privilege-set>" + privileges + "</D:current-user-privilege-set>" +
		"<D:supported-report-set>" + reports + "</D:supported-report-set>" + token +
		"<CS:getctag>" + strconv.Itoa(s.epoch) + "-" + strconv.Itoa(s.seq) + "</CS:getctag>" +
		"<C:supported-address-data>" + types + "</C:supported-address-data>" + size
}

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, p string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	depth := r.Header.Get("Depth")
	m := newMS()
	principal := "<D:current-user-principal><D:href>" + Principal + "</D:href></D:current-user-principal>"
	switch p {
	case "/", Root:
		m.props(p, "<D:resourcetype><D:collection/></D:resourcetype>"+principal)
	case Principal:
		m.props(p, "<D:resourcetype><D:principal/></D:resourcetype>"+principal+"<C:addressbook-home-set><D:href>"+Home+"</D:href></C:addressbook-home-set>")
	case Home:
		m.props(p, "<D:resourcetype><D:collection/></D:resourcetype>")
		if depth == "1" {
			for _, n := range s.order {
				m.props(BookPath(n), s.bookProps(s.books[n]))
			}
		}
	default:
		rest, _ := strings.CutPrefix(p, Home)
		bookName, name, _ := strings.Cut(rest, "/")
		b := s.books[bookName]
		switch {
		case b != nil && name == "":
			m.props(p, s.bookProps(b))
			if depth == "1" {
				for _, n := range b.names() {
					m.props(p+url.PathEscape(n), `<D:resourcetype/><D:getetag>"`+b.cards[n].etag+`"</D:getetag>`)
				}
			}
		case b != nil && b.cards[name] != nil:
			m.props(p, `<D:resourcetype/><D:getetag>"`+b.cards[name].etag+`"</D:getetag>`)
		default:
			http.NotFound(w, r)
			return
		}
	}
	m.send(w, "")
}

func (b *book) names() []string {
	var out []string
	for n := range b.cards {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type reportReq struct {
	XMLName xml.Name
	Token   string   `xml:"DAV: sync-token"`
	Limit   string   `xml:"DAV: limit>nresults"`
	Hrefs   []string `xml:"DAV: href"`
}

func (s *Server) report(w http.ResponseWriter, bookName string, body []byte) {
	var req reportReq
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, path := s.books[bookName], BookPath(bookName)
	switch {
	case req.XMLName.Space == nsCard && req.XMLName.Local == "addressbook-multiget":
		s.Requests["REPORT multiget"]++
		m := newMS()
		for _, h := range req.Hrefs {
			u, _ := url.Parse(strings.TrimSpace(h))
			name, _ := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), path))
			c := b.cards[name]
			if c == nil {
				m.status(h, http.StatusNotFound)
				continue
			}
			m.props(h, `<D:getetag>"`+c.etag+`"</D:getetag><C:address-data>`+esc(string(c.data))+`</C:address-data>`)
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
		for n, c := range b.cards {
			if c.seq > since {
				ch = append(ch, change{n, c.seq, false})
			}
		}
		if since > 0 {
			for n, seq := range b.deleted {
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
			token = fmt.Sprintf("http://davtest.invalid/sync/%d/%d", s.epoch, ch[limit-1].seq)
		}
		m := newMS()
		for _, c := range ch {
			href := path + url.PathEscape(c.name)
			if c.gone {
				m.status(href, http.StatusNotFound)
			} else {
				m.props(href, `<D:getetag>"`+b.cards[c.name].etag+`"</D:getetag>`)
			}
		}
		if more {
			m.status(path, http.StatusInsufficientStorage)
		}
		m.send(w, "<D:sync-token>"+esc(token)+"</D:sync-token>")
	default:
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><D:error xmlns:D="DAV:"><D:supported-report/></D:error>`)
	}
}

func (s *Server) get(w http.ResponseWriter, bookName, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.books[bookName].cards[name]
	if c == nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("ETag", `"`+c.etag+`"`)
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	_, _ = w.Write(c.data)
}

// matches: an If-Match / If-None-Match list names etag ("*" any).
func matches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || t == `"`+etag+`"` {
			return true
		}
	}
	return false
}

// write is a PUT (data) or a DELETE (nil) of a card.
func (s *Server) write(w http.ResponseWriter, r *http.Request, bookName, name string, data []byte) {
	s.mu.Lock()
	before := s.BeforeWrite
	s.mu.Unlock()
	if before != nil {
		before(name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.books[bookName]
	if b.readOnly {
		s.Requests["refused"]++
		davError(w, http.StatusForbidden, "<D:need-privileges/>")
		return
	}
	c := b.cards[name]
	im, inm := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if im != "" && (c == nil || !matches(im, c.etag)) || inm != "" && c != nil && matches(inm, c.etag) {
		http.Error(w, "the card changed, or is there already", http.StatusPreconditionFailed)
		return
	}
	if data == nil {
		if !s.remove(b, name) {
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
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("BEGIN:VCARD")) || !bytes.Contains(data, []byte("\nUID:")) || !bytes.Contains(data, []byte("\nFN")) {
		davError(w, http.StatusForbidden, "<C:valid-address-data/>")
		return
	}
	if c == nil && s.Quota > 0 && len(b.cards) >= s.Quota {
		davError(w, http.StatusInsufficientStorage, "<D:quota-not-exceeded/>")
		return
	}
	if s.Rewrite {
		data = bytes.Replace(data, []byte("\r\nVERSION:"), []byte("\r\nPRODID:-//davtest//EN\r\nVERSION:"), 1)
	}
	s.put(b, name, data)
	if !s.Rewrite {
		w.Header().Set("ETag", `"`+b.cards[name].etag+`"`)
	}
	if c == nil {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func davError(w http.ResponseWriter, code int, cond string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `<?xml version="1.0"?><D:error xmlns:D="DAV:" xmlns:C="`+nsCard+`">`+cond+`</D:error>`)
}
