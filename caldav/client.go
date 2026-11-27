// Package caldav is the phone's CalDAV client (RFC 4791): it keeps the
// calendar app's events in two-way sync with one calendar on a server, the
// Kario account's or another that speaks CalDAV, with the same account and
// password as the contacts (package carddav, whose WebDAV requests, rules
// for credentials and errors it uses). client.go speaks the protocol,
// discover.go finds the calendar (RFC 6764, calendar-home-set) and whether
// its server schedules (RFC 6638), ics.go is what the system reads and
// changes of an iCalendar object, sync.go is the sync itself; the Shell
// runs it (shell/calsync.go) and hands the events to the calendar app and
// back (Queue). davtest is a server to test it against.
//
// It changes what the server has only with If-Match or If-None-Match, so
// an object another device changed in between is never written over: the
// server answers 412 and the sync keeps both (sync.go). No error it returns
// carries a URL, a user name or a password.
package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/kario-os/kario/carddav"
)

// Limits on what a server sends: an object the phone takes is at most
// MaxObject (what the calendar app takes of one event and its changed
// occurrences); a multistatus is at most MaxResponse.
const (
	MaxObject   = 512 << 10
	MaxResponse = carddav.MaxResponse
)

// ErrNoCalendar: the server answered, but has no calendar for events the
// user may write to.
var ErrNoCalendar = errors.New("the server has no calendar for this account")

// Client is a signed-in connection to one server: the contacts' client,
// whose sync-collection, PROPFIND of ETags, CTag and DELETE are WebDAV's
// and serve a calendar as they serve an address book.
type Client struct {
	*carddav.Client
}

// Object is a calendar object resource on the server: where (its path, as
// the server writes it), its ETag and, when read, its content.
type Object struct {
	Href string
	ETag string
	Data []byte
}

// fail is the error for an answer that is not what was asked: CalDAV's
// preconditions for a refused object first, then WebDAV's.
func fail(method string, r *carddav.Response) error {
	body := string(r.Body)
	switch {
	case r.Code >= 400 && r.Code < 500 && r.Code != http.StatusPreconditionFailed && (strings.Contains(body, "valid-calendar-data") ||
		strings.Contains(body, "valid-calendar-object-resource") || strings.Contains(body, "supported-calendar-component") ||
		strings.Contains(body, "supported-calendar-data") || strings.Contains(body, "no-uid-conflict")):
		return carddav.ErrRefused
	}
	return carddav.Fail(method, r)
}

func davHeader(depth string) http.Header {
	h := http.Header{}
	if depth != "" {
		h.Set("Depth", depth)
	}
	return h
}

// dav sends a PROPFIND or REPORT and reads its multistatus.
func (c *Client) dav(ctx context.Context, method string, u *url.URL, depth string, body []byte) (*multistatus, *url.URL, error) {
	r, err := c.Do(ctx, method, u, davHeader(depth), body, MaxResponse)
	if err != nil {
		return nil, nil, err
	}
	if r.Code != http.StatusMultiStatus {
		return nil, nil, fail(method, r)
	}
	ms, err := parseMultistatus(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: not a WebDAV answer: %w", method, err)
	}
	return ms, r.URL, nil
}

// Multiget reads objects by their paths (calendar-multiget). Missing are
// the paths the server has nothing at; Large those whose content is past
// MaxObject (left alone).
func (c *Client) Multiget(ctx context.Context, cal *url.URL, hrefs []string) (objs []Object, missing, large []string, err error) {
	ms, at, err := c.dav(ctx, "REPORT", cal, "1", multigetBody(hrefs))
	if err != nil {
		return nil, nil, nil, err
	}
	asked := map[string]string{}
	for _, h := range hrefs {
		asked[carddav.HrefKey(cal, h)] = h
	}
	for i := range ms.Responses {
		r := &ms.Responses[i]
		p, code := r.ok()
		for _, h := range r.Hrefs {
			k := carddav.HrefKey(cal, carddav.Resolve(at, h))
			want, ok := asked[k]
			if !ok {
				continue
			}
			delete(asked, k)
			switch {
			case code >= 200 && code < 300 && len(p.CalendarData) > MaxObject:
				large = append(large, want)
			case code >= 200 && code < 300 && p.CalendarData != "":
				objs = append(objs, Object{Href: want, ETag: carddav.NormETag(p.ETag), Data: []byte(p.CalendarData)})
			default:
				missing = append(missing, want)
			}
		}
	}
	for _, h := range asked {
		missing = append(missing, h)
	}
	return objs, missing, large, nil
}

// Get reads one object.
func (c *Client) Get(ctx context.Context, cal *url.URL, href string) (Object, error) {
	u, err := cal.Parse(href)
	if err != nil {
		return Object{}, err
	}
	r, err := c.Do(ctx, http.MethodGet, u, http.Header{"Accept": {"text/calendar"}}, nil, MaxObject)
	if err != nil {
		return Object{}, err
	}
	if r.Code != http.StatusOK {
		return Object{}, fail("GET", r)
	}
	return Object{Href: href, ETag: carddav.NormETag(r.Header.Get("ETag")), Data: r.Body}, nil
}

// Put writes an object: a new one (etag "", with If-None-Match: *) or over
// the one with that ETag (If-Match). The new ETag is "" when the server did
// not give a strong one (it changed the object as it stored it).
func (c *Client) Put(ctx context.Context, cal *url.URL, href string, data []byte, etag string) (string, error) {
	u, err := cal.Parse(href)
	if err != nil {
		return "", err
	}
	h := http.Header{"Content-Type": {"text/calendar; charset=utf-8"}}
	if etag == "" {
		h.Set("If-None-Match", "*")
	} else {
		h.Set("If-Match", carddav.QuoteETag(etag))
	}
	r, err := c.Do(ctx, http.MethodPut, u, h, data, 1<<16)
	if err != nil {
		return "", err
	}
	if r.Code != http.StatusCreated && r.Code != http.StatusNoContent && r.Code != http.StatusOK {
		return "", fail("PUT", r)
	}
	return carddav.StrongETag(r.Header.Get("ETag")), nil
}
