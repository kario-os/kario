package push

import (
	"bytes"
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

// Credentials a power cut, a kill or a full disk left part written (a file
// cut short or missing) are made again rather than keeping the gateway from
// starting, and the cert.pem clients pin then verifies the gateway.
func TestDamagedCredentialsAreMadeAgain(t *testing.T) {
	listen := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		return ln
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	for _, k := range []struct {
		kind  string
		files []string
		serve func(dir string) (string, error)
	}{
		{"TLS", []string{"cert.pem", "key.pem"}, func(dir string) (string, error) {
			cfg, err := GatewayTLS(dir, "127.0.0.1")
			if err != nil {
				return "", err
			}
			ln := listen()
			go http.Serve(tls.NewListener(ln, cfg), ok)
			return "https://" + ln.Addr().String(), nil
		}},
		{"TLCP", []string{"sign.pem", "sign.key", "enc.pem", "enc.key", "cert.pem"}, func(dir string) (string, error) {
			cfg, err := GatewayTLCP(dir, "127.0.0.1")
			if err != nil {
				return "", err
			}
			ln := listen()
			go http.Serve(tlcp.NewListener(ln, cfg), ok)
			return "https://" + ln.Addr().String(), nil
		}},
	} {
		for _, name := range k.files {
			for _, how := range []string{"cut short", "missing"} {
				dir := t.TempDir()
				if _, err := k.serve(dir); err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(dir, name)
				if how == "missing" {
					_ = os.Remove(p)
				} else if st, err := os.Stat(p); err != nil || os.Truncate(p, st.Size()/2) != nil {
					t.Fatal(err)
				}
				cut, _ := os.ReadFile(p)
				url, err := k.serve(dir)
				if err != nil {
					t.Errorf("%s, %s %s: %v", k.kind, name, how, err)
					continue
				}
				if kept, _ := os.ReadFile(p + ".damaged"); how == "cut short" && !bytes.Equal(kept, cut) {
					t.Errorf("%s, %s cut short: not set aside as %s.damaged", k.kind, name, name)
				}
				c, _ := Client(CertPath(dir), 5*time.Second)
				resp, err := c.Get(url)
				if err != nil {
					t.Errorf("%s, %s %s: the pinned certificate: %v", k.kind, name, how, err)
					continue
				}
				resp.Body.Close()
			}
		}
	}
}

// A key that cannot be read (its mode, a mount gone) is not damage: the
// gateway refuses to start, and its credentials are left as they are.
func TestUnreadableCredentialsAreKept(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a file of mode 0")
	}
	for kind, serve := range map[string]func(string) error{
		"TLS":  func(dir string) error { _, err := GatewayTLS(dir, "127.0.0.1"); return err },
		"TLCP": func(dir string) error { _, err := GatewayTLCP(dir, "127.0.0.1"); return err },
	} {
		dir := t.TempDir()
		if err := serve(dir); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(dir, "key.pem")
		if kind == "TLCP" {
			key = filepath.Join(dir, "sign.key")
		}
		pin, _ := os.ReadFile(CertPath(dir))
		if err := os.Chmod(key, 0); err != nil {
			t.Fatal(err)
		}
		if err := serve(dir); err == nil {
			t.Errorf("%s: started without reading its key", kind)
		}
		if now, _ := os.ReadFile(CertPath(dir)); !bytes.Equal(now, pin) {
			t.Errorf("%s: cert.pem made again for a key it could not read", kind)
		}
		_ = os.Chmod(key, 0o600)
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
