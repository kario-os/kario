// Package carddav is the phone's CardDAV client: it keeps the contacts store
// (package contacts) in two-way sync with address books on servers, the
// Kario account's or any other that speaks CardDAV (RFC 6352; iCloud,
// Nextcloud, Fastmail), one Sync a book. client.go speaks the protocol,
// discover.go finds the address books (RFC 6764), sync.go is the sync
// itself, groups.go its groups; the Shell runs it (shell/accounts.go).
// davtest is a server to test it against.
//
// It changes what the server has only with If-Match or If-None-Match, so a
// card another device changed in between is never written over: the server
// answers 412 and the sync merges (sync.go). No error it returns carries a
// URL, a user name or a password: a server's paths may hold the user's name.
package carddav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/contacts"
)

// Limits on what a server sends. A card the phone takes is at most MaxCard
// (a photo fits, as the contacts package reads it: a vCard file's limit);
// a multistatus is at most MaxResponse.
const (
	MaxCard     = 4 << 20
	MaxResponse = 64 << 20
)

var (
	ErrAuth = errors.New("the server did not accept the user name and password")
	// ErrPrecondition: the card is not as If-Match or If-None-Match said
	// (412): another device changed it, or it is there already.
	ErrPrecondition = errors.New("the card changed on the server")
	// ErrToken: the server no longer knows the sync token (RFC 6578
	// valid-sync-token); the client starts over.
	ErrToken    = errors.New("the server no longer knows the sync token")
	ErrNotFound = errors.New("not found on the server")
	ErrTooLarge = errors.New("too large for the server or for the phone")
	ErrFull     = errors.New("the address book on the server is full")
	ErrRefused  = errors.New("the server refused the card")
	// ErrInsecure: credentials would go over plain HTTP to another host.
	ErrInsecure = errors.New("the server is not on HTTPS")
	// ErrForeign: a photo's URL is not on the book's own server.
	ErrForeign = errors.New("the photo is on another server")
)

// StatusError is a status the client does not handle otherwise.
// RetryAfter is the server's Retry-After, 0 without one.
type StatusError struct {
	Method     string
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: the server answered %d %s", e.Method, e.Code, http.StatusText(e.Code))
}

