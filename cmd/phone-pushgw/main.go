// phone-pushgw stands in for the cloud push gateway (the role APNs, FCM or a
// vendor push service plays): the open core, package pushgw, with no app
// server authentication, no rate limits and one replica set. App servers
// post messages for a token; every device keeps one long-lived connection
// over which they arrive. Messages are stored and forwarded: they wait,
// until their TTL, for the device to come online and acknowledge them.
//
//	phone-pushgw [--listen 127.0.0.1:8790] [--addr-file PATH] [--heartbeat 30s]
//	             [--state FILE] [--trust-root ROOT.pub] [--tls-dir DIR]
//
// The HTTP API is in package pushgw. With --trust-root a device must
// register first: a keystored attestation, chained to the manufacturer root,
// of its push identity. Without it devices are trusted on first use
// (development). With --state the signing key, devices, install generations
// and queued messages survive a restart, so tokens app servers hold keep
// working. With --tls-dir it serves HTTPS with a certificate kept there
// (cert.pem, which clients pin). With --cluster-key and --peer-addr-file it
// is one instance of a cluster (pushgw/cluster.go).
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/pushgw"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8790", "address for app servers and devices")
	addrFile := flag.String("addr-file", "", "write the gateway URL here once listening (for --listen with port 0)")
	heartbeat := flag.Duration("heartbeat", 30*time.Second, "keep-alive interval on device connections")
	statePath := flag.String("state", "", "keep the signing key, devices and queued messages in this file")
	trustRoot := flag.String("trust-root", "", "manufacturer root (root.pub): devices must register with an attestation chained to it")
	tlsDir := flag.String("tls-dir", "", "serve HTTPS with the certificate kept here (made on first start; clients pin cert.pem)")
	useTLCP := flag.Bool("tlcp", false, "with --tls-dir: serve over TLCP (GB/T 38636, SM2/SM3/SM4) instead of TLS 1.3")
	clusterKey := flag.String("cluster-key", "", "join a cluster: the secret its gateways share (base64, 32 bytes)")
	peerFiles := flag.String("peer-addr-file", "", "with --cluster-key: the peers' --addr-file paths, comma-separated (read when needed)")
	flag.Parse()
	g, err := pushgw.New(*statePath, *trustRoot, *heartbeat)
	if err != nil {
		log.Fatal(err)
	}
	if *clusterKey != "" {
		key, err := pushgw.LoadClusterKey(*clusterKey)
		if err != nil {
			log.Fatal(err)
		}
		pin := ""
		if *tlsDir != "" {
			pin = push.CertPath(*tlsDir)
		}
		client, err := push.Client(pin, 10*time.Second)
		if err != nil {
			log.Fatal(err)
		}
		var peers []string
		for _, f := range strings.Split(*peerFiles, ",") {
			if f = strings.TrimSpace(f); f != "" {
				peers = append(peers, f)
			}
		}
		g.Join(key, client, peers)
	}
	ln, url, err := pushgw.Listen(*listen, *tlsDir, *useTLCP)
	if err != nil {
		log.Fatal(err)
	}
	if *addrFile != "" {
		tmp := *addrFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(url+"\n"), 0o644); err != nil {
			log.Fatal(err)
		}
		if err := os.Rename(tmp, *addrFile); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("phone-pushgw: %s", url)
	log.Fatal(http.Serve(ln, g.Routes()))
}
