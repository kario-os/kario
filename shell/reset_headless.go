package shell

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kario-os/kario/antitheft/antitheftest"
	"github.com/kario-os/kario/svc"
)

// The factory reset's and the anti-theft lock's selftest steps
// (headless.go): the account server's kario-antitheft/1 in the Shell's
// process (package antitheftest, on 127.0.0.1) as the product's, at the
// CardDAV server's address when one runs (the Kario account signs in
// there), its key as the one the product ships, and the account's owner
// on the page and in the mailbox, and a previous owner's binding left on
// it (theft-bind-other), as steps. Its state is a file in the run
// directory, which a restart of the phone keeps: the Shell that starts over
// opens the same server again.

type theftSim struct {
	srv                *antitheftest.Server
	ts                 *httptest.Server // its own address, without a CardDAV server
	login              string
	savedURL, savedKey string
	keyFile, file      string
}

func (r *Runner) theftStep(cmd, arg string) error {
	c := r.C
	arg = strings.TrimSpace(arg)
	s := r.theft
	switch cmd {
	case "theft-server":
		if s != nil {
			if s.ts != nil {
				s.ts.Close()
			}
			if a := c.accounts.sim; a != nil {
				a.theft = nil
			}
			c.cfg.AccountURL, c.cfg.FamilyKey, r.theft = s.savedURL, s.savedKey, nil
			if arg == "off" {
				_ = os.Remove(s.file)
				_ = os.Remove(s.keyFile)
				return nil
			}
		}
		f := strings.Fields(arg)
		if len(f) != 3 || f[0] != "on" {
			return errors.New("usage: theft-server on <login> <app password> | off")
		}
		file := filepath.Join(c.cfg.RunDir, "antitheft-sim.json")
		srv, err := antitheftest.Open(file)
		if err != nil {
			return err
		}
		srv.AddAccount(f[1], f[2])
		s = &theftSim{srv: srv, login: f[1], savedURL: c.cfg.AccountURL, savedKey: c.cfg.FamilyKey,
			keyFile: filepath.Join(c.cfg.RunDir, "antitheft-selftest.pub"), file: file}
		if err := os.WriteFile(s.keyFile, []byte(base64.StdEncoding.EncodeToString(srv.Pub)+"\n"), 0o644); err != nil {
			return err
		}
		if a := c.accounts.sim; a != nil {
			a.theft = srv
		} else {
			s.ts = httptest.NewServer(srv)
			c.cfg.AccountURL = s.ts.URL
		}
		c.cfg.FamilyKey, r.theft = s.keyFile, s
		return nil
	case "device-id":
		var csr struct {
			ID  string `json:"device_id"`
			Pub string `json:"device_pub"`
		}
		if err := c.broker.rpc(svc.Keystored, svc.KeyCSR, nil, &csr); err != nil {
			return err
		}
		root, _ := os.ReadFile(filepath.Join(c.cfg.StateDir, "factory", "root.pub"))
		got := csr.ID + " " + csr.Pub + " " + strings.Join(strings.Fields(string(root)), " ")
		file := filepath.Join(c.cfg.RunDir, "selftest-device-id")
		switch arg {
		case "save":
			return os.WriteFile(file, []byte(got), 0o600)
		case "same":
			was, err := os.ReadFile(file)
			if err != nil {
				return errors.New("no device-id save")
			}
			_ = os.Remove(file)
			if string(was) != got {
				return fmt.Errorf("the device's identity changed: %s, was %s", got, was)
			}
			fmt.Fprintf(r.Out, "PHONE-RESET: the device kept its identity %s\n", csr.ID)
			return nil
		}
		return errors.New("usage: device-id save|same")
	case "device-wipe":
		return deviceWipe(r.Out)
	case "state-file":
		rel, want, ok := strings.Cut(arg, " ")
		if !ok || (want != "present" && want != "absent") || strings.Contains(rel, "..") {
			return errors.New("usage: state-file <path under the state dir> present|absent")
		}
		_, err := os.Stat(filepath.Join(c.cfg.StateDir, rel))
		if (err == nil) != (want == "present") {
			return fmt.Errorf("%s is %s", rel, map[bool]string{true: "there", false: "not there"}[err == nil])
		}
		return nil
	}
	if s == nil {
		return errors.New("no theft-server")
	}
	switch cmd {
	case "theft-bound":
		want, ms := lastNum(arg, 8000)
		if want != "yes" && want != "no" {
			return errors.New("usage: theft-bound yes|no [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return (len(s.srv.Devices()) > 0) == (want == "yes") }, ms); !ok {
			return fmt.Errorf("the server's bound phones: %v", s.srv.Devices())
		}
	case "theft-code":
		code := s.srv.LastCode(s.login)
		if code == "" {
			return errors.New("no code was sent")
		}
		if arg == "wrong" {
			code = map[bool]string{true: "111111", false: "000000"}[code == "000000"]
		}
		return r.step("type", code)
	case "theft-remove":
		devs := s.srv.Devices()
		if len(devs) != 1 {
			return fmt.Errorf("bound phones: %v", devs)
		}
		return s.srv.Remove(devs[0])
	case "theft-bind-other":
		if arg == "" || strings.EqualFold(arg, s.login) {
			return errors.New("usage: theft-bind-other <another login>")
		}
		var csr struct {
			ID  string `json:"device_id"`
			Pub string `json:"device_pub"`
		}
		if err := c.broker.rpc(svc.Keystored, svc.KeyCSR, nil, &csr); err != nil {
			return err
		}
		return s.srv.BindTo(csr.ID, csr.Pub, arg)
	}
	return nil
}

// deviceWipe runs the image's data-wipe (yocto's phone-wipe) as
// phone-wipe.service would, on a /var/lib and a persistent area made in a
// temporary directory, and checks what it kept, what it emptied, and that
// the unit comes before sysinit.target and is enabled.
func deviceWipe(out io.Writer) error {
	script := wipeScript
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("the image has no %s: %v", script, err)
	}
	if err := wipeUnit(systemctl("show", "-p", "Before", "-p", "UnitFileState", "phone-wipe.service")); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "device-wipe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	varlib, persist := filepath.Join(tmp, "var-lib"), filepath.Join(tmp, "slots")
	files := map[string]string{"phone/factory/root.pub": "root", "phone/factory/device.cert": "cert", "phone/keystore/device.key": "key",
		"phone/keystore/device-sm2.key": "sm2", "phone/keystore/lock.json": "pin", "phone/shell/contacts.json": "[]", "iwd/Home.psk": "psk",
		"systemd/timesync/clock": "", "slots/wipe/request": "kario-wipe/1 settings 0"}
	for rel, text := range files {
		p := filepath.Join(varlib, rel)
		if strings.HasPrefix(rel, "slots/") {
			p = filepath.Join(persist, strings.TrimPrefix(rel, "slots/"))
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			return err
		}
	}
	if run, err := exec.Command(script, "empty", varlib, persist).CombinedOutput(); err != nil {
		return fmt.Errorf("data-wipe: %v: %s", err, run)
	}
	for rel, want := range map[string]bool{"phone/factory/root.pub": true, "phone/factory/device.cert": true, "phone/keystore/device.key": true,
		"phone/keystore/device-sm2.key": true, "phone/keystore/lock.json": false, "phone/shell/contacts.json": false, "iwd/Home.psk": false,
		"systemd/timesync/clock": true} {
		raw, err := os.ReadFile(filepath.Join(varlib, rel))
		if (err == nil) != want || want && string(raw) != files[rel] {
			return fmt.Errorf("%s after the wipe: %q %v", rel, raw, err)
		}
	}
	if salt, err := os.ReadFile(filepath.Join(varlib, "phone", "keystore", "data.salt")); err != nil || len(salt) != 32 {
		return fmt.Errorf("data salt: %d bytes, %v", len(salt), err)
	}
	if _, err := os.Stat(filepath.Join(persist, "wipe")); err == nil {
		return errors.New("the request outlived the wipe")
	}
	fmt.Fprintln(out, "PHONE-RESET: the image's data-wipe kept the identity and emptied the rest")
	return nil
}

