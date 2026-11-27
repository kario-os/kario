package carddav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/carddav/davtest"
	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

const (
	testUser = "alice@example.com"
	testPass = "abcd-efgh-ijkl-mnop"
)

type env struct {
	t      *testing.T
	srv    *davtest.Server
	ts     *httptest.Server
	store  *contacts.Store
	client *Client
	sync   *Sync
	saves  int
}

func newEnv(t *testing.T, set func(*davtest.Server)) *env {
	t.Helper()
	srv := davtest.New(testUser, testPass)
	if set != nil {
		set(srv)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	e := &env{t: t, srv: srv, ts: ts, store: contacts.Open(filepath.Join(t.TempDir(), "contacts.json"), nil),
		client: &Client{User: testUser, Password: testPass}}
	return e
}

func (e *env) start() {
	e.t.Helper()
	b, err := e.client.Discover(context.Background(), e.ts.URL)
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	e.sync = &Sync{Client: e.client, Book: b, Local: StoreLocal(e.store, nil), State: &State{},
		Save:   func(*State) error { e.saves++; return nil },
		NewUID: func() string { n++; return fmt.Sprintf("kario-%04d", n) }, Now: func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }}
}

func (e *env) run() Result {
	e.t.Helper()
	res, err := e.sync.Run(context.Background())
	if err != nil {
		e.t.Fatalf("sync: %v", err)
	}
	return res
}

func (e *env) add(c ui.Contact) ui.Contact {
	e.t.Helper()
	out, err := e.store.Save(c)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) byName(name string) (ui.Contact, bool) {
	for _, c := range e.store.List() {
		if c.Name == name {
			return c, true
		}
	}
	return ui.Contact{}, false
}

func (e *env) names() string {
	var out []string
	for _, c := range e.store.List() {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func card3(uid, fn string, lines ...string) []byte {
	return []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + fn + "\r\n" + strings.Join(append(lines, ""), "\r\n") + "END:VCARD\r\n")
}

func num(n, label string) ui.ContactNumber { return ui.ContactNumber{Number: n, Label: label} }

func TestDiscover(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	for _, from := range []string{e.ts.URL, e.ts.URL + "/", e.ts.URL + davtest.Book, e.ts.URL + davtest.Principal} {
		b, err := e.client.Discover(ctx, from)
		if err != nil || b.URL != e.ts.URL+davtest.Book || !b.Sync || b.Version != "3.0" || b.Name != "Contacts" {
			t.Errorf("from %s: %+v %v", from, b, err)
		}
	}
	if e.srv.Count("well-known") == 0 {
		t.Error("did not ask /.well-known/carddav")
	}
	bad := &Client{User: testUser, Password: "wrong"}
	if _, err := bad.Discover(ctx, e.ts.URL); !errors.Is(err, ErrAuth) {
		t.Errorf("a wrong password: %v", err)
	}
	if strings.Contains(fmt.Sprint(bad.Discover(ctx, e.ts.URL)), "wrong") {
		t.Error("the error tells the password")
	}
	none := httptest.NewServer(http.NotFoundHandler())
	defer none.Close()
	if _, err := e.client.Discover(ctx, none.URL); !errors.Is(err, ErrNoBook) {
		t.Errorf("no CardDAV there: %v", err)
	}
	if _, err := e.client.Discover(ctx, "http://contacts.example.com"); !errors.Is(err, ErrInsecure) {
		t.Errorf("plain HTTP to another host: %v", err)
	}
	if _, err := (&Client{User: "alice"}).Discover(ctx, ""); !errors.Is(err, ErrNoServer) {
		t.Errorf("nothing to go by: %v", err)
	}
}

type fakeDNS struct {
	srv  map[string][]*net.SRV
	txt  map[string][]string
	asks []string
}

func (d *fakeDNS) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	d.asks = append(d.asks, "_"+service+"._"+proto+"."+name)
	if r, ok := d.srv[name]; ok {
		return "", r, nil
	}
	return "", nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	return d.txt[name], nil
}

