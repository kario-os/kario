// kario-system is the system services in one program (R45, like k3s or
// busybox): the Shell, the services, the sandbox launcher, the USB sharing
// helper and phone-cts-verify (the table below) share one copy of the Go
// runtime and of the packages they all link, so the image carries one
// program instead of one per service and an update ships it once. The image links each service's name to
// it (bin/phone-shell → kario-system); it runs the one it is called by, or
// the one named first (kario-system phone-pushd ...). Apps stay programs of
// their own: they are packages, installed and verified one by one.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kario-os/kario/programs/audiod"
	"github.com/kario-os/kario/programs/ctsverify"
	"github.com/kario-os/kario/programs/imed"
	"github.com/kario-os/kario/programs/keystored"
	"github.com/kario-os/kario/programs/mediad"
	"github.com/kario-os/kario/programs/nfcd"
	"github.com/kario-os/kario/programs/pushd"
	"github.com/kario-os/kario/programs/sandboxcmd"
	"github.com/kario-os/kario/programs/shellcmd"
	"github.com/kario-os/kario/programs/smsd"
	"github.com/kario-os/kario/programs/usbshared"
)

var programs = map[string]func(){
	"phone-shell":      shellcmd.Main,
	"phone-pushd":      pushd.Main,
	"phone-keystored":  keystored.Main,
	"phone-imed":       imed.Main,
	"phone-audiod":     audiod.Main,
	"phone-mediad":     mediad.Main,
	"phone-nfcd":       nfcd.Main,
	"phone-smsd":       smsd.Main,
	"phone-sandbox":    sandboxcmd.Main,
	"phone-cts-verify": ctsverify.Main,
	"phone-usbshared":  usbshared.Main,
}

func main() {
	if run, ok := programs[filepath.Base(os.Args[0])]; ok {
		run()
		return
	}
	if len(os.Args) > 1 {
		if run, ok := programs[os.Args[1]]; ok {
			os.Args = os.Args[1:]
			run()
			return
		}
	}
	var names []string
	for name := range programs {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stderr, "kario-system runs one of: %v (called by that name, or named first)\n", names)
	os.Exit(2)
}
