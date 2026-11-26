package push

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitee.com/Trisia/gotlcp/tlcp"
)

func TestPinnedGateway(t *testing.T) {
	serve := func(dir string) string {
		cfg, err := GatewayTLS(dir, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go http.Serve(tls.NewListener(ln, cfg), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
		return "https://" + ln.Addr().String()
	}
	dir, other := t.TempDir(), t.TempDir()
	url, otherURL := serve(dir), serve(other)
	c, err := Client(CertPath(dir), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := c.Get(otherURL); err == nil {
		t.Fatal("a gateway with another certificate was trusted")
	}
	late, _ := Client(filepath.Join(t.TempDir(), "cert.pem"), time.Second)
	if _, err := late.Get(url); err == nil {
		t.Fatal("trusted without a pinned certificate")
	}
}

func TestPinnedGatewayTLCP(t *testing.T) {
	serve := func(dir string) string {
		cfg, err := GatewayTLCP(dir, "127.0.0.1", "localhost")
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go http.Serve(tlcp.NewListener(ln, cfg), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "over TLCP") }))
		return "https://" + ln.Addr().String()
	}
	dir, other := t.TempDir(), t.TempDir()
	url, otherURL := serve(dir), serve(other)
	if _, err := GatewayTLCP(dir, "127.0.0.1"); err != nil {
		t.Fatalf("reloading the kept credentials: %v", err)
	}
	c, _ := Client(CertPath(dir), 5*time.Second)
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "over TLCP" {
		t.Fatalf("body %q", body)
	}
	if _, err := c.Get(otherURL); err == nil {
		t.Fatal("a gateway under another SM2 CA was trusted")
	}
	tlsDir := t.TempDir()
	if _, err := GatewayTLS(tlsDir, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	tlsPinned, _ := Client(CertPath(tlsDir), 2*time.Second)
	if _, err := tlsPinned.Get(url); err == nil {
		t.Fatal("a TLS pin accepted a TLCP gateway")
	}
}

// The gateway's certificate rotated (11-platform-architecture.md §15.1),
// TLS and TLCP: an image pinning the old certificate and the next one
// reaches the gateway with either; one pinning the old alone not the one
// with the next; once the old one left the pin file, not the old gateway.
// A pin file mixing the two kinds is refused.
func TestPinRotation(t *testing.T) {
	serveTLS := func(dir string) string {
		cfg, err := GatewayTLS(dir, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		go http.Serve(tls.NewListener(ln, cfg), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
		return "https://" + ln.Addr().String()
	}
	serveTLCP := func(dir string) string {
		cfg, err := GatewayTLCP(dir, "127.0.0.1", "localhost")
		if err != nil {
			t.Fatal(err)
		}
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		go http.Serve(tlcp.NewListener(ln, cfg), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
		return "https://" + ln.Addr().String()
	}
	pins := t.TempDir()
	pinFile := func(name string, dirs ...string) string {
		var all []byte
		for _, d := range dirs {
			raw, err := os.ReadFile(CertPath(d))
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, raw...)
		}
		path := filepath.Join(pins, name)
		if err := os.WriteFile(path, all, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for kind, serve := range map[string]func(string) string{"TLS": serveTLS, "TLCP": serveTLCP} {
		oldDir, nextDir := t.TempDir(), t.TempDir()
		oldURL, nextURL := serve(oldDir), serve(nextDir)
		for name, c := range map[string]struct {
			pin string
			url string
			ok  bool
		}{
			"old image, old gateway":   {pinFile(kind+"-before", oldDir), oldURL, true},
			"old image, next gateway":  {pinFile(kind+"-before", oldDir), nextURL, false},
			"both pinned, old":         {pinFile(kind+"-both", oldDir, nextDir), oldURL, true},
			"both pinned, next":        {pinFile(kind+"-both", oldDir, nextDir), nextURL, true},
			"old one retired, next":    {pinFile(kind+"-after", nextDir), nextURL, true},
			"old one retired, the old": {pinFile(kind+"-after", nextDir), oldURL, false},
		} {
			cl, _ := Client(c.pin, 5*time.Second)
			resp, err := cl.Get(c.url)
			if err == nil {
				resp.Body.Close()
			}
			if c.ok != (err == nil) {
				t.Errorf("%s, %s: %v", kind, name, err)
			}
		}
	}
	tlsDir, tlcpDir := t.TempDir(), t.TempDir()
	url := serveTLS(tlsDir)
	serveTLCP(tlcpDir)
	mixed, _ := Client(pinFile("mixed", tlsDir, tlcpDir), 2*time.Second)
	if _, err := mixed.Get(url); err == nil || !strings.Contains(err.Error(), "mixes") {
		t.Fatalf("a pin file of both kinds: %v", err)
	}
}