// RFC 6764: an e-mail address and no server finds the server by DNS SRV,
// and the TXT record's path.
func TestDiscoverSRV(t *testing.T) {
	srv := davtest.New(testUser, testPass)
	ts := httptest.NewTLSServer(srv)
	defer ts.Close()
	host, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	var p uint16
	fmt.Sscan(port, &p)
	dns := &fakeDNS{srv: map[string][]*net.SRV{"example.com": {{Target: host + ".", Port: p, Priority: 10}}},
		txt: map[string][]string{"_carddavs._tcp.example.com": {"path=" + davtest.Principal}}}
	c := &Client{User: testUser, Password: testPass, Resolver: dns, HTTP: ts.Client()}
	b, err := c.Discover(context.Background(), "")
	if err != nil || !strings.HasSuffix(b.URL, davtest.Book) || !strings.HasPrefix(b.URL, "https://") {
		t.Fatalf("%+v %v", b, err)
	}
	if len(dns.asks) != 1 || dns.asks[0] != "_carddavs._tcp.example.com" {
		t.Errorf("asked %v", dns.asks)
	}
	if srv.Count("well-known") != 0 {
		t.Error("the TXT record's path was not used")
	}
}

// The first sync takes the server's cards in, sends the phone's out, and
// does not add twice the person both have.
func TestFirstSyncBothWays(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("bob.vcf", card3("bob", "Bob Lee", "TEL;TYPE=CELL:+1 555 0100", "EMAIL:bob@example.com"))
		s.Put("v4.vcf", []byte("BEGIN:VCARD\nVERSION:4.0\nUID:urn:uuid:v4\nFN:\u738b\u4e94\nTEL;VALUE=uri;TYPE=cell:tel:+86-138-0013-8000\nEND:VCARD\n"))
	})
	e.add(ui.Contact{Name: "bob lee", Numbers: []ui.ContactNumber{num("+15550100", ui.NumberMobile), num("010 1234", ui.NumberWork)}})
	e.add(ui.Contact{Name: "\u5f20\u4e09", Numbers: []ui.ContactNumber{num("13900001111", ui.NumberMobile)}, Notes: "from the phone"})
	e.start()
	res := e.run()
	if got := e.names(); got != "Bob Lee,\u5f20\u4e09,\u738b\u4e94" {
		t.Fatalf("the phone has %s", got)
	}
	if res.Added != 1 || res.Uploaded != 2 || res.Changed != 1 {
		t.Errorf("%+v", res)
	}
	if bob, _ := e.byName("Bob Lee"); nums(bob) != "mobile=+1 555 0100,work=010 1234" {
		t.Errorf("the phone's Bob: %s", nums(bob))
	}
	cards := e.srv.Cards()
	if len(cards) != 3 {
		t.Fatalf("the server has %d cards", len(cards))
	}
	bob := string(cards["bob.vcf"])
	if !strings.Contains(bob, "EMAIL:bob@example.com\r\n") || !strings.Contains(bob, "TEL;TYPE=WORK:010 1234\r\n") || !strings.Contains(bob, "UID:bob\r\n") {
		t.Errorf("the server's Bob:\n%s", bob)
	}
	name, ok := e.srv.Find("FN:\u5f20\u4e09")
	if !ok || name != "kario-0001.vcf" || !strings.Contains(string(cards[name]), "UID:kario-0001\r\n") || !strings.Contains(string(cards[name]), "NOTE:from the phone") {
		t.Errorf("the phone's contact on the server: %s\n%s", name, cards[name])
	}
	if !bytes.Equal(cards["v4.vcf"], []byte("BEGIN:VCARD\nVERSION:4.0\nUID:urn:uuid:v4\nFN:\u738b\u4e94\nTEL;VALUE=uri;TYPE=cell:tel:+86-138-0013-8000\nEND:VCARD\n")) {
		t.Errorf("a card nobody changed was written:\n%s", cards["v4.vcf"])
	}
	for _, it := range e.sync.State.Items {
		if it.ETag == "" || it.ID == "" || it.New {
			t.Errorf("item %+v", it)
		}
	}
	if puts := e.srv.Count("PUT"); puts != 2 {
		t.Errorf("%d PUTs", puts)
	}
	again := e.run()
	if again != (Result{}) || e.srv.Count("PUT") != 2 || e.srv.Count("REPORT multiget") != 1 {
		t.Errorf("a second sync with nothing new: %+v, %d PUTs, %d multigets", again, e.srv.Count("PUT"), e.srv.Count("REPORT multiget"))
	}
}