// wipeScript is the image's data-wipe (tests put another in its place).
var wipeScript = "/usr/lib/phone/data-wipe"

// systemctl runs systemctl with args: what it printed, and what it said
// on the side (tests stand in for it).
var systemctl = func(args ...string) (out, said []byte, err error) {
	var e bytes.Buffer
	cmd := exec.Command("systemctl", args...)
	cmd.Stderr = &e
	out, err = cmd.Output()
	return out, e.Bytes(), err
}

// systemdOff says why this system's service manager does not answer, ""
// when it does: systemctl fails where no systemd runs, and prints nothing
// and exits 0 where the systemd running is another root's (it only says
// "Running in chroot, ignoring command").
func systemdOff() string {
	out, said, err := systemctl("show", "-p", "Version")
	return systemctlOff(out, said, err, "Version=")
}

// systemctlOff is why systemctl's answer lacks want, "" when it has it.
func systemctlOff(out, said []byte, err error, want string) string {
	why := strings.TrimSpace(string(said))
	switch {
	case err != nil:
		return strings.TrimSpace(fmt.Sprintf("systemctl: %v %s", err, why))
	case !bytes.Contains(out, []byte(want)):
		return "systemctl answered nothing: " + firstNonEmpty(why, "it printed nothing and said nothing")
	}
	return ""
}

// wipeUnit checks systemctl's show of phone-wipe.service: installed,
// enabled, before sysinit.target.
func wipeUnit(out, said []byte, err error) error {
	if off := systemctlOff(out, said, err, "UnitFileState="); off != "" {
		return errors.New("phone-wipe.service: " + off)
	}
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			props[k] = v
		}
	}
	switch state := props["UnitFileState"]; {
	case state == "":
		return errors.New("phone-wipe.service: systemd has no such unit")
	case state != "enabled":
		return fmt.Errorf("phone-wipe.service is %s, not enabled", state)
	case !slices.Contains(strings.Fields(props["Before"]), "sysinit.target"):
		return fmt.Errorf("phone-wipe.service does not come before sysinit.target (Before=%s)", props["Before"])
	}
	return nil
}
