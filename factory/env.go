package factory

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// Env is where the automatic checks look: the kernel's files under Sys, Dev
// and Proc, the services' sockets in RunDir, and the helpers they run
// (alsaucm, arecord). The tests and the selftest point it at a tree of
// their own (Fake). It does not use hal's roots: the Shell, which runs the
// selftest's test in its own process, reads its hardware through them.
type Env struct {
	Sys, Dev, Proc string
	RunDir         string
	// Run runs a helper; nil runs the program.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Sleep waits (the RTC's second reading); nil is time.Sleep.
	Sleep func(time.Duration)
	// ModemWait is how long the modem may take to show up in telephonyd
	// after the boot (ModemManager probes it some seconds after).
	ModemWait time.Duration
}

// System is the phone's own: /sys, /dev, /proc and /run/phone.
func System() *Env {
	return &Env{Sys: "/sys", Dev: "/dev", Proc: "/proc", RunDir: "/run/phone", ModemWait: 90 * time.Second}
}

func (e *Env) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if e.Run != nil {
		return e.Run(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var x *exec.ExitError
	if errors.As(err, &x) && len(x.Stderr) > 0 {
		err = fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(x.Stderr)))
	}
	return out, err
}

func (e *Env) sleep(d time.Duration) {
	if e.Sleep != nil {
		e.Sleep(d)
		return
	}
	time.Sleep(d)
}

func read(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func glob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	sort.Strings(m)
	return m
}

// bitmap is an input device's capability bits as sysfs prints them: hex
// longs, the most significant first.
type bitmap []uint64

func parseBitmap(s string) bitmap {
	f := strings.Fields(s)
	b := make(bitmap, len(f))
	for i, w := range f {
		v, _ := strconv.ParseUint(w, 16, 64)
		b[len(f)-1-i] = v
	}
	return b
}

func (b bitmap) has(n int) bool {
	w := n / bits.UintSize
	return w < len(b) && b[w]&(1<<(n%bits.UintSize)) != 0
}

// inputDev is an evdev device's name and capabilities (class/input).
type inputDev struct {
	node                    string
	name                    string
	key, sw, abs, ff, props bitmap
}

func (e *Env) inputs() []inputDev {
	var out []inputDev
	for _, ev := range glob(filepath.Join(e.Sys, "class/input/event*")) {
		d := filepath.Join(ev, "device")
		name, err := read(filepath.Join(d, "name"))
		if err != nil {
			continue
		}
		capOf := func(f string) bitmap {
			s, _ := read(filepath.Join(d, f))
			return parseBitmap(s)
		}
		out = append(out, inputDev{node: filepath.Base(ev), name: name, key: capOf("capabilities/key"), sw: capOf("capabilities/sw"),
			abs: capOf("capabilities/abs"), ff: capOf("capabilities/ff"), props: capOf("properties")})
	}
	return out
}

// Input codes the checks look for (linux/input-event-codes.h).
const (
	keyEnter       = 28
	keyA           = 30
	key1           = 2
	swLid          = 0x00
	swMuteDevice   = 0x0e
	absX           = 0x00
	absSndProfile  = 0x24
	absMTPositionX = 0x35
	ffRumble       = 0x50
	ffPeriodic     = 0x51
	propDirect     = 0x01
)

// keystoreCSR is keystored's answer to csr.
type keystoreCSR struct {
	DeviceID    string `json:"device_id"`
	Provisioned bool   `json:"provisioned"`
}

// keystoreWait: keystored starts on its socket's first connection and
// opens the keystore then; slower than that it is taken for gone.
const keystoreWait = 30 * time.Second

func (e *Env) keystore() (keystoreCSR, error) {
	var csr keystoreCSR
	cl, err := wire.Dial(filepath.Join(e.RunDir, svc.KeystoredSock))
	if err != nil {
		return csr, fmt.Errorf("keystored: %w", err)
	}
	defer cl.Close()
	if err := cl.CallTimeout(svc.KeyCSR, nil, &csr, keystoreWait); err != nil {
		return csr, fmt.Errorf("keystored: %w", err)
	}
	if csr.DeviceID == "" {
		return csr, errors.New("keystored: no device id")
	}
	return csr, nil
}

// telStatus is what the checks read of telephonyd's status.
type telStatus struct {
	Backend string `json:"backend"`
	Sim     string `json:"sim"`
	IMEI    string `json:"imei"`
}

// telephony asks telephonyd for its status until the modem shows (its
// IMEI known) or ModemWait is over: the test talks to telephonyd, which
// talks to ModemManager, never to the modem itself.
func (e *Env) telephony(ctx context.Context) (telStatus, error) {
	var st telStatus
	end := time.Now().Add(e.ModemWait)
	for {
		cl, err := wire.Dial(filepath.Join(e.RunDir, svc.TelephonydSock))
		if err == nil {
			st = telStatus{}
			err = cl.CallTimeout(svc.TelStatus, nil, &st, 10*time.Second)
			cl.Close()
		}
		if err == nil && st.IMEI != "" || time.Now().After(end) || ctx.Err() != nil {
			if err != nil {
				return st, fmt.Errorf("telephonyd: %w", err)
			}
			return st, nil
		}
		e.sleep(2 * time.Second)
	}
}
