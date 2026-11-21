package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gitee.com/Trisia/gotlcp/tlcp"
	"github.com/emmansun/gmsm/smx509"
)

// The device's connection to the gateway and app servers' calls to it go
// over TLS. The prototype gateway makes its own certificate on first start
// and keeps it; clients pin it (in production: a public CA, with the
// gateway's key pinned in the image).

// GatewayTLS loads or makes the gateway's certificate in dir (cert.pem,
// key.pem), valid for the given hosts.
func GatewayTLS(dir string, hosts ...string) (*tls.Config, error) {
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := makeCert(dir, certPath, keyPath, hosts); err != nil {
			return nil, err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, nil
}

// CertPath is where GatewayTLS keeps the certificate clients pin.
func CertPath(dir string) string { return filepath.Join(dir, "cert.pem") }

func makeCert(dir, certPath, keyPath string, hosts []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "phone push gateway"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kd, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// Client trusts only the certificates in pinned (a PEM file). The file is
// read at each handshake, so a client may start before the gateway has made
// it, and its kind picks the protocol: SM2 certificates are the gateway's
// TLCP CAs (the server's certificates must chain to one of them), anything
// else is TLS 1.3 where the server must present exactly one of those
// certificates. More than one is the gateway's certificate while it is
// rotated (11-platform-architecture.md §15.1): the one it presents and the
// next, or the one it replaced; they are all of one kind. With pinned
// empty it is a plain client (development gateways without TLS).
func Client(pinned string, timeout time.Duration) (*http.Client, error) {
	c := &http.Client{Timeout: timeout}
	if pinned == "" {
		return c, nil
	}
	c.Transport = &http.Transport{DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialPinned(ctx, network, addr, pinned)
	}}
	return c, nil
}

// readPins is the certificates in the pinned file (DER) and whether they
// are SM2 ones.
func readPins(pinned string) ([][]byte, bool, error) {
	raw, err := os.ReadFile(pinned)
	if err != nil {
		return nil, false, err
	}
	var pins [][]byte
	sm := false
	for {
		var b *pem.Block
		if b, raw = pem.Decode(raw); b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		if len(pins) > 0 && isSM2(b.Bytes) != sm {
			return nil, false, errors.New("the pinned file mixes SM2 and other certificates")
		}
		sm = isSM2(b.Bytes)
		pins = append(pins, b.Bytes)
	}
	if len(pins) == 0 {
		return nil, false, errors.New("no certificate in the pinned file")
	}
	return pins, sm, nil
}

func dialPinned(ctx context.Context, network, addr, pinned string) (net.Conn, error) {
	pins, sm, err := readPins(pinned)
	if err != nil {
		return nil, err
	}
	if sm {
		pool := smx509.NewCertPool()
		for _, der := range pins {
			ca, err := smx509.ParseCertificate(der)
			if err != nil {
				return nil, err
			}
			pool.AddCert(ca)
		}
		host, _, _ := net.SplitHostPort(addr)
		d := &tlcp.Dialer{Config: &tlcp.Config{RootCAs: pool, ServerName: host}}
		return d.DialContext(ctx, network, addr)
	}
	d := &tls.Dialer{Config: &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The standard chain check is replaced by the pin below: the
		// gateway's own certificate names the address it listens on
		// (0.0.0.0, localhost), not the one a phone dials (10.0.2.2 under
		// QEMU), and a phone may not have the time yet.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, want := range pins {
				if len(raw) > 0 && bytes.Equal(raw[0], want) {
					return nil
				}
			}
			return errors.New("push gateway certificate does not match the pinned one")
		},
	}}
	return d.DialContext(ctx, network, addr)
}
