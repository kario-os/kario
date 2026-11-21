package shell

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"sync"
)

// installProxy serves the bundle an automatic install downloads to RAUC
// on the loopback, from the update server. RAUC can neither pause nor
// cancel an install, and its streaming helper cannot be stopped from
// outside without RAUC failing on its own socket; cut ends every download
// through the proxy and refuses the next, so that RAUC fails the install
// as when the network goes away, and cleans up as then (updateauto.go).
type installProxy struct {
	url   string
	srv   *http.Server
	mu    sync.Mutex
	cut   bool
	conns map[net.Conn]bool
}

func newInstallProxy(bundle string) (*installProxy, error) {
	to, err := url.Parse(bundle)
	if err != nil || to.Host == "" || (to.Scheme != "http" && to.Scheme != "https") {
		return nil, errors.New("not a bundle on a server")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var token [8]byte
	_, _ = rand.Read(token[:])
	at := "/" + hex.EncodeToString(token[:]) + "/" + path.Base(to.Path)
	p := &installProxy{url: "http://" + ln.Addr().String() + at, conns: map[net.Conn]bool{}}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			u := *to
			r.Out.URL, r.Out.Host = &u, to.Host
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	p.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			cut := p.cut
			p.mu.Unlock()
			if cut || r.URL.Path != at {
				http.Error(w, "the download was stopped", http.StatusServiceUnavailable)
				return
			}
			rp.ServeHTTP(w, r)
		}),
		ConnState: func(c net.Conn, s http.ConnState) {
			p.mu.Lock()
			defer p.mu.Unlock()
			switch s {
			case http.StateNew:
				p.conns[c] = true
			case http.StateClosed, http.StateHijacked:
				delete(p.conns, c)
			}
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// stop ends the downloads under way and refuses the next.
func (p *installProxy) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cut = true
	for c := range p.conns {
		_ = c.Close()
	}
}

func (p *installProxy) close() { _ = p.srv.Close() }
