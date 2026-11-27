package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// tokenServer is an update server as kario-otasrv serves restricted
// bundles: latest.json offers phone.raucb (and delta.raucb from 0.7.0),
// restricted or not; the challenge and token endpoints give a token to a
// phone attested under one of roots for ota.BundleApp over
// ota.BundleNonce; a restricted bundle goes only with a token it issued.
type tokenServer struct {
	*httptest.Server
	bundle     []byte
	restricted bool
	mu         sync.Mutex
	roots      []*keystore.Roots
	ttl        int
	tokenCode  int  // the token endpoint answers this status instead
	denyBundle bool // the bundle answers 403 to any token
	challenges map[string]bool
	tokens     map[string]bool
	issued     int
	posts      int            // requests to the challenge and token endpoints
	asked      []string       // the bundles tokens were asked for
	auths      []string       // each bundle request's Authorization
	gets       map[string]int // bundle requests by path
}

func newTokenServer(t *testing.T, restricted bool, roots ...*keystore.Roots) *tokenServer {
	s := &tokenServer{bundle: make([]byte, 4*16<<10), restricted: restricted, roots: roots, ttl: 3600,
		challenges: map[string]bool{}, tokens: map[string]bool{}, gets: map[string]int{}}
	rand.New(rand.NewSource(7)).Read(s.bundle)
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *tokenServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/latest.json":
		fmt.Fprintf(w, `{"version":"9.0.0","url":"phone.raucb","restricted":%v,"deltas":[{"from":"0.7.0","url":"delta.raucb"}]}`, s.restricted)
	case ota.BundleChallengePath:
		s.posts++
		c := fmt.Sprintf("c%d", len(s.challenges))
		s.challenges[c] = true
		_ = json.NewEncoder(w).Encode(ota.BundleChallenge{Challenge: c, ExpiresIn: 300})
	case ota.BundleTokenPath:
		s.posts++
		var req ota.BundleTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Attestation == nil {
			http.Error(w, "a request names a bundle and carries the phone's attestation", http.StatusBadRequest)
			return
		}
		s.asked = append(s.asked, req.Bundle)
		if s.tokenCode != 0 {
			http.Error(w, "no", s.tokenCode)
			return
		}
		if !s.challenges[req.Challenge] {
			http.Error(w, "unknown or expired challenge", http.StatusForbidden)
			return
		}
		delete(s.challenges, req.Challenge)
		ours := false
		for _, root := range s.roots {
			ours = ours || keystore.VerifyAttestation(root, req.Attestation, ota.BundleApp, ota.BundleNonce(req.Challenge, req.Bundle)) == nil
		}
		if !ours {
			http.Error(w, "not a phone this update server trusts", http.StatusForbidden)
			return
		}
		s.issued++
		tok := fmt.Sprintf("t%d", s.issued)
		s.tokens[tok] = true
		_ = json.NewEncoder(w).Encode(ota.BundleToken{Token: tok, ExpiresIn: s.ttl})
	case "/phone.raucb", "/delta.raucb":
		s.gets[r.URL.Path]++
		auth := r.Header.Get("Authorization")
		s.auths = append(s.auths, auth)
		tok, _ := strings.CutPrefix(auth, "Bearer ")
		if s.restricted && (!s.tokens[tok] || s.denyBundle) {
			http.Error(w, "restricted: this bundle needs a download token", http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "phone.raucb", time.Time{}, bytes.NewReader(s.bundle))
	default:
		http.NotFound(w, r)
	}
}

// restart voids every token, as a restart of kario-otasrv does.
func (s *tokenServer) restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = map[string]bool{}
}

func (s *tokenServer) with(f func(s *tokenServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// testKeystore is a phone's keystore and its root.
func testKeystore(t *testing.T) (*keystore.Store, *keystore.Roots) {
	t.Helper()
	dir := t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := keystore.LoadRootPub(keystore.RootPubPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return ks, roots
}

// bundlePhone gives c a keystore (keystored on its run directory) and
// returns its root.
func bundlePhone(t *testing.T, c *Core) *keystore.Roots {
	t.Helper()
	ks, roots := testKeystore(t)
	if c.cfg.RunDir == "" {
		c.cfg.RunDir = t.TempDir()
	}
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ App, Nonce string }
		_ = json.Unmarshal(params, &p)
		return ks.Attest(p.App, p.Nonce, "")
	})
	c.broker = &broker{c: c, clients: map[string]*wire.Client{}}
	return roots
}

