// phone-ota-verify checks install reports (ota.Report, one JSON a line, as
// phones POST them to /report) against the root the device certificates
// must chain to: each must be signed by a device for exactly what it says.
//
//	phone-ota-verify --root root.pub reports.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ota"
)

func main() {
	rootPath := flag.String("root", "", "the root public key the device certificates must chain to")
	flag.Parse()
	if *rootPath == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: phone-ota-verify --root root.pub reports.jsonl")
		os.Exit(2)
	}
	roots, err := keystore.LoadRootPub(*rootPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer f.Close()
	n, bad := 0, 0
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var r ota.Report
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			fmt.Printf("OTA-VERIFY: line %d is not a report: %v\n", n+1, err)
			bad++
		} else if err := ota.VerifyReport(r, roots); err != nil {
			fmt.Printf("OTA-VERIFY: line %d (%s %s): %v\n", n+1, r.Version, r.Outcome, err)
			bad++
		}
		n++
	}
	if n == 0 || bad > 0 {
		fmt.Printf("OTA-VERIFY: FAIL, %d of %d reports\n", bad, n)
		os.Exit(1)
	}
	fmt.Printf("OTA-VERIFY: PASS, %d reports signed by the device\n", n)
}