// Temporary: the server is busy, down for a while or its gateway is.
func (e *StatusError) Temporary() bool {
	switch e.Code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// NetError is a request that did not reach the server or got no answer:
// no network, DNS, a refused connection, a timeout, TLS.
type NetError struct{ Err error }

func (e *NetError) Error() string { return "the server could not be reached: " + e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// Client is a signed-in connection to one server.
type Client struct {
	HTTP           *http.Client // nil: one with timeouts
	User, Password string
	UserAgent      string
	// Resolver finds a domain's CardDAV server (DNS SRV, RFC 6764); nil:
	// the system's.
	Resolver Resolver
}

// Resolver is what Discover asks DNS (net.Resolver has both).
type Resolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

var defaultHTTP = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{
	Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
	TLSHandshakeTimeout: 20 * time.Second, ResponseHeaderTimeout: time.Minute, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 2,
}}

// result is an answer read whole, within its limit.
type result struct {
	code   int
	header http.Header
	body   []byte
	url    *url.URL // where the answer came from, after redirects
}

const maxRedirects = 5

// do sends a request, following redirects for the methods that only read
// (with the same method and body, as RFC 6764 has clients do for
// .well-known), never from HTTPS to HTTP. Credentials go along only to
// HTTPS, or to plain HTTP on the same loopback or private address.
func (c *Client) do(ctx context.Context, method string, u *url.URL, hdr http.Header, body []byte, limit int64) (*result, error) {
	return c.send(ctx, method, u, hdr, body, limit, nil)
}

// send is do, and with origin set it follows no redirect off that origin.
func (c *Client) send(ctx context.Context, method string, u *url.URL, hdr http.Header, body []byte, limit int64, origin *url.URL) (*result, error) {
	hc := defaultHTTP
	if c.HTTP != nil {
		hc = c.HTTP
	}
	noFollow := *hc
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	follow := method == "PROPFIND" || method == "REPORT" || method == http.MethodGet || method == "OPTIONS"
	for hop := 0; ; hop++ {
		if !credentialsMayGo(u) {
			return nil, ErrInsecure
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		if body != nil && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/xml; charset=utf-8")
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		req.SetBasicAuth(c.User, c.Password)
		resp, err := noFollow.Do(req)
		if err != nil {
			return nil, netError(ctx, err)
		}
		if follow && hop < maxRedirects && isRedirect(resp.StatusCode) {
			resp.Body.Close()
			next, err := u.Parse(resp.Header.Get("Location"))
			if err != nil || resp.Header.Get("Location") == "" {
				return nil, &StatusError{Method: method, Code: resp.StatusCode}
			}
			if u.Scheme == "https" && next.Scheme != "https" {
				return nil, ErrInsecure
			}
			if origin != nil && !sameOrigin(next, origin) {
				return nil, ErrForeign
			}
			u = next
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()
		if err != nil {
			return nil, netError(ctx, err)
		}
		if int64(len(raw)) > limit {
			return nil, ErrTooLarge
		}
		return &result{code: resp.StatusCode, header: resp.Header, body: raw, url: u}, nil
	}
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// credentialsMayGo: HTTPS, or plain HTTP to this machine or an address of
// a private network (a server at home), named by its IP address.
func credentialsMayGo(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
	}
	return false
}

// netError drops the URL net/http puts in its errors.
func netError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &NetError{Err: err}
}

// fail is the error for an answer that is not what was asked.
func fail(method string, r *result) error {
	body := string(r.body)
	switch {
	case r.code == http.StatusUnauthorized:
		return ErrAuth
	case r.code == http.StatusPreconditionFailed:
		return ErrPrecondition
	case r.code == http.StatusNotFound || r.code == http.StatusGone:
		return ErrNotFound
	case strings.Contains(body, "valid-sync-token") && r.code >= 400 && r.code < 500:
		return ErrToken
	case r.code == http.StatusRequestEntityTooLarge || strings.Contains(body, "max-resource-size"):
		return ErrTooLarge
	case r.code == http.StatusInsufficientStorage || strings.Contains(body, "quota-not-exceeded"):
		return ErrFull
	case strings.Contains(body, "valid-address-data") || strings.Contains(body, "supported-address-data") ||
		strings.Contains(body, "no-uid-conflict") || r.code == http.StatusUnsupportedMediaType:
		return ErrRefused
	}
	return &StatusError{Method: method, Code: r.code, RetryAfter: retryAfter(r.header.Get("Retry-After"))}
}

func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, time.Until(t))
	}
	return 0
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
	r, err := c.do(ctx, method, u, davHeader(depth), body, MaxResponse)
	if err != nil {
		return nil, nil, err
	}
	if r.code != http.StatusMultiStatus {
		return nil, nil, fail(method, r)
	}
	ms, err := parseMultistatus(r.body)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: not a WebDAV answer: %w", method, err)
	}
	return ms, r.url, nil
}

// Card is a card on the server: where (its path, as the server writes it),
// its ETag and, when read, its content.
type Card struct {
	Href string
	ETag string
	Data []byte
}

// Changes is what a sync-collection answered: the cards changed or added
// (no content), the ones deleted, the token to ask with next, and whether
// there is more (the server stopped at the limit: ask again with Token).
type Changes struct {
	Changed []Card
	Deleted []string
	Token   string
	More    bool
}