// rangeRauc stands in for rauc install: the bundle by range requests from
// the URL it is given, piece by piece as RAUC streams it, then the other
// slot set for the next start; mid runs after the first piece. It keeps
// the URLs it was given and the tokens the server had issued as each began.
type rangeRauc struct {
	srv    *tokenServer
	mid    func()
	urls   []string
	issued []int
}

func (r *rangeRauc) install(c *Core) func(string, func(string)) (string, error) {
	slots := c.update.slots
	return func(url string, line func(string)) (string, error) {
		r.srv.with(func(s *tokenServer) { r.issued = append(r.issued, s.issued) })
		r.urls = append(r.urls, url)
		const piece = 16 << 10
		for off := 0; off < len(r.srv.bundle); off += piece {
			b, err := rangeGet(url, off, piece)
			if err != nil || !bytes.Equal(b, r.srv.bundle[off:off+piece]) {
				return fmt.Sprintf("Failed to download bundle %s: %v", url, err), errors.New("exit status 1")
			}
			if off == 0 && r.mid != nil {
				r.mid()
			}
		}
		return "", os.WriteFile(filepath.Join(slots, "boot"), []byte("slot=b\ngood=a\ntries=3\nbooted=a\n"), 0o644)
	}
}

// restrictedCore is a shell with A/B slots, a keystore, the update server
// srv (restricted or not, for this phone's root unless stranger) and the
// stand-in RAUC.
func restrictedCore(t *testing.T, restricted, stranger bool) (*Core, *tokenServer, *rangeRauc) {
	c := updateCore(t, "true")
	roots := bundlePhone(t, c)
	if stranger {
		_, roots = testKeystore(t)
	}
	srv := newTokenServer(t, restricted, roots)
	c.cfg.UpdateURL = srv.URL
	r := &rangeRauc{srv: srv}
	c.update.install = r.install(c)
	return c, srv, r
}

// checkOffer reads latest.json; the offer has its restriction.
func checkOffer(t *testing.T, c *Core, restricted bool) {
	t.Helper()
	checked := false
	c.updateCheck(func(_ any, err error) {
		if err != nil {
			t.Errorf("check: %v", err)
		}
		checked = true
	})
	pumpUntil(t, c, func() bool { return checked })
	if c.update.offer == nil || c.update.offer.Restricted != restricted {
		t.Fatalf("offered %+v", c.update.offer)
	}
}

func userInstall(t *testing.T, c *Core) error {
	t.Helper()
	var err error
	replied := false
	c.updateInstall(func(_ any, e error) { err, replied = e, true })
	pumpUntil(t, c, func() bool { return replied || !c.update.retryAt.IsZero() })
	return err
}

// A restricted bundle installs through the shell's proxy, by hand and
// automatically: the proxy bought a token by the phone's attestation
// before RAUC started and sent it with every request to the server.
func TestRestrictedInstall(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "by hand", true: "automatic"}[automatic], func(t *testing.T) {
			c, srv, r := restrictedCore(t, true, false)
			if automatic {
				c.settings.AutoSystemUpdate = true
				c.update.unmetered = func(then func(bool)) { then(true) }
				c.updateTick()
				pumpUntil(t, c, func() bool { return c.update.state == "installed" })
			} else {
				checkOffer(t, c, true)
				if err := userInstall(t, c); err != nil || c.update.state != "installed" {
					t.Fatalf("%s: %v", c.update.state, err)
				}
			}
			if len(r.urls) != 1 || strings.HasPrefix(r.urls[0], srv.URL) || !strings.HasPrefix(r.urls[0], "http://127.0.0.1:") || r.issued[0] != 1 {
				t.Fatalf("RAUC installed %v, with %v tokens issued as it began", r.urls, r.issued)
			}
			srv.with(func(s *tokenServer) {
				if len(s.auths) != 4 || s.asked[0] != "phone.raucb" {
					t.Fatalf("the server saw %q, tokens asked for %v", s.auths, s.asked)
				}
				for _, a := range s.auths {
					if a != "Bearer t1" {
						t.Fatalf("the server saw %q", s.auths)
					}
				}
			})
			if c.update.proxy != nil || c.updateStatus().Automatic != automatic {
				t.Fatalf("after the install: proxy %v, %+v", c.update.proxy, c.updateStatus())
			}
		})
	}
}

