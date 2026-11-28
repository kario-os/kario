package carddav

import (
	"context"
	"net/http"
	"net/url"
)

// The WebDAV under the CardDAV requests, for the client of another DAV
// protocol on the same account (package caldav): the same rules for
// credentials and redirects, the same errors, the same ETags and paths.

// Response is an answer read whole: its status, headers, body, and where
// it came from after redirects.
type Response struct {
	Code   int
	Header http.Header
	Body   []byte
	URL    *url.URL
}

// Do sends a request and reads its answer, at most limit bytes (longer is
// ErrTooLarge). PROPFIND, REPORT, GET and OPTIONS follow redirects, never
// from HTTPS to HTTP; credentials go only to HTTPS or to plain HTTP on this
// machine or a private address (ErrInsecure otherwise).
func (c *Client) Do(ctx context.Context, method string, u *url.URL, hdr http.Header, body []byte, limit int64) (*Response, error) {
	r, err := c.do(ctx, method, u, hdr, body, limit)
	if err != nil {
		return nil, err
	}
	return &Response{Code: r.code, Header: r.header, Body: r.body, URL: r.url}, nil
}

// Fail is the error for an answer that is not what was asked (ErrAuth,
// ErrPrecondition, ErrNotFound, ErrToken, ErrTooLarge, ErrFull, ErrRefused
// or a StatusError).
func Fail(method string, r *Response) error {
	return fail(method, &result{code: r.Code, header: r.Header, body: r.Body, url: r.URL})
}

// HrefKey is a path as a key that is the same however it is escaped.
func HrefKey(base *url.URL, href string) string { return hrefKey(base, href) }

// Resolve is a href of an answer as an absolute path, as the server wrote it.
func Resolve(base *url.URL, href string) string { return resolve(base, href) }

// NormETag is an ETag without its quotes (a weak one as W/ and its tag).
func NormETag(v string) string { return normETag(v) }

// StrongETag is a written resource's ETag: "" when weak (the server changed
// what it stored).
func StrongETag(v string) string { return strongETag(v) }

// QuoteETag is an ETag for If-Match.
func QuoteETag(etag string) string { return quote(etag) }

// RandomUID is a random UUID (version 4), for a new resource's name and UID.
func RandomUID() string { return randomUID() }
