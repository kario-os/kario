// Package devcmd is phone-dev, the phone's side of kario device
// (package devctl): run in an SSH session over the trusted cable, it hands
// its request to the Shell, which decides. It reads the package to install
// from a file or its standard input and says which SSH connection it runs
// in; it has no rights of its own beyond reaching the Shell's socket.
//
//	phone-dev install FILE|-
//	phone-dev trust KEY
//	phone-dev logs [-f] APP
//	phone-dev reload|restart|uninstall APP
//	phone-dev debug [-attach] APP   the debugger socket on standard input and output
//	phone-dev push PATH SIZE [MODE]   SIZE bytes from standard input
//	phone-dev pull PATH          the file on standard output
//	phone-dev screenshot         a PNG on standard output
//	phone-dev key NAME...
//	phone-dev type TEXT
//	phone-dev syslog [-f] [-u UNIT] [-n N]
//	phone-dev forward [-reverse] PORT   held until the gate closes or standard input ends
//	phone-dev reboot [bootloader|recovery]
//	phone-dev update SIZE        SIZE bytes of a bundle from standard input
//	phone-dev bugreport          a zip on standard output
package devcmd

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/pkg"
)

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	if err := Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, devctl.Program+":", err)
		os.Exit(1)
	}
}

func usage() error {
	return fmt.Errorf("usage: %s install FILE|- | trust KEY | logs [-f] APP | reload APP | restart APP | uninstall APP | debug [-attach] APP"+
		" | push PATH SIZE [MODE] | pull PATH | screenshot | key NAME... | type TEXT | syslog [-f] [-u UNIT] [-n N]"+
		" | forward [-reverse] PORT | reboot [bootloader|recovery] | update SIZE | bugreport", devctl.Program)
}

// Run is one request, the Shell's socket found through devctl.SockPath;
// notes get what the Shell says beside bytes on out.
func Run(args []string, stdin io.Reader, out, notes io.Writer) error {
	if len(args) == 0 {
		return usage()
	}
	req := devctl.Request{Op: args[0], Via: os.Getenv(devctl.SSHEnv)}
	rest := args[1:]
	var body io.Reader
	var err error
	switch req.Op {
	case devctl.OpInstall:
		if len(rest) != 1 {
			return usage()
		}
		raw, err := readPackage(rest[0], stdin)
		if err != nil {
			return err
		}
		req.Size, body = int64(len(raw)), bytes.NewReader(raw)
	case devctl.OpTrust:
		if len(rest) != 1 {
			return usage()
		}
		req.Key = strings.TrimSpace(rest[0])
	case devctl.OpLogs:
		if len(rest) == 2 && rest[0] == "-f" {
			req.Follow, rest = true, rest[1:]
		}
		if len(rest) != 1 {
			return usage()
		}
		req.App = rest[0]
	case devctl.OpDebug:
		if len(rest) == 2 && rest[0] == "-attach" {
			req.Attach, rest = true, rest[1:]
		}
		if len(rest) != 1 {
			return usage()
		}
		req.App, body = rest[0], stdin
	case devctl.OpReload, devctl.OpRestart, devctl.OpUninstall:
		if len(rest) != 1 {
			return usage()
		}
		req.App = rest[0]
	case devctl.OpPush:
		if len(rest) != 2 && len(rest) != 3 {
			return usage()
		}
		req.Path = rest[0]
		if req.Size, err = strconv.ParseInt(rest[1], 10, 64); err != nil || req.Size < 0 {
			return fmt.Errorf("push: %q is not a size", rest[1])
		}
		if len(rest) == 3 {
			mode, err := strconv.ParseUint(rest[2], 8, 32)
			if err != nil || mode > 0o777 {
				return fmt.Errorf("push: %q is not a mode (octal, at most 777)", rest[2])
			}
			req.Mode = uint32(mode)
		}
		body = stdin
	case devctl.OpPull:
		if len(rest) != 1 {
			return usage()
		}
		req.Path = rest[0]
	case devctl.OpScreenshot, devctl.OpBugreport:
		if len(rest) != 0 {
			return usage()
		}
	case devctl.OpKey:
		req.Keys = rest
	case devctl.OpType:
		req.Text = strings.Join(rest, " ")
	case devctl.OpSyslog:
		fs := flag.NewFlagSet("syslog", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.BoolVar(&req.Follow, "f", false, "")
		fs.StringVar(&req.Unit, "u", "", "")
		fs.IntVar(&req.Lines, "n", 0, "")
		if fs.Parse(rest) != nil || fs.NArg() != 0 {
			return usage()
		}
	case devctl.OpForward:
		if len(rest) == 2 && rest[0] == "-reverse" {
			req.Reverse, rest = true, rest[1:]
		}
		if len(rest) != 1 {
			return usage()
		}
		if req.Port, err = strconv.Atoi(rest[0]); err != nil {
			return fmt.Errorf("forward: %q is not a port", rest[0])
		}
		body = stdin
	case devctl.OpReboot:
		if len(rest) > 1 {
			return usage()
		}
		if len(rest) == 1 {
			req.Target = rest[0]
		}
	case devctl.OpUpdate:
		if len(rest) != 1 {
			return usage()
		}
		if req.Size, err = strconv.ParseInt(rest[0], 10, 64); err != nil || req.Size <= 0 {
			return fmt.Errorf("update: %q is not a size", rest[0])
		}
		body = stdin
	default:
		return usage()
	}
	return devctl.Call(devctl.SockPath(""), req, body, out, notes)
}

func readPackage(name string, stdin io.Reader) ([]byte, error) {
	r := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, pkg.MaxSize+1))
	switch {
	case err != nil:
		return nil, err
	case len(raw) == 0:
		return nil, fmt.Errorf("%s: an empty package", name)
	case len(raw) > pkg.MaxSize:
		return nil, fmt.Errorf("%s: larger than a package may be (%d MB)", name, pkg.MaxSize>>20)
	}
	return raw, nil
}
