package shell

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The emulator's enterprise Wi-Fi access point for the selftest: hostapd
// (BSD-3-Clause, a test package of the image) with its own EAP server,
// PEAP and TTLS, on a simulated radio iwd leaves alone (the third
// mac80211_hwsim radio; the image's iwd ignores it). Its CA and the
// server's certificate are made here, at test time, so no key is in git;
// while it is up iwd checks enterprise servers against that CA.

const hostapdBin = "/usr/sbin/hostapd"

// eapTestDir holds the access point's files. iwd reads the CA from it and
// has a /tmp of its own (PrivateTmp), where the selftest's run directory is.
const eapTestDir = "/run/phone-eap-test"

// testNetIPv4 is the phone's address on the access point, which runs no
// DHCP server: iwd reports a network connected once IPv4 is configured.
const testNetIPv4 = "[IPv4]\nAddress=192.168.250.2\nNetmask=255.255.255.0\n"

// freeRadio is a Wi-Fi interface iwd does not manage.
func (r *iwdRadio) freeRadio() (string, error) {
	o, err := r.load()
	if err != nil {
		return "", err
	}
	managed := map[string]bool{}
	for _, d := range o.devices(false) {
		if n, _ := o.prop(d, iwdDevice, "Name").(string); n != "" {
			managed[n] = true
		}
	}
	ents, _ := os.ReadDir("/sys/class/net")
	for _, e := range ents {
		if _, err := os.Stat(filepath.Join("/sys/class/net", e.Name(), "wireless")); err == nil && !managed[e.Name()] {
			return e.Name(), nil
		}
	}
	return "", errors.New("no Wi-Fi radio outside iwd")
}

// canEAP: hostapd is installed and has a radio.
func (r *iwdRadio) canEAP() bool {
	if _, err := os.Stat(hostapdBin); err != nil {
		return false
	}
	_, err := r.freeRadio()
	return err == nil
}

// startEAP puts the access point up in dir; the server takes user with
// pass inside PEAP (MSCHAPv2) or TTLS (MSCHAPv2 or PAP) and has a
// certificate for domain.
func (r *iwdRadio) startEAP(dir, ssid, user, pass, domain string) error {
	iface, err := r.freeRadio()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	caPEM, certPEM, keyPEM, err := testCerts(domain)
	if err != nil {
		return err
	}
	path := func(name string) string { return filepath.Join(dir, name) }
	conf := fmt.Sprintf("interface=%s\ndriver=nl80211\nssid=%s\nhw_mode=g\nchannel=6\nauth_algs=1\n"+
		"wpa=2\nwpa_key_mgmt=WPA-EAP\nrsn_pairwise=CCMP\nieee8021x=1\neap_server=1\n"+
		"eap_user_file=%s\nca_cert=%s\nserver_cert=%s\nprivate_key=%s\n",
		iface, ssid, path("eap_user"), path("ca.pem"), path("server.pem"), path("server.key"))
	users := fmt.Sprintf("* PEAP,TTLS\n%q MSCHAPV2,TTLS-MSCHAPV2,TTLS-PAP %q [2]\n", user, pass)
	for name, data := range map[string][]byte{"ca.pem": caPEM, "server.pem": certPEM, "server.key": keyPEM, "eap_user": []byte(users), "hostapd.conf": []byte(conf)} {
		mode := os.FileMode(0o600)
		if name == "ca.pem" {
			mode = 0o644
		}
		if err := os.WriteFile(path(name), data, mode); err != nil {
			return err
		}
	}
	logf, err := os.Create(path("hostapd.log"))
	if err != nil {
		return err
	}
	cmd := exec.Command(hostapdBin, path("hostapd.conf"))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		logf.Close()
		close(exited)
	}()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		raw, _ := os.ReadFile(path("hostapd.log"))
		if strings.Contains(string(raw), "AP-ENABLED") {
			break
		}
		select {
		case <-exited:
			return fmt.Errorf("hostapd on %s exited: %s", iface, strings.TrimSpace(string(raw)))
		default:
		}
		if time.Now().After(end) {
			return fmt.Errorf("hostapd on %s did not come up: %s", iface, strings.TrimSpace(string(raw)))
		}
	}
	r.mu.Lock()
	r.testCA, r.testNet = path("ca.pem"), ssid
	r.mu.Unlock()
	return nil
}

// testCerts makes a CA and a server certificate for domain (RSA, which
// every TLS stack takes), valid for a day.
func testCerts(domain string) (caPEM, certPEM, keyPEM []byte, err error) {
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Kario selftest CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, err
	}
	srv := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	srvDER, err := x509.CreateCertificate(rand.Reader, srv, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), nil
}