// SyncCollection asks a book what changed since token ("" for every
// card), at most limit changes (0: as many as the server gives).
func (c *Client) SyncCollection(ctx context.Context, book *url.URL, token string, limit int) (Changes, error) {
	ms, at, err := c.dav(ctx, "REPORT", book, "0", syncBody(token, limit))
	if err != nil {
		return Changes{}, err
	}
	out := Changes{Token: ms.SyncToken}
	self := hrefKey(book, book.EscapedPath())
	for i := range ms.Responses {
		r := &ms.Responses[i]
		p, code := r.ok()
		for _, h := range r.Hrefs {
			href := resolve(at, h)
			switch {
			case hrefKey(book, href) == self:
				if code == http.StatusInsufficientStorage {
					out.More = true
				}
			case code == http.StatusNotFound || code == http.StatusGone:
				out.Deleted = append(out.Deleted, href)
			case code >= 200 && code < 300 && !p.isCollection():
				out.Changed = append(out.Changed, Card{Href: href, ETag: normETag(p.ETag)})
			}
		}
	}
	if out.Token == "" {
		return Changes{}, errors.New("REPORT: the server's sync-collection answer has no sync token")
	}
	return out, nil
}

// List is every card of a book with its ETag (PROPFIND Depth 1), for a
// server without sync-collection.
func (c *Client) List(ctx context.Context, book *url.URL) ([]Card, error) {
	ms, at, err := c.dav(ctx, "PROPFIND", book, "1", []byte(listETags))
	if err != nil {
		return nil, err
	}
	self := hrefKey(book, book.EscapedPath())
	var out []Card
	for i := range ms.Responses {
		r := &ms.Responses[i]
		p, code := r.ok()
		for _, h := range r.Hrefs {
			href := resolve(at, h)
			if code >= 200 && code < 300 && !p.isCollection() && hrefKey(book, href) != self {
				out = append(out, Card{Href: href, ETag: normETag(p.ETag)})
			}
		}
	}
	return out, nil
}

// State is a book's sync token and CTag (either may be ""): a CTag that
// did not change means no card did.
func (c *Client) State(ctx context.Context, book *url.URL) (token, ctag string, err error) {
	ms, _, err := c.dav(ctx, "PROPFIND", book, "0", []byte(bookState))
	if err != nil {
		return "", "", err
	}
	for i := range ms.Responses {
		if p, code := ms.Responses[i].ok(); code >= 200 && code < 300 {
			return p.SyncToken, p.CTag, nil
		}
	}
	return "", "", nil
}

// Multiget reads cards by their paths (addressbook-multiget). Missing are
// the paths the server has no card at.
func (c *Client) Multiget(ctx context.Context, book *url.URL, hrefs []string) (cards []Card, missing []string, err error) {
	ms, at, err := c.dav(ctx, "REPORT", book, "1", multigetBody(hrefs))
	if err != nil {
		return nil, nil, err
	}
	asked := map[string]string{}
	for _, h := range hrefs {
		asked[hrefKey(book, h)] = h
	}
	for i := range ms.Responses {
		r := &ms.Responses[i]
		p, code := r.ok()
		for _, h := range r.Hrefs {
			k := hrefKey(book, resolve(at, h))
			want, ok := asked[k]
			if !ok {
				continue
			}
			delete(asked, k)
			if code >= 200 && code < 300 && p.AddressData != "" && len(p.AddressData) <= MaxCard {
				cards = append(cards, Card{Href: want, ETag: normETag(p.ETag), Data: []byte(p.AddressData)})
			} else {
				missing = append(missing, want)
			}
		}
	}
	for _, h := range asked {
		missing = append(missing, h)
	}
	return cards, missing, nil
}

func cardURL(book *url.URL, href string) (*url.URL, error) { return book.Parse(href) }

// Get reads one card.
func (c *Client) Get(ctx context.Context, book *url.URL, href string) (Card, error) {
	u, err := cardURL(book, href)
	if err != nil {
		return Card{}, err
	}
	r, err := c.do(ctx, http.MethodGet, u, http.Header{"Accept": {"text/vcard"}}, nil, MaxCard)
	if err != nil {
		return Card{}, err
	}
	if r.code != http.StatusOK {
		return Card{}, fail("GET", r)
	}
	return Card{Href: href, ETag: normETag(r.header.Get("ETag")), Data: r.body}, nil
}

