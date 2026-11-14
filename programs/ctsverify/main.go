// phone-cts-verify checks a compatibility report (phone-shell --cts): the
// device attestation must chain to the given root and cover exactly this
// report, and the report must say PASS.
package ctsverify

import (
	"flag"
	"fmt"
	"os"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/keystore"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	rootPath := flag.String("root", "", "the root public key the device certificate must chain to")
	flag.Parse()
	if *rootPath == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: phone-cts-verify --root root.pub report.json")
		os.Exit(2)
	}
	root, err := keystore.LoadRootPub(*rootPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	raw, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	r, err := cts.VerifyReport(raw, root)
	if err != nil {
		fmt.Println("CTS-VERIFY: FAIL,", err)
		os.Exit(1)
	}
	fmt.Printf("CTS-VERIFY: signature OK (device %s, %s %s, %s); result %s, %d cases, capabilities %s\n",
		r.Attestation.DeviceID, r.System, r.Version, r.Date, r.Result, len(r.Cases), r.Coverage)
	if r.Result != "PASS" {
		os.Exit(1)
	}
}
