// phone-factory-test is the line's hardware test (docs/40-testing.md §10).
//
//	phone-factory-test [--auto] [--out FILE] [--sku FILE]     on the phone: the report on stdout and in FILE
//	phone-factory-test --unit ADDR [--auto] [--wait DUR] [--device ID] [--out FILE]
//	                                                          the station: the same, of the unit at ADDR over USB
//	phone-factory-test --unit ADDR --provision CA_DIR         provision the unit with a CA at hand (the station's flow)
//	phone-factory-test --gate                                 phone-factory-gate.service: factory mode's mark, if available
//	phone-factory-test --serve [--listen ADDR]                phone-factory.service: factory mode's runner
//	phone-factory-test --fake DIR ...                         a unit made up in DIR (a developer's machine)
//
// The exit code is the report's (factory.Exit*): 0 every required check
// passed, 1 one did not, 2 the test could not run, 3 factory mode is not
// available (the keystore is provisioned).
package factorytest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/factory"
	"github.com/kario-os/kario/keystore"
)

const defaultOut = "/run/phone/factory/report.json"

// Main is the program: kario-system runs it by the name it was called by.
func Main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("phone-factory-test", flag.ContinueOnError)
	auto := fs.Bool("auto", false, "only the automatic checks")
	out := fs.String("out", "", "also write the report here (default "+defaultOut+" on the phone, factory-report-<device id>.json at the station)")
	skuPath := fs.String("sku", factory.SKUPath, "the SKU (sku.json)")
	unit := fs.String("unit", "", `the unit's runner: "10.71.0.1:7171" over USB, "unix:PATH" on the phone`)
	wait := fs.Duration("wait", 0, "with --unit and not --auto: how long to wait for the operator to finish the factory screen")
	device := fs.String("device", os.Getenv("DEVICE_ID"), "with --unit: the unit's device id, which the report must have (kario-station sets DEVICE_ID)")
	caDir := fs.String("provision", "", "with --unit: provision the unit with the CA in this directory")
	gate := fs.Bool("gate", false, "write factory mode's mark if the keystore is not provisioned (exit 3 if it is)")
	mark := fs.String("mark", factory.MarkPath, "factory mode's mark")
	serve := fs.Bool("serve", false, "be factory mode's runner")
	listen := fs.String("listen", factory.USBAddr, `with --serve: the USB link's address ("" none)`)
	runDir := fs.String("run-dir", "/run/phone", "the services' sockets")
	fake := fs.String("fake", "", "a made-up unit in this directory, every device of the SKU present (a developer's machine)")
	if err := fs.Parse(args); err != nil {
		return factory.ExitError
	}
	log.SetPrefix("phone-factory-test: ")
	log.SetFlags(0)
	mode := factory.ModeFull
	if *auto {
		mode = factory.ModeAuto
	}
	switch {
	case *unit != "" && *caDir != "":
		return provision(stdout, *unit, *caDir)
	case *unit != "":
		return station(stdout, *unit, mode, *wait, *device, *out)
	}
	sku, err := factory.LoadSKU(*skuPath)
	if err != nil {
		log.Print(err)
		return factory.ExitError
	}
	env := factory.System()
	env.RunDir = *runDir
	if *fake != "" {
		f, err := factory.NewFake(*fake, sku, factory.FakeOptions{Keystore: true})
		if err != nil {
			log.Print(err)
			return factory.ExitError
		}
		defer f.Close()
		env = f.Env
	}
	switch {
	case *gate:
		id, err := env.Enter(*mark)
		if err != nil {
			log.Printf("normal boot: %v", err)
			return factory.ExitRefused
		}
		log.Printf("factory mode: %s, keystore not provisioned (%s)", id, *mark)
		return factory.ExitPass
	case *serve:
		return serveUnit(env, sku, *runDir, *listen)
	}
	if *out == "" {
		*out = defaultOut
	}
	sock := filepath.Join(*runDir, factory.Sock)
	if _, err := os.Stat(sock); err == nil && *fake == "" {
		return station(stdout, "unix:"+sock, mode, *wait, "", *out)
	}
	rep := factory.NewServer(env, sku, "").Run(context.Background(), mode, 0)
	return emit(stdout, rep, *out)
}

// emit prints the report and writes it to out; the exit code is the
// report's.
func emit(stdout io.Writer, rep *factory.Report, out string) int {
	raw, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Fprintln(stdout, string(raw))
	if out != "" {
		if err := rep.Write(out); err != nil {
			log.Print(err)
			return factory.ExitError
		}
	}
	for _, c := range rep.Checks {
		if c.Result != factory.Pass {
			log.Printf("%s %s: %s", c.ID, c.Result, c.Reason)
		}
	}
	if rep.Refused != "" {
		log.Print(rep.Refused)
	}
	return rep.ExitCode()
}

// station tests the unit behind addr: the report on stdout and in out.
func station(stdout io.Writer, addr, mode string, wait time.Duration, device, out string) int {
	u := factory.Dial(addr)
	rep, err := u.Test(mode, wait)
	if err != nil {
		log.Printf("%s: %v", addr, err)
		return factory.ExitError
	}
	if device != "" && rep.DeviceID != device && rep.Refused == "" {
		log.Printf("%s is unit %s, not %s (DEVICE_ID)", addr, rep.DeviceID, device)
		return factory.ExitError
	}
	if out == "" {
		out = "factory-report.json"
		if rep.DeviceID != "" {
			out = "factory-report-" + rep.DeviceID + ".json"
		}
	}
	return emit(stdout, rep, out)
}

// provision certifies the unit with the CA in dir over the runner's API.
func provision(stdout io.Writer, addr, dir string) int {
	ca, err := keystore.LoadCA(dir)
	if err != nil {
		log.Print(err)
		return factory.ExitError
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	id, err := factory.Provision(factory.Dial(addr), ca, hex.EncodeToString(b[:]))
	if errors.Is(err, factory.ErrRefused) {
		log.Print(err)
		return factory.ExitRefused
	}
	if err != nil {
		log.Print(err)
		return factory.ExitError
	}
	fmt.Fprintf(stdout, "provisioned %s under root %s\n", id, ca.Root)
	return factory.ExitPass
}

// serveUnit is factory mode's runner: the factory screen's socket, and the
// station's port on the USB link once it has its address.
func serveUnit(env *factory.Env, sku factory.SKU, runDir, listen string) int {
	id, err := env.Gate()
	if err != nil {
		log.Printf("not serving: %v", err)
		return factory.ExitRefused
	}
	srv := factory.NewServer(env, sku, id)
	ln, err := factory.ListenSock(filepath.Join(runDir, factory.Sock))
	if err != nil {
		log.Print(err)
		return factory.ExitError
	}
	if listen != "" {
		go func() {
			tcp, err := factory.ListenUSB(context.Background(), listen)
			if err != nil {
				log.Print(err)
				return
			}
			log.Printf("the station's API on %s", listen)
			log.Print(factory.Serve(tcp, srv.Station()))
		}()
	}
	log.Printf("unit %s (%s), %d checks; the factory screen's API on %s", id, sku.ID, len(factory.Plan(sku)), ln.Addr())
	log.Print(factory.Serve(ln, srv.Local()))
	return factory.ExitError
}
