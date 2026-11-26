package carddav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// TestInterop syncs two phones with a real server's account, when one is
// given:
//
//	KARIO_CARDDAV_SERVER=contacts.icloud.com KARIO_CARDDAV_USER=... KARIO_CARDDAV_PASSWORD=... go test ./carddav -run Interop -v
//
// Use an account kept for tests: the test adds contacts of its own (named
// "Kario interop <time> …"), changes and deletes them; every other card it
// reads and never writes.
func TestInterop(t *testing.T) {
	server, user, pass := os.Getenv("KARIO_CARDDAV_SERVER"), os.Getenv("KARIO_CARDDAV_USER"), os.Getenv("KARIO_CARDDAV_PASSWORD")
	if user == "" || pass == "" {
		t.Skip("no KARIO_CARDDAV_USER and KARIO_CARDDAV_PASSWORD: no real server to try")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &Client{User: user, Password: pass, UserAgent: "Kario CardDAV interop test"}
	book, err := client.Discover(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("book %s (%q), sync-collection %v, vCard %s, max %d", book.URL, book.Name, book.Sync, book.Version, book.MaxSize)
	type phone struct {
		store *contacts.Store
		sync  *Sync
	}
	newPhone := func(name string) *phone {
		s := contacts.Open(filepath.Join(t.TempDir(), name+".json"), nil)
		return &phone{s, &Sync{Client: client, Book: book, Local: StoreLocal(s, nil), State: &State{}}}
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
	find := func(p *phone, name string) (ui.Contact, bool) {
		for _, c := range p.store.List() {
			if c.Name == name {
				return c, true
			}
		}
		return ui.Contact{}, false
	}
	one, two := newPhone("one"), newPhone("two")
	run(one, "phone one, first sync")
	tag := fmt.Sprintf("Kario interop %d", time.Now().Unix())
	for i, n := range []string{"A", "B", "C"} {
		one.store.Save(ui.Contact{Name: tag + " " + n, Numbers: []ui.ContactNumber{{Number: fmt.Sprintf("+1 555 01%02d", i), Label: ui.NumberMobile}},
			Birthday: "--04-12", Org: "Kario interop"})
	}
	uid := strings.ReplaceAll(tag, " ", "-") + "-D"
	raw := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + tag + " D\r\nN:D;" + tag + ";;;\r\nTEL;TYPE=CELL:+1 555 0199\r\n" +
		"EMAIL;TYPE=INTERNET:d@example.com\r\nX-KARIO-INTEROP:keep me\r\nEND:VCARD\r\n"
	u := mustParse(t, book.URL)
	if _, err := client.Put(ctx, u, uid+".vcf", []byte(raw), ""); err != nil {
		t.Fatal(err)
	}
	if res := run(one, "phone one, three new"); res.Uploaded != 3 {
		t.Errorf("uploaded %d", res.Uploaded)
	}
	run(two, "phone two, first sync")
	for _, n := range []string{"A", "B", "C", "D"} {
		if _, ok := find(two, tag+" "+n); !ok {
			t.Errorf("phone two has no %s", n)
		}
	}
	if got, _ := find(two, tag+" A"); got.Birthday != "--04-12" || got.Org != "Kario interop" {
		t.Errorf("a birthday without its year and an organization: %+v", got)
	}
	a, _ := find(one, tag+" A")
	a.Notes = "changed on phone one"
	one.store.Save(a)
	b, _ := find(one, tag+" B")
	one.store.Delete(b.ID)
	d, _ := find(one, tag+" D")
	d.Numbers = append(d.Numbers, ui.ContactNumber{Number: "+1 555 0198", Label: ui.NumberWork})
	one.store.Save(d)
	c, _ := find(two, tag+" C")
	c.Name = tag + " C renamed"
	two.store.Save(c)
	run(one, "phone one, changes")
	run(two, "phone two, changes")
	run(one, "phone one, again")
	for _, p := range []*phone{one, two} {
		if got, _ := find(p, tag+" A"); got.Notes != "changed on phone one" {
			t.Errorf("A: %+v", got)
		}
		if _, ok := find(p, tag+" B"); ok {
			t.Error("B is still there")
		}
		if _, ok := find(p, tag+" C renamed"); !ok {
			t.Error("C was not renamed")
		}
	}
	// A 412: phone one writes A between phone two's read and its write.
	a, _ = find(one, tag+" A")
	a.Name = tag + " A one"
	one.store.Save(a)
	a2, _ := find(two, tag+" A")
	a2.Notes = "from phone two"
	two.store.Save(a2)
	var between sync.Once
	two.sync.Client = &Client{User: user, Password: pass, HTTP: &http.Client{Transport: hook(func(r *http.Request) {
		if r.Method == http.MethodPut {
			between.Do(func() { run(one, "phone one, in between") })
		}
	})}}
	if res := run(two, "phone two, 412"); res.Conflicts == 0 {
		t.Error("no conflict seen")
	}
	run(one, "phone one, after the 412")
	for _, p := range []*phone{one, two} {
		if got, ok := find(p, tag+" A one"); !ok || got.Notes != "from phone two" {
			t.Errorf("A after the 412: %+v", got)
		}
	}
	got, err := client.Get(ctx, u, uid+".vcf")
	switch {
	case err != nil:
		t.Error(err)
	case !strings.Contains(string(got.Data), "X-KARIO-INTEROP:keep me") || !strings.Contains(string(got.Data), "d@example.com") ||
		!strings.Contains(string(got.Data), "+1 555 0198"):
		t.Errorf("D on the server:\n%s", got.Data)
	}
	for _, n := range []string{"A one", "C renamed", "D"} {
		if x, ok := find(one, tag+" "+n); ok {
			one.store.Delete(x.ID)
		}
	}
	run(one, "phone one, clean up")
	run(two, "phone two, clean up")
	for _, x := range two.store.List() {
		if strings.HasPrefix(x.Name, tag) {
			t.Errorf("left on the server: %s", x.Name)
		}
	}
}

// hook runs before each request goes out.
type hook func(*http.Request)

func (h hook) RoundTrip(r *http.Request) (*http.Response, error) {
	h(r)
	return http.DefaultTransport.RoundTrip(r)
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