// Photo reads a contact's picture at ref, a card's PHOTO URL (resolved
// against the book's): only from the book's own server (its scheme, host
// and port, redirects too), which has the credentials anyway and knows
// when the card is read; at most contacts.MaxPhotoBytes.
func (c *Client) Photo(ctx context.Context, book *url.URL, ref string) ([]byte, error) {
	u, err := book.Parse(strings.TrimSpace(ref))
	if err != nil || !sameOrigin(u, book) {
		return nil, ErrForeign
	}
	r, err := c.send(ctx, http.MethodGet, u, http.Header{"Accept": {"image/jpeg, image/png, image/gif"}}, nil, contacts.MaxPhotoBytes, book)
	if err != nil {
		return nil, err
	}
	if r.code != http.StatusOK {
		return nil, fail("GET", r)
	}
	return r.body, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// Put writes a card: a new one (etag "", with If-None-Match: *) or over
// the one with that ETag (If-Match). The new ETag is "" when the server
// did not give a strong one (it changed the card as it stored it: read it
// again).
func (c *Client) Put(ctx context.Context, book *url.URL, href string, data []byte, etag string) (string, error) {
	u, err := cardURL(book, href)
	if err != nil {
		return "", err
	}
	h := http.Header{"Content-Type": {"text/vcard; charset=utf-8"}}
	if etag == "" {
		h.Set("If-None-Match", "*")
	} else {
		h.Set("If-Match", quote(etag))
	}
	r, err := c.do(ctx, http.MethodPut, u, h, data, 1<<16)
	if err != nil {
		return "", err
	}
	if r.code != http.StatusCreated && r.code != http.StatusNoContent && r.code != http.StatusOK {
		return "", fail("PUT", r)
	}
	return strongETag(r.header.Get("ETag")), nil
}

// Delete removes the card with that ETag; one already gone is no error.
func (c *Client) Delete(ctx context.Context, book *url.URL, href, etag string) error {
	u, err := cardURL(book, href)
	if err != nil {
		return err
	}
	h := http.Header{}
	if etag != "" {
		h.Set("If-Match", quote(etag))
	}
	r, err := c.do(ctx, http.MethodDelete, u, h, nil, 1<<16)
	if err != nil {
		return err
	}
	switch r.code {
	case http.StatusNoContent, http.StatusOK, http.StatusAccepted, http.StatusNotFound, http.StatusGone:
		return nil
	}
	return fail("DELETE", r)
}

// ETags are kept without their quotes, a weak one as W/ and its tag.
func normETag(v string) string {
	v = strings.TrimSpace(v)
	if w, ok := strings.CutPrefix(v, "W/"); ok {
		return "W/" + strings.Trim(w, `"`)
	}
	return strings.Trim(v, `"`)
}

// strongETag is a written card's ETag: none when weak (the server changed
// the card as it stored it).
func strongETag(v string) string {
	if v = normETag(v); strings.HasPrefix(v, "W/") {
		return ""
	}
	return v
}

func quote(etag string) string {
	if w, ok := strings.CutPrefix(etag, "W/"); ok {
		return `W/"` + w + `"`
	}
	return `"` + etag + `"`
}

// resolve is a href of an answer as an absolute path, the way the server
// wrote it; a full URL on another host stays whole.
func resolve(base *url.URL, href string) string {
	u, err := base.Parse(strings.TrimSpace(href))
	if err != nil {
		return href
	}
	if u.Host != base.Host || u.Scheme != base.Scheme {
		return u.String()
	}
	return u.EscapedPath()
}

// hrefKey compares paths as the same resource however they are escaped.
func hrefKey(base *url.URL, href string) string {
	u, err := base.Parse(href)
	if err != nil {
		return href
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return u.Host + p
}
