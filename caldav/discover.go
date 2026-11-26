package caldav

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/kario-os/kario/carddav"
)

// Calendar is a calendar as discovery found it.
type Calendar struct {
	URL  string `json:"url"`            // absolute
	Name string `json:"name,omitempty"` // its displayname
	// Sync: it answers sync-collection (RFC 6578); without, the client
	// compares ETags.
	Sync bool `json:"sync,omitempty"`
	// MaxSize is the largest object it takes (max-resource-size), 0 unsaid.
	MaxSize int64 `json:"max_size,omitempty"`
	// Schedule: its server schedules (RFC 6638, Scheduling): it sends the
	// invitations, updates, cancellations and answers of what is written.
	// Inbox is the scheduling inbox, Addresses the user's calendar user
	// addresses (calendar-user-address-set, the mailto: ones), Checked
	// when this was last looked at (Unix seconds; 0 never).
	Schedule  bool     `json:"schedule,omitempty"`
	Inbox     string   `json:"inbox,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	Checked   int64    `json:"checked,omitempty"`
}

// Scheduling is the calendar as its server schedules: the DAV header of
// the calendar's answer says calendar-auto-schedule (RFC 6638 §2) and the
// user's principal (current-user-principal, or the calendar's owner)
// names a schedule-inbox-URL; the principal's calendar-user-address-set
// is the user's addresses. Not scheduling clears the three.
func (c *Client) Scheduling(ctx context.Context, cal Calendar) (Calendar, error) {
	u, err := url.Parse(cal.URL)
	if err != nil {
		return cal, err
	}
	r, err := c.Do(ctx, "PROPFIND", u, davHeader("0"), []byte(findOwner), MaxResponse)
	if err != nil {
		return cal, err
	}
	if r.Code != http.StatusMultiStatus {
		return cal, fail("PROPFIND", r)
	}
	ms, err := parseMultistatus(r.Body)
	if err != nil {
		return cal, fmt.Errorf("PROPFIND: not a WebDAV answer: %w", err)
	}
	auto := false
	for _, v := range r.Header.Values("DAV") {
		for _, f := range strings.Split(v, ",") {
			auto = auto || strings.EqualFold(strings.TrimSpace(f), "calendar-auto-schedule")
		}
	}
	principal := ""
	for i := range ms.Responses {
		p, code := ms.Responses[i].ok()
		switch {
		case code < 200 || code > 299:
		case p.Principal != nil && p.Principal.Href != "":
			principal = p.Principal.Href
		case p.Owner != nil && p.Owner.Href != "" && principal == "":
			principal = p.Owner.Href
		}
	}
	cal.Schedule, cal.Inbox, cal.Addresses = false, "", nil
	if !auto || principal == "" {
		return cal, nil
	}
	pu, err := r.URL.Parse(principal)
	if err != nil {
		return cal, err
	}
	pms, at, err := c.dav(ctx, "PROPFIND", pu, "0", []byte(findScheduling))
	if err != nil {
		return cal, err
	}
	for i := range pms.Responses {
		p, code := pms.Responses[i].ok()
		if code < 200 || code > 299 {
			continue
		}
		if p.Inbox != nil && p.Inbox.Href != "" {
			if iu, err := at.Parse(p.Inbox.Href); err == nil {
				cal.Inbox = iu.String()
			}
		}
		if p.Addresses != nil {
			for _, h := range p.Addresses.Hrefs {
				if a := strings.TrimSpace(h); strings.HasPrefix(strings.ToLower(a), "mailto:") && !slices.Contains(cal.Addresses, a) {
					cal.Addresses = append(cal.Addresses, a)
				}
			}
		}
	}
	cal.Schedule = cal.Inbox != "" && len(cal.Addresses) > 0
	if !cal.Schedule {
		cal.Inbox, cal.Addresses = "", nil
	}
	return cal, nil
}

// Discover finds the user's calendar from what was typed as the server
// (the contacts account's: a URL, a host name, or nothing with an e-mail
// address as the user name). It tries, in turn, what was given, the host's
// /.well-known/caldav and its root (RFC 6764 §5), and for a bare domain
// the DNS records _caldavs._tcp (SRV, with TXT's path; §3, §4), before them
// when nothing was typed, after them otherwise. From a principal
// (current-user-principal) it reads calendar-home-set (RFC 4791 §6.2.1)
// and takes the home's first calendar, by path, that holds events and that
// the user may write to.
func (c *Client) Discover(ctx context.Context, server string) (Calendar, error) {
	server = strings.TrimSpace(server)
	var starts []*url.URL
	domain := ""
	if server == "" {
		_, d, ok := strings.Cut(c.User, "@")
		if !ok || d == "" {
			return Calendar{}, carddav.ErrNoServer
		}
		domain = d
	} else {
		if !strings.Contains(server, "://") {
			server = "https://" + server
		}
		u, err := url.Parse(server)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return Calendar{}, carddav.ErrNoServer
		}
		u.RawQuery, u.Fragment, u.User = "", "", nil
		if u.Path != "" && u.Path != "/" {
			starts = append(starts, u)
		}
		starts = append(starts, hostURL(u, "/.well-known/caldav"), hostURL(u, "/"))
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
			starts = []*url.URL{hostURL(d, "/.well-known/caldav"), hostURL(d, "/")}
		}
	}
	var first error
	tried := map[string]bool{}
	for round := 0; round < 2; round++ {
		for _, u := range starts {
			if tried[u.String()] {
				continue
			}
			tried[u.String()] = true
			cal, err := c.discoverFrom(ctx, u)
			switch {
			case err == nil:
				return cal, nil
			case errors.Is(err, carddav.ErrAuth), errors.Is(err, carddav.ErrInsecure), ctx.Err() != nil:
				return Calendar{}, err
			case first == nil && !errors.Is(err, carddav.ErrNotFound):
				first = err
			}
		}
		if round == 0 && server != "" {
			starts = srv()
		}
	}
	if first == nil {
		first = ErrNoCalendar
	}
	return Calendar{}, first
}

func hostURL(u *url.URL, path string) *url.URL {
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: path}
}

// lookupSRV is where _caldavs._tcp.<domain> says the server is, lowest
// priority first; the TXT record's path=, or /.well-known/caldav.
func (c *Client) lookupSRV(ctx context.Context, domain string) []*url.URL {
	var r carddav.Resolver = net.DefaultResolver
	if c.Resolver != nil {
		r = c.Resolver
	}
	_, addrs, err := r.LookupSRV(ctx, "caldavs", "tcp", domain)
	if err != nil || len(addrs) == 0 {
		return nil
	}
	path := "/.well-known/caldav"
	if txts, err := r.LookupTXT(ctx, "_caldavs._tcp."+domain); err == nil {
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

// discoverFrom follows one start: a calendar itself, or the principal it
// names, the principal's calendar home and the home's calendars.
func (c *Client) discoverFrom(ctx context.Context, start *url.URL) (Calendar, error) {
	ms, at, err := c.dav(ctx, "PROPFIND", start, "0", []byte(findPrincipal))
	if err != nil {
		return Calendar{}, err
	}
	var principal, home string
	for i := range ms.Responses {
		p, code := ms.Responses[i].ok()
		if code < 200 || code > 299 {
			continue
		}
		if p.isCalendar() {
			return c.calendarAt(ctx, at)
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
			return Calendar{}, err
		}
		ms, pat, err := c.dav(ctx, "PROPFIND", pu, "0", []byte(findPrincipal))
		if err != nil {
			return Calendar{}, err
		}
		for i := range ms.Responses {
			if p, code := ms.Responses[i].ok(); code >= 200 && code < 300 && p.Home != nil && p.Home.Href != "" {
				home, at = p.Home.Href, pat
			}
		}
	}
	if home == "" {
		return Calendar{}, carddav.ErrNotFound
	}
	hu, err := at.Parse(home)
	if err != nil {
		return Calendar{}, err
	}
	return c.homeCalendar(ctx, hu)
}

// homeCalendar picks the home's calendar: the first, by path, that holds
// events and the user may write to (iCloud lists calendars of reminders
// only, Nextcloud read-only ones of birthdays).
func (c *Client) homeCalendar(ctx context.Context, home *url.URL) (Calendar, error) {
	ms, at, err := c.dav(ctx, "PROPFIND", home, "1", []byte(findCalendars))
	if err != nil {
		return Calendar{}, err
	}
	type found struct {
		href string
		p    prop
	}
	var cals []found
	for i := range ms.Responses {
		p, code := ms.Responses[i].ok()
		if code < 200 || code > 299 || !p.isCalendar() || !p.events() || !p.writable() {
			continue
		}
		for _, h := range ms.Responses[i].Hrefs {
			cals = append(cals, found{carddav.Resolve(at, h), p})
		}
	}
	slices.SortStableFunc(cals, func(a, b found) int { return strings.Compare(a.href, b.href) })
	if len(cals) == 0 {
		return Calendar{}, ErrNoCalendar
	}
	u, err := at.Parse(cals[0].href)
	if err != nil {
		return Calendar{}, err
	}
	return describe(u, cals[0].p), nil
}

// calendarAt is the calendar at u, as its properties describe it.
func (c *Client) calendarAt(ctx context.Context, u *url.URL) (Calendar, error) {
	ms, at, err := c.dav(ctx, "PROPFIND", u, "0", []byte(findCalendars))
	if err != nil {
		return Calendar{}, err
	}
	for i := range ms.Responses {
		if p, code := ms.Responses[i].ok(); code >= 200 && code < 300 && p.isCalendar() && p.events() {
			return describe(at, p), nil
		}
	}
	return Calendar{}, ErrNoCalendar
}

func describe(u *url.URL, p prop) Calendar {
	if !strings.HasSuffix(u.Path, "/") {
		v := *u
		v.Path += "/"
		v.RawPath = ""
		u = &v
	}
	cal := Calendar{URL: u.String(), Name: strings.TrimSpace(p.DisplayName)}
	cal.Sync = p.reports(nsDAV, "sync-collection") || p.SyncToken != ""
	cal.MaxSize, _ = strconv.ParseInt(strings.TrimSpace(p.MaxSize), 10, 64)
	return cal
}
