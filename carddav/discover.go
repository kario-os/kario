package carddav

import (
	"context"
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Book is an address book as discovery found it.
type Book struct {
	URL  string // absolute
	Name string // its displayname, "" without one
	// Sync: it answers sync-collection (RFC 6578); without, the client
	// compares ETags.
	Sync bool
	// Version is the vCard version new cards are written in.
	Version string
	// MaxSize is the largest card it takes (max-resource-size), 0 unsaid.
	MaxSize int64
	// ReadOnly: the user may not write to it (its privileges); a sync
	// only reads it.
	ReadOnly bool
}

var (
	// ErrNoServer: neither a server nor an e-mail address to find one by.
	ErrNoServer = errors.New("no server address")
	// ErrNoBook: the server answered, but has no address book for the user.
	ErrNoBook = errors.New("the server has no address book for this account")
)

// Discover finds the user's address book (the first DiscoverAll finds)
// from what was typed as the server: a URL (an address book's, a
// principal's, any path on the server), a host name, or nothing, with an
// e-mail address as the user name. It tries, in turn, what was given, the
// host's /.well-known/carddav and its
// root (RFC 6764 §5), and for a bare domain the DNS records
// _carddavs._tcp (SRV, with TXT's path; §3, §4) before them when nothing
// was typed, after them otherwise. Plain HTTP is taken only for this
// machine and private addresses; SRV's insecure _carddav is not asked.
func (c *Client) Discover(ctx context.Context, server string) (Book, error) {
	books, err := c.DiscoverAll(ctx, server)
	if err != nil {
		return Book{}, err
	}
	return books[0], nil
}

// DiscoverAll finds the user's address books as Discover does: those of
// the home the user may write to, by path, then its read-only ones (as
// Nextcloud's system book), or the one book typed.
func (c *Client) DiscoverAll(ctx context.Context, server string) ([]Book, error) {
	server = strings.TrimSpace(server)
	var starts []*url.URL
	domain := ""
	if server == "" {
		_, d, ok := strings.Cut(c.User, "@")
		if !ok || d == "" {
			return nil, ErrNoServer
		}
		domain = d
	} else {
		if !strings.Contains(server, "://") {
			server = "https://" + server
		}
		u, err := url.Parse(server)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, ErrNoServer
		}
		u.RawQuery, u.Fragment, u.User = "", "", nil
		if u.Path != "" && u.Path != "/" {
			starts = append(starts, u)
		}
		starts = append(starts, hostURL(u, "/.well-known/carddav"), hostURL(u, "/"))
		if u.Port() == "" && (u.Path == "" || u.Path == "/") && net.ParseIP(u.Hostname()) == nil {
			domain = u.Hostname()
		}
	}
	srv := func() []*url.URL {
		if domain == "" {
			return nil
		}
		return c.lookupSRV(ctx, domain)
	}
	if server == "" {
		starts = srv()
		if len(starts) == 0 {
			d := &url.URL{Scheme: "https", Host: domain}
			starts = []*url.URL{hostURL(d, "/.well-known/carddav"), hostURL(d, "/")}
		}
	}
	// The error told is the first that is not "nothing there".
	var first error
	tried := map[string]bool{}
	for round := 0; round < 2; round++ {
		for _, u := range starts {
			if tried[u.String()] {
				continue
			}
			tried[u.String()] = true
			b, err := c.discoverFrom(ctx, u)
			switch {
			case err == nil:
				return b, nil
			case errors.Is(err, ErrAuth), errors.Is(err, ErrInsecure), ctx.Err() != nil:
				return nil, err
			case first == nil && !errors.Is(err, ErrNotFound):
				first = err
			}
		}
		if round == 0 && server != "" {
			starts = srv()
		}
	}
	if first == nil {
		first = ErrNoBook
	}
	return nil, first
}

func hostURL(u *url.URL, path string) *url.URL {
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: path}
}

// lookupSRV is where _carddavs._tcp.<domain> says the server is, lowest
// priority first; the TXT record's path=, or /.well-known/carddav.
func (c *Client) lookupSRV(ctx context.Context, domain string) []*url.URL {
	var r Resolver = net.DefaultResolver
	if c.Resolver != nil {
		r = c.Resolver
	}
	_, addrs, err := r.LookupSRV(ctx, "carddavs", "tcp", domain)
	if err != nil || len(addrs) == 0 {
		return nil
	}
	path := "/.well-known/carddav"
	if txts, err := r.LookupTXT(ctx, "_carddavs._tcp."+domain); err == nil {
		for _, t := range txts {
			if p, ok := strings.CutPrefix(strings.TrimSpace(t), "path="); ok && strings.HasPrefix(p, "/") {
				path = p
			}
		}
	}
	slices.SortStableFunc(addrs, func(a, b *net.SRV) int { return int(a.Priority) - int(b.Priority) })
	var out []*url.URL
	for _, a := range addrs {
		host := strings.TrimSuffix(a.Target, ".")
		if host == "" {
			continue
		}
		if a.Port != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(int(a.Port)))
		}
		out = append(out, &url.URL{Scheme: "https", Host: host, Path: path})
	}
	return out
}

