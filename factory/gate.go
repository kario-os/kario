package factory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CmdlineFlag on the kernel command line asks for factory mode: whoever
// sets it holds the phone (fastboot over USB with the bootloader open, or
// the line's boot image), which is the physical presence factory mode
// needs (docs/31-lock-and-biometrics.md §13.4).
const CmdlineFlag = "kario.factory=1"

// MarkPath is factory mode's mark: phone-factory-gate.service writes it on
// a boot that asked for factory mode while the keystore was not
// provisioned. It is on /run, so no boot after keeps it.
const MarkPath = "/run/phone/factory/on"

// ErrProvisioned refuses factory mode on a provisioned phone.
var ErrProvisioned = errors.New("the keystore is provisioned: factory mode is not available")

// Requested: the command line asks for factory mode (its last
// kario.factory= word, as the kernel takes a repeated parameter).
func Requested(cmdline string) bool {
	on := false
	for _, w := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(w, "kario.factory="); ok {
			on = v == "1"
		}
	}
	return on
}

// RequestedNow: this boot's command line asks for factory mode.
func RequestedNow() bool {
	b, err := os.ReadFile("/proc/cmdline")
	return err == nil && Requested(string(b))
}

// Gate asks the keystore whether factory mode is available: only while it
// is not provisioned. No answer within keystoreWait is a refusal too.
// The device id comes back either way when the keystore says it.
func (e *Env) Gate() (string, error) {
	csr, err := e.keystore()
	if err != nil {
		return "", fmt.Errorf("factory mode is not available: %w", err)
	}
	if csr.Provisioned {
		return csr.DeviceID, ErrProvisioned
	}
	return csr.DeviceID, nil
}

// Enter writes the mark when the gate allows factory mode, and clears it
// when not: phone-factory-gate.service's whole job.
func (e *Env) Enter(mark string) (string, error) {
	id, err := e.Gate()
	if err != nil {
		os.Remove(mark)
		return id, err
	}
	if err := os.MkdirAll(filepath.Dir(mark), 0o755); err != nil {
		return id, err
	}
	return id, os.WriteFile(mark, []byte(id+"\n"), 0o644)
}

// Active: this boot is in factory mode (the gate wrote the mark).
func Active(mark string) bool {
	_, err := os.Stat(mark)
	return err == nil
}
