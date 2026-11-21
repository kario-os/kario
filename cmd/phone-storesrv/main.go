// phone-storesrv is the simulated store backend devices are tested against,
// next to the device like phone-pushgw: the open core (package appstore)
// with a shared staff token, nothing more. The store a phone maker runs is
// built on the same core.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/appstore"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8793", "address to serve on")
	addrFile := flag.String("addr-file", "", "write the base URL here once listening")
	dir := flag.String("data", "/var/lib/phone-storesrv", "store root key, packages and state")
	rootOut := flag.String("root-out", "", "also write the store root's public key here (a device trusts it)")
	adminFile := flag.String("admin-token-file", "", "the staff token (made on first start if missing)")
	flag.Parse()
	log.SetPrefix("phone-storesrv: ")
	store, err := appstore.Open(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if *rootOut != "" {
		if err := os.MkdirAll(filepath.Dir(*rootOut), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*rootOut, []byte(appstore.PublicText(store)), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	admin := ""
	if *adminFile != "" {
		if raw, err := os.ReadFile(*adminFile); err == nil {
			admin = strings.TrimSpace(string(raw))
		} else {
			b := make([]byte, 24)
			_, _ = rand.Read(b)
			admin = hex.EncodeToString(b)
			if err := os.WriteFile(*adminFile, []byte(admin+"\n"), 0o600); err != nil {
				log.Fatal(err)
			}
		}
	}
	s := appstore.New(*dir, store, admin)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(url+"\n"), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("serving %s, %d apps published", url, len(s.Catalog()))
	log.Fatal(http.Serve(ln, s.Routes()))
}
