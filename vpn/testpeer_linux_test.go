package vpn

import (
	"context"
	"net"
	"testing"
	"time"
)

// The peer's DNS server answers its name, with nothing for the parent
// domains and NXDOMAIN for the rest; its profile for the phone is a full
// tunnel the phone's own key pair completes.
func TestTestPeerDNSAndConf(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	k, _ := GenerateKey()
	p := &TestPeer{dns: pc, key: k}
	go p.serveDNS()
	defer pc.Close()
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp4", pc.LocalAddr().String())
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := r.LookupNetIP(ctx, "ip4", TestName)
	if err != nil || len(got) != 1 || got[0] != TestTunnelPeer.Addr() {
		t.Fatalf("%s: %v %v", TestName, got, err)
	}
	if got, err := r.LookupNetIP(ctx, "ip4", "other.test"); err == nil {
		t.Errorf("other.test resolved to %v", got)
	}
	if got, err := r.LookupNetIP(ctx, "ip4", "kario-vpn.test"); err == nil || len(got) != 0 {
		t.Errorf("the parent domain resolved to %v", got)
	}
	c, err := Parse([]byte(p.Conf()))
	if err != nil {
		t.Fatal(err)
	}
	if !c.FullTunnel() || !c.PrivateKey.IsZero() || c.Peers[0].PublicKey != k.Public() || c.DNS[0] != TestTunnelPeer.Addr() {
		t.Errorf("%+v", c)
	}
}