// After the first sync only what changed travels: the sync token says
// what, and the phone's own writes do not come back to it.
func TestIncrementalSync(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		for i := 0; i < 5; i++ {
			s.Put(fmt.Sprintf("c%d.vcf", i), card3(fmt.Sprint("u", i), fmt.Sprint("Person ", i), fmt.Sprintf("TEL:+1 555 010%d", i)))
		}
	})
	e.start()
	e.run()
	gets, lists := e.srv.Count("REPORT multiget"), e.srv.Count("PROPFIND")
	e.srv.Put("c2.vcf", card3("u2", "Person Two", "TEL:+1 555 0102", "NOTE:renamed elsewhere"))
	e.srv.Delete("c4.vcf")
	e.srv.Put("new.vcf", card3("n", "Newcomer", "TEL:+1 555 0199"))
	res := e.run()
	if res.Added != 1 || res.Changed != 1 || res.Deleted != 1 {
		t.Errorf("%+v", res)
	}
	if got := e.names(); got != "Newcomer,Person 0,Person 1,Person 3,Person Two" {
		t.Errorf("%s", got)
	}
	if e.srv.Count("REPORT multiget") != gets+1 || e.srv.Count("PROPFIND") != lists {
		t.Errorf("%d multigets, %d PROPFINDs: more than the changes", e.srv.Count("REPORT multiget")-gets, e.srv.Count("PROPFIND")-lists)
	}
	c, _ := e.byName("Person 1")
	c.Notes = "edited on the phone"
	e.store.Save(c)
	gets = e.srv.Count("REPORT multiget")
	if res := e.run(); res.Uploaded != 1 {
		t.Errorf("%+v", res)
	}
	if res := e.run(); res != (Result{}) || e.srv.Count("REPORT multiget") != gets {
		t.Errorf("the phone's own write came back: %+v", res)
	}
	if !strings.Contains(string(e.srv.Card("c1.vcf")), "NOTE:edited on the phone") {
		t.Errorf("%s", e.srv.Card("c1.vcf"))
	}
}

// A sync token the server forgot: everything again, and a card deleted
// meanwhile (its tombstone forgotten too) is found missing.
func TestTokenInvalidResync(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("a.vcf", card3("a", "Ann", "TEL:1001"))
		s.Put("b.vcf", card3("b", "Ben", "TEL:1002"))
	})
	e.add(ui.Contact{Name: "Cat", Number: "1003"})
	e.start()
	e.run()
	e.srv.Delete("b.vcf")
	e.srv.ForgetTokens()
	syncs := e.srv.Count("REPORT sync")
	res := e.run()
	if got := e.names(); got != "Ann,Cat" || res.Deleted != 1 || len(e.srv.Cards()) != 2 {
		t.Errorf("%s %+v %d cards", got, res, len(e.srv.Cards()))
	}
	if e.srv.Count("REPORT sync") != syncs+2 {
		t.Errorf("%d sync-collections: not the refused one and one from the start", e.srv.Count("REPORT sync")-syncs)
	}
	if res := e.run(); res != (Result{}) {
		t.Errorf("after the resync: %+v", res)
	}
}

