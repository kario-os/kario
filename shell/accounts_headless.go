package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	caltest "github.com/kario-os/kario/caldav/davtest"
	"github.com/kario-os/kario/carddav/davtest"
)

// The contacts accounts' selftest steps (headless.go): a CardDAV and CalDAV
// server in the Shell's process (packages carddav/davtest and
// caldav/davtest, on 127.0.0.1) as the product's Kario account server,
// another device's edits on it, and the network the sync sees.

type carddavSim struct {
	srv   *davtest.Server
	cal   *caltest.Server
	ts    *httptest.Server
	saved string
	// theft: the anti-theft lock's API at the same address
	// (reset_headless.go).
	theft http.Handler
}

// accountSim is the Kario account server on one address as the Shell sees
// it: the calendar's requests (its paths, and a PROPFIND asking for
// calendar-home-set, which CalDAV's discovery sends) to the CalDAV server,
// the rest to the CardDAV one.
func accountSim(card, cal http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/caldav" || strings.HasPrefix(r.URL.Path, caltest.Home) {
			cal.ServeHTTP(w, r)
			return
		}
		if r.Method == "PROPFIND" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte("calendar-home-set")) {
				cal.ServeHTTP(w, r)
				return
			}
		}
		card.ServeHTTP(w, r)
	})
}

// simCard is the server's card at the start: a contact with what the
// phone does not show, a photo that is no picture, an e-mail address and
// a group (CATEGORIES).
const simCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:kario-selftest-lei\r\nFN:\u4e91\u7aef\u674e\u96f7\r\nN:\u674e;\u96f7;;;\r\n" +
	"TEL;TYPE=CELL:+86 139 1234 0000\r\nEMAIL;TYPE=INTERNET:lei@kario.test\r\nPHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQSkZJRgABAQAAAQABAAD\r\n" +
	"CATEGORIES:\u540c\u4e8b\r\nX-KARIO-SELFTEST:keep\r\nEND:VCARD\r\n"

