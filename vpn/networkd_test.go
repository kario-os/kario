package vpn

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden networkd files in testdata")

// The networkd files of a split and a full tunnel, and of a split tunnel
// with per-app routing (its own table), against testdata/*.golden
// (go test ./vpn -run Networkd -update rewrites them).
func TestNetworkdGolden(t *testing.T) {
	for _, name := range []string{"split", "full", "full-v6", "split-apps"} {
		conf, own := strings.CutSuffix(name, "-apps")
		raw, err := os.ReadFile(filepath.Join("testdata", conf+".conf"))
		if err != nil {
			t.Fatal(err)
		}
		c, err := Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out strings.Builder
		for _, f := range NetworkdFiles(c, "/run/systemd/network", own) {
			out.WriteString("== " + f.Name)
			if f.Secret {
				out.WriteString(" (secret)")
			}
			out.WriteString("\n" + f.Data)
		}
		golden := filepath.Join("testdata", name+".golden")
		if *update {
			if err := os.WriteFile(golden, []byte(out.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != string(want) {
			t.Errorf("%s: networkd files differ from %s:\n%s", name, golden, out.String())
		}
	}
}

// networkdLine is every line NetworkdFiles may write: a section, a key
// networkd knows with a value of the characters its values need, or
// nothing.
var networkdLine = regexp.MustCompile(`^(|\[(NetDev|WireGuard|WireGuardPeer|Match|Link|Network|RoutingPolicyRule)\]|` +
	`(Name|Kind|MTUBytes|PrivateKeyFile|ListenPort|FirewallMark|RouteTable|PublicKey|PresharedKeyFile|Endpoint|AllowedIPs|PersistentKeepalive|` +
	`RequiredForOnline|Address|DNS|Domains|DNSDefaultRoute|LinkLocalAddressing|IPv6AcceptRA|Table|SuppressPrefixLength|Priority|Family|InvertRule)=[A-Za-z0-9.:/+=\[\]~ _-]+)$`)

func checkNetworkd(t *testing.T, c Config) {
	t.Helper()
	for _, f := range append(NetworkdFiles(c, "/run/systemd/network", false), NetworkdFiles(c, "/run/systemd/network", true)...) {
		if f.Secret {
			if _, err := ParseKey(strings.TrimSuffix(f.Data, "\n")); err != nil {
				t.Fatalf("%s: %q", f.Name, f.Data)
			}
			continue
		}
		for _, l := range strings.Split(strings.TrimSuffix(f.Data, "\n"), "\n") {
			if !networkdLine.MatchString(l) {
				t.Fatalf("%s: line %q", f.Name, l)
			}
		}
	}
}

// Whatever a file holds, what reaches networkd is lines of the expected
// shape: a value cannot start a section or a key of its own.
func FuzzParse(f *testing.F) {
	for _, name := range []string{"split", "full", "full-v6"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name+".conf"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte(sample))
	f.Add([]byte("[Interface]\nAddress=10.0.0.2\n[Peer]\nPublicKey=" + testPub + "\nEndpoint=[::1]:1\nAllowedIPs=::/0\nPresharedKey=" + testPSK + "\n"))
	f.Add([]byte("[Interface]\nDNS = 1.1.1.1\nDNS=a.b\n#\n[peer]\nendpoint=x:1\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c, err := Parse(raw)
		if err != nil {
			if _, ok := err.(*Error); !ok {
				t.Fatalf("error of type %T", err)
			}
			return
		}
		if len(c.Addresses) == 0 || len(c.Peers) == 0 {
			t.Fatalf("accepted without an address or a peer: %+v", c)
		}
		if c.PrivateKey.IsZero() {
			c.PrivateKey = mustKey(testPriv)
		}
		checkNetworkd(t, c)
	})
}

// A full tunnel has its own table and the mark's rule already: per-app
// routing changes none of its files.
func TestNetworkdFullOwnTable(t *testing.T) {
	for _, name := range []string{"full", "full-v6"} {
		raw, _ := os.ReadFile(filepath.Join("testdata", name+".conf"))
		c, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		a, b := NetworkdFiles(c, "/run", false), NetworkdFiles(c, "/run", true)
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("%s: %s differs with its own table:\n%s", name, a[i].Name, b[i].Data)
			}
		}
	}
}

func TestNetworkdLines(t *testing.T) {
	for _, name := range []string{"split", "full", "full-v6"} {
		raw, _ := os.ReadFile(filepath.Join("testdata", name+".conf"))
		c, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		checkNetworkd(t, c)
	}
}