func TestMergeRule(t *testing.T) {
	base := ui.Contact{Name: "Ann", Notes: "n", Numbers: []ui.ContactNumber{num("1001", ui.NumberMobile), num("1002", ui.NumberHome)}}
	local := ui.Contact{Name: "Ann Lee", Notes: "n", Numbers: []ui.ContactNumber{num("1001", ui.NumberWork), num("1003", ui.NumberMobile)}}
	remote := ui.Contact{Name: "Ann", Notes: "from the server", Numbers: []ui.ContactNumber{num("1001", ui.NumberMobile), num("1002", ui.NumberHome), num("1004", ui.NumberOther)}}
	got := Merge(base, local, remote)
	if got.Name != "Ann Lee" || got.Notes != "from the server" || nums(got) != "work=1001,other=1004,mobile=1003" {
		t.Errorf("%+v %s", got, nums(got))
	}
	both := ui.Contact{Name: "Annie", Notes: "server notes", Numbers: base.Numbers}
	local.Notes = "phone notes"
	if got := Merge(base, local, both); got.Name != "Annie" || got.Notes != "server notes" {
		t.Errorf("both changed name and notes: the server's wins: %+v", got)
	}
}

func nums(c ui.Contact) string {
	var out []string
	for _, n := range c.Numbers {
		out = append(out, n.Label+"="+n.Number)
	}
	return strings.Join(out, ",")
}

// Another device changes the card between the phone's read and its write:
// the server answers 412, the phone reads it again and merges.
func TestConcurrentEdit412(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("ann.vcf", card3("ann", "Ann", "TEL;TYPE=CELL:1001", "X-KARIO-TEST:keep me"))
	})
	e.start()
	e.run()
	var once atomic.Bool
	e.srv.BeforeWrite = func(name string) {
		if name == "ann.vcf" && once.CompareAndSwap(false, true) {
			e.srv.Put("ann.vcf", card3("ann", "Ann", "TEL;TYPE=CELL:1001", "X-KARIO-TEST:keep me", "NOTE:from the tablet"))
		}
	}
	ann, _ := e.byName("Ann")
	ann.Name = "Ann Lee"
	ann.Numbers = append(ann.Numbers, num("1002", ui.NumberWork))
	e.store.Save(ann)
	res := e.run()
	if res.Conflicts != 1 || res.Uploaded != 1 || res.Changed != 1 {
		t.Errorf("%+v", res)
	}
	got, _ := e.byName("Ann Lee")
	if got.Notes != "from the tablet" || nums(got) != "mobile=1001,work=1002" {
		t.Errorf("the phone: %+v", got)
	}
	card := string(e.srv.Card("ann.vcf"))
	for _, want := range []string{"FN:Ann Lee", "NOTE:from the tablet", "TEL;TYPE=WORK:1002", "X-KARIO-TEST:keep me"} {
		if !strings.Contains(card, want) {
			t.Errorf("the server's card has no %q:\n%s", want, card)
		}
	}
	if e.srv.Count("PUT") != 2 {
		t.Errorf("%d PUTs: not the refused one and the merged one", e.srv.Count("PUT"))
	}
}

// Both sides changed the contact since the last sync, seen in the pull.
func TestBothChangedSincePull(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Put("ann.vcf", card3("ann", "Ann", "TEL;TYPE=CELL:1001")) })
	e.start()
	e.run()
	e.srv.Put("ann.vcf", card3("ann", "Annie", "TEL;TYPE=CELL:1001", "TEL;TYPE=HOME:1009"))
	ann, _ := e.byName("Ann")
	ann.Name, ann.Notes = "Ann Lee", "met in Paris"
	e.store.Save(ann)
	res := e.run()
	got, ok := e.byName("Annie")
	if !ok || got.Notes != "met in Paris" || nums(got) != "mobile=1001,home=1009" || res.Conflicts != 1 {
		t.Errorf("%+v %+v", got, res)
	}
	if card := string(e.srv.Card("ann.vcf")); !strings.Contains(card, "FN:Annie") || !strings.Contains(card, "NOTE:met in Paris") {
		t.Errorf("%s", card)
	}
}

