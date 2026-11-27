package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bundleServer serves a bundle with range requests, each piece slowly.
func bundleServer(t *testing.T, bundle []byte, slow time.Duration, sent *atomic.Int64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(slow)
		http.ServeContent(&countingWriter{ResponseWriter: w, n: sent}, r, "phone.raucb", time.Time{}, bytes.NewReader(bundle))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type countingWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// rangeGet downloads bytes [from, from+n) of url, as RAUC's streaming does.
func rangeGet(url string, from, n int) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, from+n-1))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode != http.StatusPartialContent {
		err = errors.New(resp.Status)
	}
	return b, err
}

// The proxy passes range requests through to the update server; stopped,
// it ends a download under way and refuses the next.
func TestInstallProxy(t *testing.T) {
	bundle := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(bundle)
	// The loopback's socket buffers hold a whole bundle this size, so the
	// server holds the rest of a whole download back until its request has
	// been up 10 seconds; a stopped proxy ends that request before.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			http.ServeContent(w, r, "phone.raucb", time.Time{}, bytes.NewReader(bundle))
			return
		}
		_, _ = w.Write(bundle[:len(bundle)/8])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
			_, _ = w.Write(bundle[len(bundle)/8:])
		}
	}))
	t.Cleanup(srv.Close)
	if _, err := newInstallProxy("phone.raucb", nil); err == nil {
		t.Fatal("a proxy for no server")
	}
	p, err := newInstallProxy(srv.URL+"/phone.raucb?v=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if !strings.HasPrefix(p.url, "http://127.0.0.1:") || !strings.HasSuffix(p.url, "/phone.raucb") {
		t.Fatalf("proxy at %s", p.url)
	}
	if b, err := rangeGet(p.url, 0, 4); err != nil || !bytes.Equal(b, bundle[:4]) {
		t.Fatalf("the first bytes: %v", err)
	}
	if b, err := rangeGet(p.url, 300000, 65536); err != nil || !bytes.Equal(b, bundle[300000:300000+65536]) {
		t.Fatalf("a range: %v", err)
	}
	if _, err := rangeGet(strings.TrimSuffix(p.url, "phone.raucb")+"other.raucb", 0, 4); err == nil {
		t.Fatal("served another file")
	}

	// A download under way: half of it read, the proxy stops, the rest
	// does not come.
	resp, err := http.Get(p.url)
	if err != nil {
		t.Fatal(err)
	}
	half := make([]byte, len(bundle)/8)
	if _, err := io.ReadFull(resp.Body, half); err != nil {
		t.Fatal(err)
	}
	p.stop()
	rest, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil && len(half)+len(rest) == len(bundle) {
		t.Fatal("the download went on after the stop")
	}
	if _, err := rangeGet(p.url, 0, 4); err == nil {
		t.Fatal("a download after the stop")
	}
}

// The proxy's connections to the update server after the first leave from
// the first one's address: with that address gone (the Wi-Fi the download
// began on), nothing more is fetched, over another network or at all.
func TestInstallProxyKeepsItsNetwork(t *testing.T) {
	bundle := make([]byte, 1<<16)
	rand.New(rand.NewSource(3)).Read(bundle)
	var mu sync.Mutex
	var from []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		mu.Lock()
		from = append(from, host)
		mu.Unlock()
		http.ServeContent(w, r, "phone.raucb", time.Time{}, bytes.NewReader(bundle))
	}))
	defer srv.Close()
	p, err := newInstallProxy(srv.URL+"/phone.raucb", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	again := func(addr string) error {
		p.mu.Lock()
		p.from = net.ParseIP(addr)
		p.mu.Unlock()
		p.tr.CloseIdleConnections()
		_, err := rangeGet(p.url, 100, 10)
		return err
	}
	if b, err := rangeGet(p.url, 0, 4); err != nil || !bytes.Equal(b, bundle[:4]) || !p.from.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("the first range: %v, from %v", err, p.from)
	}
	saw := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, from...)
	}
	if l, err := net.Listen("tcp", "127.0.0.2:0"); err == nil {
		l.Close()
		if err := again("127.0.0.2"); err != nil || saw()[len(saw())-1] != "127.0.0.2" {
			t.Fatalf("from another address of the machine: %v, the server saw %v", err, saw())
		}
	}
	before := len(saw())
	if err := again("192.0.2.1"); err == nil || len(saw()) != before {
		t.Fatalf("its address gone, the proxy still fetched: %v, the server saw %v", err, saw())
	}
}

// An automatic install of a full bundle whose battery runs low off the
// charger part way: the shell stops its download through the proxy, RAUC
// (here a stand-in downloading as RAUC does) fails as when the network
// goes, the update waits, and once on the charger it installs from the
// start of what is left (RAUC's adaptive install takes the blocks already
// in the slot).
func TestAutoInstallStopsThroughProxy(t *testing.T) {
	if !alone(t) {
		return
	}
	const piece = 64 << 10
	bundle := make([]byte, 16*piece)
	rand.New(rand.NewSource(2)).Read(bundle)
	var sent atomic.Int64
	srv := bundleServer(t, bundle, 30*time.Millisecond, &sent)
	c := updateCore(t, "true")
	c.settings.AutoSystemUpdate, c.cfg.UpdateURL = true, srv.URL
	c.update.unmetered = func(then func(bool)) { then(true) }
	var pieces atomic.Int64
	var urls []string
	c.update.install = func(url string, line func(string)) (string, error) {
		urls = append(urls, url)
		for off := 0; off < len(bundle); off += piece {
			b, err := rangeGet(url, off, piece)
			if err != nil || !bytes.Equal(b, bundle[off:off+piece]) {
				return fmt.Sprintf("Failed to stream bundle %s: %v", url, err), errors.New("exit status 1")
			}
			pieces.Add(1)
		}
		return "", os.WriteFile(filepath.Join(c.update.slots, "boot"), []byte("slot=b\ngood=a\ntries=3\nbooted=a\n"), 0o644)
	}
	c.update.offer = &updateOffer{Version: "0.8.0", URL: srv.URL + "/phone.raucb"}
	c.startInstall(nil)
	for pieces.Load() < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	c.powerOverride = &Power{Level: 20}
	c.updateTick()
	pumpUntil(t, c, func() bool { return c.update.state != "installing" })
	if c.update.state != "idle" || c.update.paused == "" || pieces.Load() >= 16 || c.update.proxy != nil {
		t.Fatalf("after the stop: %s, paused %q, %d pieces", c.update.state, c.update.paused, pieces.Load())
	}
	first := sent.Load()
	c.powerOverride = &Power{Level: 20, Charging: true}
	c.SetClockSkew(autoPauseCheck)
	c.updateTick()
	pumpUntil(t, c, func() bool { return c.update.state == "installed" })
	if len(urls) != 2 || urls[0] == urls[1] || sent.Load()-first < int64(len(bundle)) {
		t.Fatalf("installs %v; the second downloaded %d bytes", urls, sent.Load()-first)
	}
	t.Logf("the stopped install downloaded %d of %d bytes", first, len(bundle))
}
