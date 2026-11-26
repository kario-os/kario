package caldav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/carddav"
)

// TestInterop syncs two phones' calendars with a real server's account,
// when one is given:
//
//	KARIO_CALDAV_SERVER=caldav.icloud.com KARIO_CALDAV_USER=... KARIO_CALDAV_PASSWORD=... go test ./caldav -run Interop -v
//
// Use an account kept for tests: the test adds events of its own (UIDs
// "kario-interop-<time>-…"), changes and deletes them; every other object
// it reads and never writes.
func TestInterop(t *testing.T) {
	server, user, pass := os.Getenv("KARIO_CALDAV_SERVER"), os.Getenv("KARIO_CALDAV_USER"), os.Getenv("KARIO_CALDAV_PASSWORD")
	if user == "" || pass == "" {
		t.Skip("no KARIO_CALDAV_USER and KARIO_CALDAV_PASSWORD: no real server to try")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	newClient := func(h *http.Client) *Client {
		return &Client{Client: &carddav.Client{User: user, Password: pass, UserAgent: "Kario CalDAV interop test", HTTP: h}}
	}
	client := newClient(nil)
	cal, err := client.Discover(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("calendar %s (%q), sync-collection %v, max %d", cal.URL, cal.Name, cal.Sync, cal.MaxSize)
	tag := fmt.Sprintf("kario-interop-%d", time.Now().Unix())
	type phone struct {
		local *memLocal
		sync  *Sync
	}
	newPhone := func() *phone {
		l := &memLocal{}
		return &phone{l, &Sync{Client: client, Calendar: cal, Local: l, State: &State{}}}
	}
	run := func(p *phone, what string) Result {
		t.Helper()
		res, err := p.sync.Run(ctx)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%s: %+v", what, res)
		return res
	}
	mine := func(p *phone) map[string]Change {
		out := map[string]Change{}
		for uid, c := range p.local.take() {
			if strings.HasPrefix(uid, tag) || strings.Contains(c.ICS, tag) {
				out[uid] = c
			}
		}
		return out
	}
	one, two := newPhone(), newPhone()
	run(one, "phone one, first sync")
	one.local.take()
	for _, n := range []string{"a", "b"} {
		one.local.q.Push(Change{UID: tag + "-" + n, ICS: ev(tag+"-"+n, tag+" "+n)})
	}
	if res := run(one, "phone one, two new"); res.Uploaded != 2 {
		t.Errorf("uploaded %d", res.Uploaded)
	}
	run(two, "phone two, first sync")
	if got := mine(two); len(got) != 2 {
		t.Fatalf("phone two has %d of the events", len(got))
	}
	one.local.q.Push(Change{UID: tag + "-a", ICS: ev(tag+"-a", tag+" a renamed")})
	one.local.q.Push(Change{UID: tag + "-b", Gone: true})
	run(one, "phone one, changes")
	run(two, "phone two, changes")
	got := mine(two)
	if !strings.Contains(got[tag+"-a"].ICS, "a renamed") || !got[tag+"-b"].Gone {
		t.Errorf("phone two after the changes: %+v", got)
	}
	// A 412: phone one writes a between phone two's read and its write.
	one.local.q.Push(Change{UID: tag + "-a", ICS: ev(tag+"-a", tag+" a by one")})
	two.local.q.Push(Change{UID: tag + "-a", ICS: ev(tag+"-a", tag+" a by two")})
	var between sync.Once
	two.sync.Client = newClient(&http.Client{Transport: hook(func(r *http.Request) {
		if r.Method == http.MethodPut {
			between.Do(func() { run(one, "phone one, in between") })
		}
	})})
	if res := run(two, "phone two, 412"); res.Conflicts == 0 {
		t.Error("no conflict seen")
	}
	run(one, "phone one, after the 412")
	got = mine(one)
	copies := 0
	for _, c := range got {
		if strings.Contains(c.ICS, "a by two") {
			copies++
		}
	}
	if copies == 0 {
		t.Errorf("the copy of phone two's edit: %+v", got)
	}
	// Clean up: every event of the test.
	u, _ := url.Parse(cal.URL)
	for _, it := range one.sync.State.Items {
		if o, err := client.Get(ctx, u, it.Href); err == nil && strings.Contains(string(o.Data), tag) {
			one.local.q.Push(Change{UID: it.UID, Gone: true})
		}
	}
	run(one, "phone one, clean up")
	if left, ok := findOn(ctx, client, u, tag); ok {
		t.Errorf("left on the server: %s", left)
	}
}

// TestInteropScheduling has a real scheduling server (RFC 6638) carry an
// invitation between two of its accounts, when they are given:
//
//	KARIO_CALDAV_SERVER=... KARIO_CALDAV_USER=... KARIO_CALDAV_PASSWORD=... KARIO_CALDAV_USER2=... KARIO_CALDAV_PASSWORD2=... go test ./caldav -run InteropScheduling -v
//
// The first organizes, the second attends: the invitation reaches the
// second's calendar, the answer comes back into the first's, a rename
// made on the first's stale copy keeps the answer, the deletion cancels
// the second's copy. Use accounts kept for tests: only events of the
// test's own (UIDs "kario-sched-<time>") are written.
func TestInteropScheduling(t *testing.T) {
	server := os.Getenv("KARIO_CALDAV_SERVER")
	users := [][2]string{{os.Getenv("KARIO_CALDAV_USER"), os.Getenv("KARIO_CALDAV_PASSWORD")}, {os.Getenv("KARIO_CALDAV_USER2"), os.Getenv("KARIO_CALDAV_PASSWORD2")}}
	for _, u := range users {
		if u[0] == "" || u[1] == "" {
			t.Skip("no KARIO_CALDAV_USER, KARIO_CALDAV_PASSWORD, KARIO_CALDAV_USER2 and KARIO_CALDAV_PASSWORD2: no real server to try")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	type phone struct {
		local *memLocal
		sync  *Sync
	}
	var phones []*phone
	for _, u := range users {
		client := &Client{Client: &carddav.Client{User: u[0], Password: u[1], UserAgent: "Kario CalDAV interop test"}}
		cal, err := client.Discover(ctx, server)
		if err == nil {
			cal, err = client.Scheduling(ctx, cal)
		}
		if err != nil {
			t.Fatal(err)
		}
		if !cal.Schedule {
			t.Skip("the server does not schedule (no calendar-auto-schedule, inbox or address)")
		}
		t.Logf("calendar %s, inbox %s, addresses %v", cal.URL, cal.Inbox, cal.Addresses)
		l := &memLocal{}
		phones = append(phones, &phone{l, &Sync{Client: client, Calendar: cal, Local: l, State: &State{}}})
	}
	org, att := phones[0], phones[1]
	run := func(p *phone, what string) Result {
		t.Helper()
		res, err := p.sync.Run(ctx)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%s: %+v", what, res)
		return res
	}
	uid := fmt.Sprintf("kario-sched-%d", time.Now().Unix())
	me, them := org.sync.Calendar.Addresses[0], att.sync.Calendar.Addresses[0]
	invite := func(summary, partstat string) string {
		return ev(uid, summary, "ORGANIZER:"+me+"\r\nATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:"+me+"\r\nATTENDEE;PARTSTAT="+partstat+";RSVP=TRUE:"+them+"\r\n")
	}
	run(org, "organizer, first sync")
	org.local.take()
	run(att, "attendee, first sync")
	att.local.take()
	org.local.q.Push(Change{UID: uid, ICS: invite(uid, "NEEDS-ACTION")})
	run(org, "organizer invites")
	if got := org.local.take()[uid].ICS; !strings.Contains(strings.ReplaceAll(got, "\r\n ", ""), "SCHEDULE-STATUS") {
		t.Errorf("no delivery status comes back:\n%s", got)
	}
	run(att, "attendee, the invitation")
	inv := att.local.take()[uid].ICS
	if !strings.Contains(inv, uid) {
		t.Fatalf("the invitation did not reach the attendee")
	}
	answered := strings.Replace(strings.ReplaceAll(inv, "\r\n ", ""), "PARTSTAT=NEEDS-ACTION", "PARTSTAT=ACCEPTED", 1)
	att.local.q.Push(Change{UID: uid, ICS: answered})
	run(att, "attendee accepts")
	org.local.q.Push(Change{UID: uid, ICS: invite(uid+" renamed", "NEEDS-ACTION")})
	if res := run(org, "organizer renames on a stale copy"); res.Conflicts != 0 {
		t.Errorf("a conflict: %+v", res)
	}
	got := strings.ReplaceAll(org.local.take()[uid].ICS, "\r\n ", "")
	if !strings.Contains(got, "renamed") || !strings.Contains(got, "PARTSTAT=ACCEPTED") {
		t.Errorf("the organizer's copy keeps the answer:\n%s", got)
	}
	run(att, "attendee, the rename")
	if got := strings.ReplaceAll(att.local.take()[uid].ICS, "\r\n ", ""); !strings.Contains(got, "renamed") {
		t.Errorf("the attendee's copy is not renamed:\n%s", got)
	}
	org.local.q.Push(Change{UID: uid, Gone: true})
	run(org, "organizer deletes")
	run(att, "attendee, the cancellation")
	if got := att.local.take()[uid]; !got.Gone && !strings.Contains(got.ICS, "CANCELLED") {
		t.Errorf("the attendee's copy is not cancelled: %+v", got)
	}
	att.local.q.Push(Change{UID: uid, Gone: true})
	run(att, "attendee, clean up")
	for _, p := range phones {
		u, _ := url.Parse(p.sync.Calendar.URL)
		if left, ok := findOn(ctx, p.sync.Client, u, uid); ok {
			t.Errorf("left on the server: %s", left)
		}
	}
}

// findOn is the first object of the calendar whose content holds text.
func findOn(ctx context.Context, c *Client, cal *url.URL, text string) (string, bool) {
	list, err := c.List(ctx, cal)
	if err != nil {
		return "", false
	}
	for _, it := range list {
		if o, err := c.Get(ctx, cal, it.Href); err == nil && strings.Contains(string(o.Data), text) {
			return it.Href, true
		}
	}
	return "", false
}

// hook runs before each request goes out.
type hook func(*http.Request)

func (h hook) RoundTrip(r *http.Request) (*http.Response, error) {
	h(r)
	return http.DefaultTransport.RoundTrip(r)
}