func TestDeletes(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		for _, n := range []string{"a", "b", "c", "d"} {
			s.Put(n+".vcf", card3(n, "Person "+n, "TEL:100"+n))
		}
	})
	e.start()
	e.run()
	pa, _ := e.byName("Person a")
	e.store.Delete(pa.ID)
	e.srv.Delete("b.vcf")
	res := e.run()
	if res.Removed != 1 || res.Deleted != 1 || e.srv.Card("a.vcf") != nil || e.names() != "Person c,Person d" {
		t.Errorf("%+v %s", res, e.names())
	}
	// An edit beats a delete, either way round.
	pc, _ := e.byName("Person c")
	e.store.Delete(pc.ID)
	e.srv.Put("c.vcf", card3("c", "Person c", "TEL:100c", "NOTE:changed elsewhere"))
	pd, _ := e.byName("Person d")
	pd.Notes = "changed on the phone"
	e.store.Save(pd)
	e.srv.Delete("d.vcf")
	res = e.run()
	if got, ok := e.byName("Person c"); !ok || got.Notes != "changed elsewhere" {
		t.Errorf("deleted on the phone, changed on the server: %+v", got)
	}
	if card := e.srv.Card("d.vcf"); !strings.Contains(string(card), "NOTE:changed on the phone") {
		t.Errorf("changed on the phone, deleted on the server: %s", card)
	}
	if res.Conflicts != 2 {
		t.Errorf("%+v", res)
	}
	if res := e.run(); res != (Result{}) {
		t.Errorf("%+v", res)
	}
}

// A delete on the phone of a card changed on the server meanwhile (the
// DELETE's If-Match fails): the contact comes back.
func TestDelete412(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Put("a.vcf", card3("a", "Ann", "TEL:1001")) })
	e.start()
	e.run()
	e.srv.BeforeWrite = func(string) {
		e.srv.BeforeWrite = nil
		e.srv.Put("a.vcf", card3("a", "Ann", "TEL:1001", "NOTE:just now"))
	}
	ann, _ := e.byName("Ann")
	e.store.Delete(ann.ID)
	e.run()
	if got, ok := e.byName("Ann"); !ok || got.Notes != "just now" || e.srv.Card("a.vcf") == nil {
		t.Errorf("%+v", got)
	}
}

// Groups and cards with nothing the phone shows are left alone, on both
// sides.
func TestCardsLeftAlone(t *testing.T) {
	group := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:g\r\nFN:Family\r\nX-ADDRESSBOOKSERVER-KIND:group\r\nX-ADDRESSBOOKSERVER-MEMBER:urn:uuid:a\r\nEND:VCARD\r\n")
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("g.vcf", group)
		s.Put("e.vcf", []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:e\r\nFN:\r\nEMAIL:x@example.com\r\nEND:VCARD\r\n"))
	})
	e.start()
	e.run()
	if n := len(e.store.List()); n != 0 || !bytes.Equal(e.srv.Card("g.vcf"), group) || len(e.srv.Cards()) != 2 {
		t.Errorf("%d contacts; %d cards", n, len(e.srv.Cards()))
	}
	if gets := e.srv.Count("REPORT multiget"); e.run() != (Result{}) || e.srv.Count("REPORT multiget") != gets {
		t.Error("read them again")
	}
}

func TestAuthFailure(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Put("a.vcf", card3("a", "Ann", "TEL:1001")) })
	e.start()
	e.run()
	e.srv.Lock()
	e.srv.Pass = "revoked"
	e.srv.Unlock()
	before := *e.sync.State
	_, err := e.sync.Run(context.Background())
	if !errors.Is(err, ErrAuth) || Temporary(err) {
		t.Errorf("%v", err)
	}
	if strings.Contains(err.Error(), testPass) || strings.Contains(err.Error(), "alice") {
		t.Errorf("the error tells the credentials: %v", err)
	}
	if e.sync.State.Token != before.Token || len(e.sync.State.Items) != 1 {
		t.Error("the state moved")
	}
}

