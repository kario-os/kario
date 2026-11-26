package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"sync"
	"time"
)

// installProxy serves the bundle an automatic install downloads to RAUC
// on the loopback, from the update server. RAUC can neither pause nor
// cancel an install, and its streaming helper cannot be stopped from
// outside without RAUC failing on its own socket; cut ends every download
// through the proxy and refuses the next, so that RAUC fails the install
// as when the network goes away, and cleans up as then (updateauto.go).
//
// Every connection to the server after the first leaves from the first
// one's address: the network the install began on, the Wi-Fi its
// conditions held on. When that Wi-Fi goes, its address goes with it and
// the download fails as when the network goes away, rather than going on
// over mobile data until the shell next looks at the network.
type installProxy struct {
	url   string
	srv   *http.Server
	mu    sync.Mutex
	cut   bool
	conns map[net.Conn]bool
	from  net.IP
	tr    *http.Transport
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
	p := &installProxy{url: "http://" + ln.Addr().String() + at, conns: map[net.Conn]bool{}, tr: http.DefaultTransport.(*http.Transport).Clone()}
	p.tr.DialContext = p.dial
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			u := *to
			r.Out.URL, r.Out.Host = &u, to.Host
		},
		Transport: p.tr,
		ErrorLog:  log.New(io.Discard, "", 0),
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

// dial connects to the update server from the address the first
// connection had.
func (p *installProxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	p.mu.Lock()
	from := p.from
	p.mu.Unlock()
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if from != nil {
		d.LocalAddr = &net.TCPAddr{IP: from}
	}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		if from != nil {
			return nil, fmt.Errorf("from %s, where the download began: %w", from, err)
		}
		return nil, err
	}
	if a, ok := conn.LocalAddr().(*net.TCPAddr); ok && from == nil {
		p.mu.Lock()
		if p.from == nil {
			p.from = a.IP
		}
		p.mu.Unlock()
	}
	return conn, nil
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

func (p *installProxy) close() {
	_ = p.srv.Close()
	p.tr.CloseIdleConnections()
}