func (r *Runner) accountStep(cmd, arg string) error {
	c := r.C
	a := &c.accounts
	arg = strings.TrimSpace(arg)
	if cmd != "carddav-server" && cmd != "carddav-contact" && cmd != "carddav-group" && a.sim == nil {
		return errors.New("no carddav-server")
	}
	switch cmd {
	case "carddav-server":
		if a.sim != nil {
			a.sim.ts.Close()
			c.cfg.AccountURL = a.sim.saved
			a.sim = nil
		}
		if arg == "off" {
			return nil
		}
		f := strings.Fields(arg)
		if len(f) != 2 {
			return fmt.Errorf("usage: carddav-server <user> <password> | off")
		}
		srv := davtest.New(f[0], f[1])
		srv.Put("lei.vcf", []byte(simCard))
		cal := caltest.New(f[0], f[1])
		sim := &carddavSim{srv: srv, cal: cal, saved: c.cfg.AccountURL}
		dav := accountSim(srv, cal)
		sim.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if h := sim.theft; h != nil && strings.HasPrefix(req.URL.Path, "/v1/antitheft/") {
				h.ServeHTTP(w, req)
				return
			}
			dav.ServeHTTP(w, req)
		}))
		a.sim = sim
		c.cfg.AccountURL = a.sim.ts.URL
	case "carddav-book":
		name, display, ok := strings.Cut(arg, " ")
		if !ok {
			return fmt.Errorf("usage: carddav-book <name> <displayname>")
		}
		a.sim.srv.AddBook(name, strings.TrimSpace(display))
	case "carddav-card":
		book, rest := simBook(arg)
		name, text, ok := strings.Cut(rest, " ")
		if !ok {
			return fmt.Errorf("usage: carddav-card [@<book>] <name> <vCard, \\n between lines>")
		}
		a.sim.srv.PutIn(book, name, []byte(strings.ReplaceAll(text, `\n`, "\r\n")+"\r\n"))
	case "carddav-has":
		book, rest := simBook(arg)
		rest, ms := lastNum(rest, 8000)
		i := strings.LastIndexByte(rest, ' ')
		if i < 0 || (rest[i+1:] != "present" && rest[i+1:] != "absent") {
			return fmt.Errorf("usage: carddav-has [@<book>] <text, \\n a line break> present|absent [ms]")
		}
		text, want := strings.TrimSpace(rest[:i]), rest[i+1:] == "present"
		t := strings.ReplaceAll(text, `\n`, "\r\n")
		if _, ok := r.waitFor(func(string) bool { _, ok := a.sim.srv.FindIn(book, t); return ok == want }, ms); !ok {
			return fmt.Errorf("the server's cards: %q present is %v", text, !want)
		}
	case "caldav-event":
		name, text, ok := strings.Cut(arg, " ")
		if !ok {
			return fmt.Errorf("usage: caldav-event <name> <iCalendar, \\n between lines>")
		}
		a.sim.cal.Put(name, []byte(strings.ReplaceAll(text, `\n`, "\r\n")+"\r\n"))
	case "caldav-delete":
		if !a.sim.cal.Delete(arg) {
			return fmt.Errorf("the calendar has no %s", arg)
		}
	case "caldav-has":
		rest, ms := lastNum(arg, 8000)
		i := strings.LastIndexByte(rest, ' ')
		if i < 0 || (rest[i+1:] != "present" && rest[i+1:] != "absent") {
			return fmt.Errorf("usage: caldav-has <text> present|absent [ms]")
		}
		text, want := strings.TrimSpace(rest[:i]), rest[i+1:] == "present"
		if _, ok := r.waitFor(func(string) bool { _, ok := a.sim.cal.Find(text); return ok == want }, ms); !ok {
			return fmt.Errorf("the server's calendar: %q present is %v", text, !want)
		}
	case "carddav-net":
		switch arg {
		case "real":
			a.net = nil
		case "offline", "wifi", "mobile":
			online, metered := arg != "offline", arg == "mobile"
			a.net = func(then func(bool, bool)) { then(online, metered) }
		default:
			return fmt.Errorf("usage: carddav-net offline|wifi|mobile|real")
		}
		// As the network's events tell the Shell (netwatch.go).
		c.accountsNetChanged()
	case "carddav-contact":
		rest, ms := lastNum(arg, 8000)
		name, text, _ := strings.Cut(rest, "|")
		name, text = strings.TrimSpace(name), strings.TrimSpace(text)
		var got string
		if _, ok := r.waitFor(func(string) bool {
			got = "none"
			for _, x := range c.contacts() {
				if x.Name == name {
					got = contactLines(x) + "\n" + x.Book
					return text != "absent" && strings.Contains(got, text)
				}
			}
			return text == "absent"
		}, ms); !ok {
			return fmt.Errorf("the phone's contact %q: %s", name, got)
		}
	case "carddav-group":
		rest, ms := lastNum(arg, 8000)
		name, want, _ := strings.Cut(rest, "|")
		name, want = strings.TrimSpace(name), strings.TrimSpace(want)
		var got string
		if _, ok := r.waitFor(func(string) bool {
			g, ok := c.contactBook().GroupNamed(name)
			if !ok {
				got = "absent"
				return want == "absent"
			}
			var names []string
			for _, id := range g.Members {
				if x, ok := c.contactBook().Get(id); ok {
					names = append(names, x.Name)
				}
			}
			slices.Sort(names)
			got = strings.Join(names, ",")
			return got == want
		}, ms); !ok {
			return fmt.Errorf("the phone's group %q: %s", name, got)
		}
	}
	return nil
}

// simBook is the book a step names first (@<book>), MainBook without, and
// the rest of the step.
func simBook(arg string) (book, rest string) {
	if name, more, ok := strings.Cut(arg, " "); ok && strings.HasPrefix(name, "@") {
		return name[1:], strings.TrimSpace(more)
	}
	return davtest.MainBook, arg
}