func TestServerDown(t *testing.T) {
	e := newEnv(t, nil)
	e.start()
	e.srv.Lock()
	e.srv.Down, e.srv.RetryAfter = true, 120
	e.srv.Unlock()
	_, err := e.sync.Run(context.Background())
	if !Temporary(err) || RetryAfter(err) != 2*time.Minute {
		t.Errorf("%v (retry after %v)", err, RetryAfter(err))
	}
	e.ts.Close()
	_, err = e.sync.Run(context.Background())
	var ne *NetError
	if !errors.As(err, &ne) || !Temporary(err) || strings.Contains(err.Error(), "/dav/") {
		t.Errorf("%v", err)
	}
}

// A large address book comes in pages (sync-collection's limit) and
// batches (multiget), and the phone's many new contacts go out with the
// state saved per batch, not per card.
func TestLargeBook(t *testing.T) {
	const n = 1200
	e := newEnv(t, func(s *davtest.Server) {
		for i := 0; i < n; i++ {
			s.Put(fmt.Sprintf("c%04d.vcf", i), card3(fmt.Sprint("u", i), fmt.Sprintf("Person %04d", i), fmt.Sprintf("TEL:+1 555 %04d", i)))
		}
	})
	for i := 0; i < 120; i++ {
		e.add(ui.Contact{Name: fmt.Sprintf("Phone %03d", i), Number: fmt.Sprintf("+86 139 0000 %04d", i)})
	}
	e.start()
	e.sync.Limit, e.sync.Batch = 100, 50
	var pages []string
	e.sync.Notify = func(phase string, done, total int) { pages = append(pages, fmt.Sprint(phase, done, "/", total)) }
	res := e.run()
	if len(e.store.List()) != n+120 || len(e.srv.Cards()) != n+120 || res.Added != n || res.Uploaded != 120 {
		t.Fatalf("%d contacts, %d cards, %+v", len(e.store.List()), len(e.srv.Cards()), res)
	}
	if s, m := e.srv.Count("REPORT sync"), e.srv.Count("REPORT multiget"); s != n/100 || m != n/50 {
		t.Errorf("%d sync-collection pages, %d multigets", s, m)
	}
	if e.saves > 40 {
		t.Errorf("the state was saved %d times", e.saves)
	}
	if len(pages) == 0 || pages[len(pages)-1] != "push120/120" {
		t.Errorf("progress %v", pages)
	}
}

// A server without sync-collection: ETags compared, the CTag saving the
// listing when nothing changed.
func TestETagFallback(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.NoSync = true
		s.Put("a.vcf", card3("a", "Ann", "TEL:1001"))
		s.Put("b.vcf", card3("b", "Ben", "TEL:1002"))
	})
	e.start()
	if e.sync.Book.Sync {
		t.Fatal("the book says it has no sync-collection")
	}
	e.run()
	e.srv.Put("a.vcf", card3("a", "Ann", "TEL:1001", "NOTE:new"))
	e.srv.Delete("b.vcf")
	res := e.run()
	if a, _ := e.byName("Ann"); a.Notes != "new" || e.names() != "Ann" || res.Changed != 1 || res.Deleted != 1 {
		t.Errorf("%+v %s", res, e.names())
	}
	lists := e.srv.Count("PROPFIND")
	e.run()
	if e.srv.Count("PROPFIND") != lists+1 {
		t.Errorf("%d PROPFINDs with the CTag unchanged", e.srv.Count("PROPFIND")-lists)
	}
	if e.srv.Count("REPORT sync") != 0 {
		t.Error("asked sync-collection")
	}
}

// A server that rewrites the cards it stores answers without an ETag: the
// phone reads it, so its own write still does not come back.
func TestServerRewritesCards(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Rewrite = true })
	e.add(ui.Contact{Name: "Ann", Number: "1001"})
	e.start()
	e.run()
	if len(e.sync.State.Items) != 1 || e.sync.State.Items[0].ETag == "" {
		t.Fatalf("%+v", e.sync.State.Items)
	}
	gets := e.srv.Count("REPORT multiget")
	if res := e.run(); res != (Result{}) || e.srv.Count("REPORT multiget") != gets {
		t.Errorf("%+v", res)
	}
}

