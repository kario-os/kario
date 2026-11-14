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

// Client trusts only the certificate in pinned (a PEM file). The file is
// read at each handshake, so a client may start before the gateway has made
// it, and its kind picks the protocol: an SM2 certificate is the gateway's
// TLCP CA (the server's certificates must chain to it), anything else is
// TLS 1.3 where the server must present exactly that certificate. With
// pinned empty it is a plain client (development gateways without TLS).
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

func dialPinned(ctx context.Context, network, addr, pinned string) (net.Conn, error) {
	raw, err := os.ReadFile(pinned)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(raw)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("no certificate in the pinned file")
	}
	if isSM2(b.Bytes) {
		ca, err := smx509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, err
		}
		pool := smx509.NewCertPool()
		pool.AddCert(ca)
		host, _, _ := net.SplitHostPort(addr)
		d := &tlcp.Dialer{Config: &tlcp.Config{RootCAs: pool, ServerName: host}}
		return d.DialContext(ctx, network, addr)
	}
	want := b.Bytes
	d := &tls.Dialer{Config: &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The standard chain check is replaced by the pin below.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || !bytes.Equal(raw[0], want) {
				return errors.New("push gateway certificate does not match the pinned one")
			}
			return nil
		},
	}}
	return d.DialContext(ctx, network, addr)
}
