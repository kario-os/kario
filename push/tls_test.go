package push

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"path/filepath"
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
