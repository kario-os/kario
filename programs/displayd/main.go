// phone-displayd owns the panel's refresh rate (08-display-adaptive-
// refresh.md): it alone chooses it, by the policy (policy.go) and what the
// Shell passes on, and says so to the Shell, whose renderer switches the
// panel to the mode of that rate.
//
//	phone-displayd --run-dir DIR [--panel auto|sim[:60,120]|kms[:/dev/dri/card0]]
//	               [--thermal-zones <type>,... --thermal-cap <degrees>]
package displayd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// thermalEvery: how often the thermal zones are read.
const thermalEvery = 10 * time.Second

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	runDir := flag.String("run-dir", "/run/phone", "directory for service sockets")
	panelSpec := flag.String("panel", "auto", `the panel: "sim" (60 and 120 Hz), "sim:<rate>,..." (simulated with those rates), "kms[:<card>]" (the phone's, through DRM/KMS), "auto" (kms where there is a DRM card, else sim)`)
	zones := flag.String("thermal-zones", "", "the thermal zones (types under /sys/class/thermal) that cap the rate when hot; none: no cap but the simulated one")
	capC := flag.Float64("thermal-cap", 45, "the temperature (degrees C) of those zones from which the rate is capped at the standard rate")
	flag.Parse()
	log.SetPrefix("phone-displayd: ")
	if err := os.MkdirAll(*runDir, 0o755); err != nil {
		log.Fatal(err)
	}
	panel, err := openPanel(*panelSpec)
	if err != nil {
		log.Fatal(err)
	}
	d := newDaemon(panel, newThermal("/sys/class/thermal", *zones, *capC), os.Getenv("PHONE_DISPLAY_TRACE") != "")
	hub, err := wire.ServeHub(filepath.Join(*runDir, svc.DisplaydSock), d.handle)
	if err != nil {
		log.Fatal(err)
	}
	d.mu.Lock()
	d.hub = hub
	d.mu.Unlock()
	log.Printf("panel %s, rates %v", firstNonEmpty(panel.Name(), *panelSpec), panel.Rates())
	go func() {
		for range time.Tick(thermalEvery) {
			d.checkThermal()
		}
	}()
	d.run()
}

type daemon struct {
	mu      sync.Mutex
	panel   Panel
	policy  *Policy
	thermal *thermal
	hub     *wire.Hub
	trace   bool
	want    int
	reason  string
	hz      int
	// switches counts the rates chosen, seq the states told; wake tells
	// run to decide again.
	switches int
	seq      uint64
	wake     chan struct{}
	now      func() time.Time
}

func newDaemon(p Panel, t *thermal, trace bool) *daemon {
	return &daemon{panel: p, policy: NewPolicy(p.Rates()), thermal: t, trace: trace, wake: make(chan struct{}, 1), now: time.Now, hz: p.Hz()}
}

// run decides whenever something changed or a hint ran out.
func (d *daemon) run() {
	for {
		d.mu.Lock()
		next := d.decide()
		d.mu.Unlock()
		var due <-chan time.Time
		if !next.IsZero() {
			due = time.After(time.Until(next))
		}
		select {
		case <-d.wake:
		case <-due:
		}
	}
}

func (d *daemon) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// decide applies the policy now; with d.mu held.
func (d *daemon) decide() time.Time {
	hz, reason, next := d.policy.Decide(d.now())
	if hz == 0 {
		return next
	}
	changed := hz != d.want
	if changed {
		d.want, d.switches = hz, d.switches+1
		d.panel.Want(hz)
	}
	if changed || reason != d.reason || d.panel.Hz() != d.hz {
		d.reason, d.hz, d.seq = reason, d.panel.Hz(), d.seq+1
		if d.trace {
			log.Printf("trace: %s want %d Hz (%s), panel at %d Hz", d.now().Format("15:04:05.000"), d.want, d.reason, d.hz)
		}
		if d.hub != nil {
			d.hub.Broadcast(svc.DisplayEvent{Event: svc.DisplayEventRate, DisplayStatus: d.state()})
		}
	}
	return next
}

func (d *daemon) state() svc.DisplayStatus {
	return svc.DisplayStatus{Seq: d.seq, Panel: d.panel.Name(), Rates: d.panel.Rates(), Hz: d.panel.Hz(), Want: d.want, Reason: d.reason,
		Setting: d.policy.Setting(), Caps: d.policy.Caps(), Switches: d.switches}
}

func (d *daemon) checkThermal() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.thermal.check() {
		d.thermalCap()
	}
}

// thermalCap: with d.mu held.
func (d *daemon) thermalCap() {
	hz := 0
	if d.thermal.hot {
		hz = d.policy.standard()
	}
	d.policy.SetCap(svc.DisplayCapThermal, hz)
	log.Printf("thermal: hot %v; capped at %d Hz (0: none)", d.thermal.hot, hz)
	d.poke()
}

// handle answers the Shell; run then sets its timer by what changed.
func (d *daemon) handle(method string, params json.RawMessage) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.poke()
	ok := map[string]bool{"ok": true}
	switch method {
	case svc.DisplayState:
		d.decide()
		return d.state(), nil
	case svc.DisplaySetting:
		var p struct{ Setting string }
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if !d.policy.SetSetting(p.Setting) {
			return nil, fmt.Errorf("no such setting %q", p.Setting)
		}
		d.decide()
		return d.state(), nil
	case svc.DisplayHint:
		var h svc.DisplayHintArgs
		if err := json.Unmarshal(params, &h); err != nil {
			return nil, err
		}
		if h.Source == "" {
			return nil, errors.New("a hint names its source")
		}
		if h.Kind == svc.DisplayClear && strings.HasSuffix(h.Source, "*") {
			d.policy.ClearPrefix(strings.TrimSuffix(h.Source, "*"))
		} else if !d.policy.Hint(h, d.now()) {
			return nil, fmt.Errorf("no such hint %q (fps %d)", h.Kind, h.FPS)
		}
		d.decide()
		return d.state(), nil
	case svc.DisplayCap:
		var c svc.DisplayCapArgs
		if err := json.Unmarshal(params, &c); err != nil {
			return nil, err
		}
		if c.Source == "" || c.Source == svc.DisplayCapThermal {
			return nil, errors.New("cap: a source of the Shell's (battery)")
		}
		d.policy.SetCap(c.Source, c.Hz)
		d.decide()
		return d.state(), nil
	case svc.DisplayPanel:
		var p svc.DisplayPanelArgs
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.KMS {
			if err := d.panel.Open(); err != nil {
				log.Printf("panel: %v", err)
				return nil, err
			}
			if r := d.panel.Rates(); !slices.Equal(r, d.policy.Rates()) {
				d.policy.SetRates(r)
				d.want = 0
				log.Printf("panel %s, rates %v, at %d Hz", d.panel.Name(), r, d.panel.Hz())
			}
		}
		if p.Applied > 0 {
			d.panel.Applied(p.Applied)
		}
		if p.Err != "" {
			log.Printf("the renderer could not switch to %d Hz: %s", d.want, p.Err)
		}
		d.decide()
		return d.state(), nil
	case svc.DisplaySimThermal:
		if !d.thermal.sim {
			return nil, errors.New("the thermal zones are real")
		}
		var p struct{ Hot bool }
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Hot != d.thermal.hot {
			d.thermal.hot = p.Hot
			d.thermalCap()
		}
		d.decide()
		return ok, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