// A book that takes vCard 4.0 only gets 4.0; one too small for a card
// refuses it and the sync goes on.
func TestVersionAndSize(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Version, s.MaxSize = "4.0", 300 })
	e.add(ui.Contact{Name: "Ann", Number: "1001"})
	e.add(ui.Contact{Name: "Ben", Number: "1002", Notes: strings.Repeat("long notes ", 50)})
	e.start()
	if e.sync.Book.Version != "4.0" || e.sync.Book.MaxSize != 300 {
		t.Fatalf("%+v", e.sync.Book)
	}
	res := e.run()
	if res.Uploaded != 1 || res.Failed != 1 || !errors.Is(res.Problem, ErrTooLarge) {
		t.Errorf("%+v", res)
	}
	name, _ := e.srv.Find("FN:Ann")
	if !strings.HasPrefix(string(e.srv.Card(name)), "BEGIN:VCARD\r\nVERSION:4.0\r\n") {
		t.Errorf("%s", e.srv.Card(name))
	}
}

func TestQuota(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Quota = 1 })
	e.add(ui.Contact{Name: "Ann", Number: "1001"})
	e.add(ui.Contact{Name: "Ben", Number: "1002"})
	e.start()
	if _, err := e.sync.Run(context.Background()); !errors.Is(err, ErrFull) || Temporary(err) {
		t.Errorf("%v", err)
	}
	if len(e.srv.Cards()) != 1 {
		t.Errorf("%d cards", len(e.srv.Cards()))
	}
}

// The phone changes a contact while the sync runs: the sync does not write
// over it, and the next one sends it.
func TestLocalChangeDuringSync(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Put("a.vcf", card3("a", "Ann", "TEL:1001")) })
	e.start()
	e.run()
	e.srv.Put("a.vcf", card3("a", "Ann", "TEL:1001", "NOTE:server"))
	inner := e.sync.Local
	e.sync.Local = &racing{Local: inner, before: func() {
		ann, _ := e.byName("Ann")
		ann.Name = "Ann (phone)"
		e.store.Save(ann)
	}}
	e.run()
	if _, ok := e.byName("Ann (phone)"); !ok {
		t.Fatalf("the user's edit was written over: %s", e.names())
	}
	e.sync.Local = inner
	e.run()
	got, _ := e.byName("Ann (phone)")
	card := string(e.srv.Card("a.vcf"))
	if got.Notes != "server" || !strings.Contains(card, "FN:Ann (phone)") || !strings.Contains(card, "NOTE:server") {
		t.Errorf("%+v\n%s", got, card)
	}
	if res := e.run(); res != (Result{}) {
		t.Errorf("%+v", res)
	}
}

// racing is the user editing just before the sync's first write.
type racing struct {
	Local
	before func()
}

func (r *racing) Apply(ops []Op) ([]Done, error) {
	if r.before != nil {
		r.before()
		r.before = nil
	}
	return r.Local.Apply(ops)
}

// failing is a store that cannot be written (a full disk).
type failing struct{ Local }

func (failing) Apply([]Op) ([]Done, error) { return nil, errors.New("no space left on device") }

// A store that cannot be written ends the run, and leaves nothing marked
// as synced that is not on the phone: the next run, once the store
// works, brings all of it.
func TestStoreWriteFails(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("a.vcf", card3("a", "Ann", "TEL:1001"))
		s.Put("b.vcf", card3("b", "Ben", "TEL:1002"))
	})
	e.add(ui.Contact{Name: "ben", Numbers: []ui.ContactNumber{num("1002", ui.NumberMobile), num("1003", ui.NumberHome)}})
	e.start()
	inner := e.sync.Local
	e.sync.Local = failing{inner}
	if _, err := e.sync.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "no space") {
		t.Fatalf("%v", err)
	}
	e.sync.Local = inner
	e.run()
	if e.names() != "Ann,Ben" {
		t.Errorf("%s", e.names())
	}
	if ben, _ := e.byName("Ben"); nums(ben) != "other=1002,home=1003" {
		t.Errorf("%+v", ben)
	}
}