// discoverFrom follows one start: an address book itself, or the principal
// it names (current-user-principal), the principal's home
// (addressbook-home-set) and the home's books.
func (c *Client) discoverFrom(ctx context.Context, start *url.URL) ([]Book, error) {
	ms, at, err := c.dav(ctx, "PROPFIND", start, "0", []byte(findPrincipal))
	if err != nil {
		return nil, err
	}
	var principal, home string
	for i := range ms.Responses {
		p, code := ms.Responses[i].ok()
		if code < 200 || code > 299 {
			continue
		}
		if p.isBook() {
			b, err := c.bookAt(ctx, at)
			if err != nil {
				return nil, err
			}
			return []Book{b}, nil
		}
		if p.Home != nil && p.Home.Href != "" {
			home = p.Home.Href
		}
		if p.Principal != nil && p.Principal.Href != "" {
			principal = p.Principal.Href
		}
	}
	if home == "" && principal != "" {
		pu, err := at.Parse(principal)
		if err != nil {
			return nil, err
		}
		ms, pat, err := c.dav(ctx, "PROPFIND", pu, "0", []byte(findPrincipal))
		if err != nil {
			return nil, err
		}
		for i := range ms.Responses {
			if p, code := ms.Responses[i].ok(); code >= 200 && code < 300 && p.Home != nil && p.Home.Href != "" {
				home, at = p.Home.Href, pat
			}
		}
	}
	if home == "" {
		return nil, ErrNotFound
	}
	hu, err := at.Parse(home)
	if err != nil {
		return nil, err
	}
	return c.homeBooks(ctx, hu)
}

// homeBooks are the home's address books: those the user may write to, by
// path, then the read-only ones (Nextcloud lists one of the system).
func (c *Client) homeBooks(ctx context.Context, home *url.URL) ([]Book, error) {
	ms, at, err := c.dav(ctx, "PROPFIND", home, "1", []byte(findBooks))
	if err != nil {
		return nil, err
	}
	type found struct {
		href string
		p    prop
	}
	var books []found
	for i := range ms.Responses {
		p, code := ms.Responses[i].ok()
		if code < 200 || code > 299 || !p.isBook() {
			continue
		}
		for _, h := range ms.Responses[i].Hrefs {
			books = append(books, found{resolve(at, h), p})
		}
	}
	slices.SortStableFunc(books, func(a, b found) int {
		if a.p.writable() != b.p.writable() {
			if a.p.writable() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.href, b.href)
	})
	if len(books) == 0 {
		return nil, ErrNoBook
	}
	var out []Book
	seen := map[string]bool{}
	for _, f := range books {
		u, err := at.Parse(f.href)
		if err != nil {
			return nil, err
		}
		if b := describe(u, f.p); !seen[b.URL] {
			seen[b.URL] = true
			out = append(out, b)
		}
	}
	return out, nil
}

// bookAt is the book at u, as its properties describe it.
func (c *Client) bookAt(ctx context.Context, u *url.URL) (Book, error) {
	ms, at, err := c.dav(ctx, "PROPFIND", u, "0", []byte(findBooks))
	if err != nil {
		return Book{}, err
	}
	for i := range ms.Responses {
		if p, code := ms.Responses[i].ok(); code >= 200 && code < 300 && p.isBook() {
			return describe(at, p), nil
		}
	}
	return Book{}, ErrNoBook
}

func describe(u *url.URL, p prop) Book {
	if !strings.HasSuffix(u.Path, "/") {
		v := *u
		v.Path += "/"
		v.RawPath = ""
		u = &v
	}
	b := Book{URL: u.String(), Name: strings.TrimSpace(p.DisplayName), Version: p.version(), ReadOnly: !p.writable()}
	b.Sync = p.reports(nsDAV, "sync-collection") || p.SyncToken != ""
	b.MaxSize, _ = strconv.ParseInt(strings.TrimSpace(p.MaxSize), 10, 64)
	return b
}
