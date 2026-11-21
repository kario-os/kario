// phone-provision is the factory station's side of device provisioning
// (59-business-model.md §2, the certification plan): the certifier's CA
// signs each device's certificate, so a relying party (a payment server, the
// compatibility report's verifier) trusts the CA's root instead of anything
// the device says about itself.
//
//	phone-provision ca --dir DIR                   make the CA (ca.key, root.pub) if missing
//	phone-provision device --dir DIR --keystore SOCK   provision the device whose keystored listens on SOCK
//
// In production the CA key sits in an offline HSM and the station talks to
// the device over USB; the device's key stays in its keystore (the TEE).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kario-os/kario/keystore"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: phone-provision ca|device --dir DIR [--keystore SOCK]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("dir", "", "the CA's directory (ca.key, root.pub)")
	sock := fs.String("keystore", "/run/phone/keystored.sock", "the device's keystored socket")
	_ = fs.Parse(os.Args[2:])
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "phone-provision: --dir is required")
		os.Exit(2)
	}
	ca, err := keystore.LoadCA(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "phone-provision:", err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "ca":
		fmt.Println("CA root:", ca.Root)
	case "device":
		id, err := keystore.Provision(ca, *sock)
		if err != nil {
			fmt.Fprintln(os.Stderr, "phone-provision:", err)
			os.Exit(1)
		}
		fmt.Printf("provisioned %s under root %s\n", id, ca.Root)
	default:
		fmt.Fprintln(os.Stderr, "phone-provision: unknown command", os.Args[1])
		os.Exit(2)
	}
}
