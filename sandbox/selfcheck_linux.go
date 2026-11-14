package sandbox

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"golang.org/x/sys/unix"
)

// Check is one thing a malicious app would try, its Name in the device's
// language. Needs names the layer (ParseApplied) that has to be in force for
// the check to mean anything; without it the check is skipped, not failed.
type Check struct {
	Name       string
	WantDenied bool
	Needs      string
	Try        func() error
}

// SelfChecks are run from inside an app's sandbox: by the settings app (its
// sandbox self-check) and by the compatibility probe, so a device maker
// cannot weaken the sandbox and still pass the CTS.
func SelfChecks(dataDir string) []Check {
	state := filepath.Dir(filepath.Dir(os.Getenv(ui.EnvTrustRoot)))
	runDirs := []string{filepath.Join(filepath.Dir(state), "run"), "/run/phone"}
	return []Check{
		{tr.T("Read the device private key"), true, "uid", func() error {
			_, err := os.ReadFile(filepath.Join(state, "keystore", "device.key"))
			return err
		}},
		{tr.T("Read another app's data"), true, "uid", func() error {
			_, err := os.ReadDir(filepath.Join(state, "apps", "com.phone.wallet"))
			return err
		}},
		{tr.T("Connect to the key service directly"), true, "uid", func() error {
			var last error = os.ErrNotExist
			for _, d := range runDirs {
				c, err := net.DialTimeout("unix", filepath.Join(d, svc.KeystoredSock), time.Second)
				if err == nil {
					c.Close()
					return nil
				}
				if !os.IsNotExist(err) {
					last = err
				}
			}
			return last
		}},
		{tr.T("Run code it wrote itself"), true, "landlock", func() error {
			script := filepath.Join(dataDir, "downloaded.sh")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				return nil
			}
			return exec.Command(script).Run()
		}},
		{tr.T("Write to the shared temporary directories /tmp and /var/tmp"), true, "landlock", func() error {
			for _, d := range []string{"/tmp", "/var/tmp"} {
				p := filepath.Join(d, "probe-"+strconv.Itoa(os.Getpid()))
				if err := os.WriteFile(p, []byte("x"), 0o600); err == nil {
					os.Remove(p)
					return nil
				}
			}
			return errors.New("denied")
		}},
		{tr.T("See other processes"), true, "ns", func() error {
			entries, err := os.ReadDir("/proc")
			if err != nil {
				return err
			}
			self := strconv.Itoa(os.Getpid())
			for _, e := range entries {
				if _, err := strconv.Atoi(e.Name()); err == nil && e.Name() != self {
					return nil
				}
			}
			return errors.New("only own process visible")
		}},
		{tr.T("Reach the network"), true, "nonet", func() error {
			ifs, err := net.Interfaces()
			if err != nil {
				return err
			}
			for _, i := range ifs {
				if i.Flags&net.FlagUp != 0 && i.Flags&net.FlagLoopback == 0 {
					return nil
				}
			}
			return errors.New("no network interface")
		}},
		{tr.T("Listen on a TCP port"), true, "nonet", func() error {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err == nil {
				l.Close()
			}
			return err
		}},
		{tr.T("Make dangerous system calls (such as keyctl)"), true, "seccomp", func() error {
			_, err := unix.KeyctlInt(unix.KEYCTL_GET_KEYRING_ID, unix.KEY_SPEC_SESSION_KEYRING, 0, 0, 0)
			return err
		}},
		{tr.T("Write to its own data directory"), false, "", func() error {
			return os.WriteFile(filepath.Join(dataDir, "probe"), []byte("ok"), 0o600)
		}},
		{tr.T("Read the vendor root public key"), false, "", func() error {
			_, err := os.ReadFile(os.Getenv(ui.EnvTrustRoot))
			return err
		}},
		{tr.T("Open the display device /dev/dri"), true, "uid", func() error {
			f, err := os.OpenFile("/dev/dri/card0", os.O_RDWR, 0)
			if err == nil {
				f.Close()
			}
			return err
		}},
		{tr.T("Read the keyboard device /dev/input"), true, "uid", func() error {
			evs, _ := filepath.Glob("/dev/input/event*")
			for _, e := range evs {
				if f, err := os.Open(e); err == nil {
					f.Close()
					return nil
				}
			}
			return errors.New("denied")
		}},
	}
}
