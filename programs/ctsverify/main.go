// phone-cts-verify checks a compatibility report (phone-shell --cts): the
// device attestation must chain to the given root and cover exactly this
// report, and the report must say PASS. The reports of a run in parts
// (phone-shell --parts) are checked each on its own and then together:
// every part in one of them, and every capability exercised in one.
package ctsverify

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/keystore"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	rootPath := flag.String("root", "", "the root public key the device certificates must chain to")
	beside := flag.Bool("root-beside", false, "each report's root is <report>.root, the one its device made and printed (the prototype's devices, which have no provisioned root)")
	flag.Parse()
	if (*rootPath == "") == !*beside || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: phone-cts-verify --root root.pub | --root-beside report.json...")
		os.Exit(2)
	}
	var reports []cts.CompatReport
	for _, path := range flag.Args() {
		rp := *rootPath
		if *beside {
			rp = path + ".root"
		}
		root, err := keystore.LoadRootPub(rp)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		r, err := cts.VerifyReport(raw, root)
		if err != nil {
			fmt.Printf("CTS-VERIFY: FAIL, %s: %v\n", path, err)
			os.Exit(1)
		}
		what, result := "", r.Result
		if len(r.Parts) > 0 {
			what, result = "part "+strings.Join(r.Parts, ",")+": ", r.Result+" (its cases "+r.PartResult+")"
		}
		fmt.Printf("CTS-VERIFY: %ssignature OK (device %s, %s %s, %s); result %s, %d cases, capabilities %s\n",
			what, r.Attestation.DeviceID, r.System, r.Version, r.Date, result, len(r.Cases), r.Coverage)
		reports = append(reports, r)
	}
	r, err := cts.MergeParts(reports)
	if err != nil {
		fmt.Println("CTS-VERIFY: FAIL,", err)
		os.Exit(1)
	}
	if len(reports) > 1 {
		fmt.Printf("CTS-VERIFY: the %d reports together, parts %s: result %s, %d cases, capabilities %s\n",
			len(reports), strings.Join(r.Parts, ","), r.Result, len(r.Cases), r.Coverage)
	}
	for _, name := range slices.Sorted(maps.Keys(r.Skipped)) {
		fmt.Printf("CTS-VERIFY: capability %s not called, its cases skipped: %s\n", name, r.Skipped[name])
	}
	if len(r.Parts) > 0 && len(r.Uncovered) > 0 {
		fmt.Printf("CTS-VERIFY: never called in any part: %s\n", strings.Join(r.Uncovered, " "))
	}
	if s := r.Sandbox; s != nil {
		fmt.Printf("CTS-VERIFY: kernel %s, %d protections missing\n", s.Kernel, len(s.Missing))
		if s.Foreign != "" {
			fmt.Printf("CTS-VERIFY: the kernel is %s: what it lacks is that system's, not the device's\n", s.Foreign)
		}
		for _, name := range slices.Sorted(maps.Keys(s.Missing)) {
			fmt.Printf("CTS-VERIFY: missing %s: %s\n", name, s.Missing[name])
		}
	}
	if r.Result != "PASS" {
		os.Exit(1)
	}
}