// The proxy keeps its token until 5 minutes before it expires, then buys
// another; a token the server no longer takes (it restarted) is dropped
// and the request sent again, once, with a new one. Once the server will
// not give this phone a token, the proxy asks no more.
func TestBundleTokenRefresh(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	srv := newTokenServer(t, true, bundlePhone(t, c))
	bundle := srv.URL + "/phone.raucb"
	p, err := newInstallProxy(bundle, func(ctx context.Context, cl *http.Client) (string, time.Duration, error) {
		return c.bundleToken(ctx, cl, bundle)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	var skew atomic.Int64
	start := time.Now()
	p.tokens.now = func() time.Time { return start.Add(time.Duration(skew.Load())) }
	if err := p.ready(); err != nil {
		t.Fatal(err)
	}
	get := func(at time.Duration) error {
		skew.Store(int64(at))
		_, err := rangeGet(p.url, 0, 4)
		return err
	}
	seen := func() (auths []string, issued, posts int) {
		srv.with(func(s *tokenServer) { auths, issued, posts = append([]string{}, s.auths...), s.issued, s.posts })
		return
	}
	if err := get(0); err != nil {
		t.Fatal(err)
	}
	if err := get(54 * time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := get(56 * time.Minute); err != nil {
		t.Fatal(err)
	}
	if auths, issued, _ := seen(); issued != 2 || strings.Join(auths, ",") != "Bearer t1,Bearer t1,Bearer t2" {
		t.Fatalf("%d tokens issued, the server saw %q", issued, auths)
	}
	srv.restart()
	if err := get(57 * time.Minute); err != nil {
		t.Fatalf("after the server restarted: %v", err)
	}
	if auths, issued, _ := seen(); issued != 3 || strings.Join(auths[3:], ",") != "Bearer t2,Bearer t3" {
		t.Fatalf("after the restart: %d tokens issued, the server saw %q", issued, auths[3:])
	}
	srv.with(func(s *tokenServer) { s.denyBundle = true })
	if err := get(58 * time.Minute); err == nil {
		t.Fatal("got a bundle the server refuses")
	}
	if _, issued, _ := seen(); issued != 4 || p.refused() {
		t.Fatalf("a bundle refused with a new token: %d tokens issued, refused %v", issued, p.refused())
	}
	srv.with(func(s *tokenServer) { s.denyBundle, s.roots = false, nil })
	srv.restart()
	if err := get(59 * time.Minute); err == nil || !p.refused() {
		t.Fatalf("a server that trusts this phone no more: %v, refused %v", err, p.refused())
	}
	_, _, posts := seen()
	if err := get(60 * time.Minute); err == nil {
		t.Fatal("got the bundle after the refusal")
	}
	if _, _, again := seen(); again != posts {
		t.Fatalf("asked for a token again after the refusal: %d requests, then %d", posts, again)
	}
}

// A server that will not give this phone a restricted bundle's token
// (403: a phone under another root) fails the install at once with "not
// for this device": RAUC does not run, nothing is tried again, and the
// full bundle is not tried after the incremental one. So too when it
// stops giving one part way (a restart, the phone no longer trusted).
func TestRestrictedNotForDevice(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		c, srv, r := restrictedCore(t, true, true)
		checkOffer(t, c, true)
		c.update.offer.Delta = &c.update.offer.Deltas[0]
		if automatic {
			c.startInstall(nil)
			pumpUntil(t, c, func() bool { return c.update.state != "installing" })
		} else if err := userInstall(t, c); err == nil || err.Error() != tr.T("This update is not for this device") {
			t.Fatalf("by hand: %v", err)
		}
		if st := c.updateStatus(); st.State != "failed" || st.Error != tr.T("This update is not for this device") || !c.update.retryAt.IsZero() || len(r.urls) != 0 {
			t.Fatalf("automatic %v: %+v, RAUC ran for %v", automatic, st, r.urls)
		}
		srv.with(func(s *tokenServer) {
			if strings.Join(s.asked, ",") != "delta.raucb" || len(s.auths) != 0 {
				t.Fatalf("tokens asked for %v, the bundles asked for with %q", s.asked, s.auths)
			}
		})
		if automatic && c.update.auto.last != "failed: "+tr.T("This update is not for this device") {
			t.Fatalf("the automatic update says %q", c.update.auto.last)
		}
	}

	c, srv, r := restrictedCore(t, true, false)
	r.mid = func() {
		srv.with(func(s *tokenServer) { s.roots = nil })
		srv.restart()
	}
	checkOffer(t, c, true)
	if err := userInstall(t, c); err == nil || err.Error() != tr.T("This update is not for this device") || !c.update.retryAt.IsZero() {
		t.Fatalf("refused part way: %v, %s", err, c.update.state)
	}
	if len(r.urls) != 1 {
		t.Fatalf("RAUC ran for %v", r.urls)
	}
}

// Any other trouble getting a restricted bundle's token (the server busy,
// unreachable) goes as a download that failed: tried again later, and
// after the last try "Downloading the update failed". RAUC never takes a
// restricted bundle straight from the server.
func TestRestrictedTokenTrouble(t *testing.T) {
	c, srv, r := restrictedCore(t, true, false)
	srv.with(func(s *tokenServer) { s.tokenCode = http.StatusServiceUnavailable })
	checkOffer(t, c, true)
	if err := userInstall(t, c); err != nil || c.update.state != "installing" || c.update.retries != 1 || c.update.retryAt.IsZero() || len(r.urls) != 0 {
		t.Fatalf("a busy server: %v, %s, %d retries, RAUC ran for %v", err, c.update.state, c.update.retries, r.urls)
	}
	srv.with(func(s *tokenServer) { s.tokenCode = 0 })
	c.SetClockSkew(time.Minute)
	c.retryInstall()
	pumpUntil(t, c, func() bool { return c.update.state != "installing" })
	if c.update.state != "installed" || len(r.urls) != 1 {
		t.Fatalf("the retry: %s %s, RAUC ran for %v", c.update.state, c.update.err, r.urls)
	}

	c, srv, r = restrictedCore(t, true, false)
	srv.with(func(s *tokenServer) { s.tokenCode = http.StatusServiceUnavailable })
	checkOffer(t, c, true)
	_ = userInstall(t, c)
	for c.update.state == "installing" {
		c.SetClockSkew(time.Hour)
		c.retryInstall()
		pumpUntil(t, c, func() bool { return c.update.state != "installing" || !c.update.retryAt.IsZero() })
	}
	if c.update.state != "failed" || c.update.retries != updateRetries || c.update.err != tr.T("Downloading the update failed") || len(r.urls) != 0 {
		t.Fatalf("always busy: %s after %d retries: %s, RAUC ran for %v", c.update.state, c.update.retries, c.update.err, r.urls)
	}

	c, _, r = restrictedCore(t, true, false)
	c.update.offer = &updateOffer{Version: "9.0.0", URL: "ftp://x/phone.raucb", Restricted: true}
	if err := userInstall(t, c); err != nil || c.update.retryAt.IsZero() || len(r.urls) != 0 {
		t.Fatalf("no proxy: %v, RAUC ran for %v", err, r.urls)
	}
}

// A public bundle goes as before: by hand straight from the server,
// automatically through the proxy, never with a token asked for or sent.
func TestPublicBundleNoToken(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		c, srv, r := restrictedCore(t, false, false)
		checkOffer(t, c, false)
		if automatic {
			c.startInstall(nil)
			pumpUntil(t, c, func() bool { return c.update.state != "installing" })
		} else if err := userInstall(t, c); err != nil {
			t.Fatal(err)
		}
		if c.update.state != "installed" || len(r.urls) != 1 {
			t.Fatalf("automatic %v: %s, RAUC ran for %v", automatic, c.update.state, r.urls)
		}
		if straight := r.urls[0] == srv.URL+"/phone.raucb"; straight == automatic {
			t.Fatalf("automatic %v: RAUC downloaded from %s", automatic, r.urls[0])
		}
		srv.with(func(s *tokenServer) {
			if s.posts != 0 || len(s.auths) != 4 || strings.Join(s.auths, "") != "" {
				t.Fatalf("automatic %v: %d token requests, the server saw %q", automatic, s.posts, s.auths)
			}
		})
	}
}
