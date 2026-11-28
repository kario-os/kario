package push

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"gitee.com/Trisia/gotlcp/tlcp"
	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/smx509"

	"github.com/kario-os/kario/statefile"
)

// TLCP (GB/T 38636, TLS on the Chinese SM algorithms) is the alternative
// transport where SM algorithms are required (payments and messaging in
// mainland China): SM2 certificates, SM2 key exchange, SM4 records with SM3
// MACs. It pairs a signing certificate with an encryption one. The prototype
// gateway makes its own SM2 CA on first start; cert.pem is that CA, which
// clients pin (Client picks TLCP when the pinned certificate is an SM2 one).

// GatewayTLCP loads or makes the gateway's TLCP credentials in dir. A
// directory a TLS gateway left behind gets a TLCP CA (and a new cert.pem to
// pin); so do credentials of which any part is missing or does not parse
// (written in part when the power went), or whose cert.pem is not the CA of
// the signing pair, the files set aside.
func GatewayTLCP(dir string, hosts ...string) (*tlcp.Config, error) {
	sign, enc := filepath.Join(dir, "sign"), filepath.Join(dir, "enc")
	cfg, err := loadTLCP(dir, sign, enc)
	if err == nil {
		return cfg, nil
	}
	parts := []string{sign + ".pem", sign + ".key", enc + ".pem", enc + ".key"}
	if _, serr := os.Stat(sign + ".pem"); serr == nil {
		parts = append(parts, CertPath(dir))
	}
	if err := setAside(dir, err, parts...); err != nil {
		return nil, err
	}
	if err := makeTLCP(dir, sign, enc, hosts); err != nil {
		return nil, err
	}
	return loadTLCP(dir, sign, enc)
}

func loadTLCP(dir, sign, enc string) (*tlcp.Config, error) {
	sc, err := tlcp.LoadX509KeyPair(sign+".pem", sign+".key")
	if err != nil {
		return nil, err
	}
	ec, err := tlcp.LoadX509KeyPair(enc+".pem", enc+".key")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(CertPath(dir))
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, errors.New("cert.pem: no certificate")
	}
	ca, err := smx509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cert.pem: %w", err)
	}
	leaf, err := smx509.ParseCertificate(sc.Certificate[0])
	if err != nil {
		return nil, err
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		return nil, fmt.Errorf("cert.pem did not sign sign.pem: %w", err)
	}
	return &tlcp.Config{Certificates: []tlcp.Certificate{sc, ec}}, nil
}

func makeTLCP(dir, sign, enc string, hosts []string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	caKey, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	ca := &smx509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "phone push gateway CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		// GmSSL refuses certificates valid for more than 3653 days.
		NotAfter: time.Now().AddDate(9, 0, 0),
		KeyUsage: smx509.KeyUsageCertSign | smx509.KeyUsageCRLSign,
		IsCA:     true, BasicConstraintsValid: true,
	}
	der, err := smx509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	if ca, err = smx509.ParseCertificate(der); err != nil {
		return err
	}
	for _, leaf := range []struct {
		path  string
		usage smx509.KeyUsage
	}{
		{sign, smx509.KeyUsageDigitalSignature},
		{enc, smx509.KeyUsageKeyEncipherment | smx509.KeyUsageDataEncipherment | smx509.KeyUsageKeyAgreement},
	} {
		key, err := sm2.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		tmpl := &smx509.Certificate{
			SerialNumber: serial(),
			Subject:      pkix.Name{CommonName: "phone push gateway"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().AddDate(5, 0, 0),
			KeyUsage:     leaf.usage,
			ExtKeyUsage:  []smx509.ExtKeyUsage{smx509.ExtKeyUsageServerAuth},
		}
		for _, h := range hosts {
			if ip := net.ParseIP(h); ip != nil {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			} else {
				tmpl.DNSNames = append(tmpl.DNSNames, h)
			}
		}
		der, err := smx509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			return err
		}
		kd, err := smx509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		if err := statefile.Write(leaf.path+".key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
			return err
		}
		if err := statefile.Write(leaf.path+".pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
			return err
		}
	}
	// The CA's key is not kept: the two leaves are all it ever signs.
	return statefile.Write(CertPath(dir), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0o644)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}

// isSM2 reports whether a certificate's key is on the SM2 curve.
func isSM2(der []byte) bool {
	c, err := smx509.ParseCertificate(der)
	if err != nil {
		return false
	}
	k, ok := c.PublicKey.(*ecdsa.PublicKey)
	return ok && k.Curve == sm2.P256()
}
