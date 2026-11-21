package displayd

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Panel is the panel's modes. displayd only chooses: the renderer holds
// the panel (DRM master) and switches to the mode it is told of, and says
// which one it switched to (Applied).
type Panel interface {
	// Name: "sim", "kms:<connector>"; "" before the modes are known.
	Name() string
	// Rates: its modes' rates at the size it runs at, lowest first; none
	// before they are known.
	Rates() []int
	// Hz is the rate it runs at.
	Hz() int
	// Want: the rate chosen; a simulated panel runs at it at once.
	Want(hz int)
	// Applied: the renderer switched to a mode of this rate.
	Applied(hz int)
	// Open: the renderer drives the panel now; a phone's modes can be read.
	Open() error
}

// simPanel stands in for a panel on the developer machine and in the
// emulator's selftest: its rates switch at once.
type simPanel struct {
	mu    sync.Mutex
	rates []int
	hz    int
}

func newSimPanel(rates []int) *simPanel {
	p := &simPanel{rates: slices.Clone(rates)}
	slices.Sort(p.rates)
	p.hz = p.rates[0]
	return p
}

func (p *simPanel) Name() string   { return "sim" }
func (p *simPanel) Rates() []int   { return slices.Clone(p.rates) }
func (p *simPanel) Open() error    { return nil }
func (p *simPanel) Applied(hz int) {}

func (p *simPanel) Hz() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hz
}

func (p *simPanel) Want(hz int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slices.Contains(p.rates, hz) {
		p.hz = hz
	}
}

// kmsPanel is a phone's panel: the connected connector's modes and the
// CRTC's mode, read through DRM/KMS without becoming the card's master.
type kmsPanel struct {
	mu    sync.Mutex
	paths []string
	name  string
	rates []int
	hz    int
}

func (p *kmsPanel) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.name
}

func (p *kmsPanel) Rates() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.rates)
}

func (p *kmsPanel) Hz() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hz
}

func (p *kmsPanel) Want(int) {}

func (p *kmsPanel) Applied(hz int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slices.Contains(p.rates, hz) {
		p.hz = hz
	}
}

// Open reads the modes once: they belong to the panel, not to the
// renderer process that drives it now.
func (p *kmsPanel) Open() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.name != "" {
		return nil
	}
	var errs []error
	for _, path := range p.paths {
		out, err := readKMS(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		p.name, p.rates, p.hz = "kms:"+out.connector, out.rates, out.hz
		return nil
	}
	if len(errs) == 0 {
		return errors.New("no DRM card")
	}
	return errors.Join(errs...)
}

// kmsInfo is what readKMS found: the connector, its modes' rates at the
// size the CRTC runs at, and the CRTC's rate.
type kmsInfo struct {
	connector string
	rates     []int
	hz        int
}

// openPanel is the --panel flag: "sim" (60 and 120 Hz), "sim:<rate>,..."
// (a panel of those rates), "kms" (/dev/dri/card0, else card1), or
// "kms:<card>"; "auto" is kms where the machine has a DRM card, else sim.
func openPanel(spec string) (Panel, error) {
	kind, arg, _ := strings.Cut(spec, ":")
	if kind == "auto" {
		kind, arg = "sim", ""
		if _, err := os.Stat("/dev/dri/card0"); err == nil {
			kind = "kms"
		}
	}
	switch kind {
	case "sim":
		rates := []int{60, 120}
		if arg != "" {
			rates = nil
			for _, f := range strings.Split(arg, ",") {
				r, err := strconv.Atoi(strings.TrimSpace(f))
				if err != nil || r <= 0 || r > 1000 {
					return nil, fmt.Errorf("--panel %s: %q is no rate", spec, f)
				}
				if !slices.Contains(rates, r) {
					rates = append(rates, r)
				}
			}
		}
		return newSimPanel(rates), nil
	case "kms":
		paths := []string{"/dev/dri/card0", "/dev/dri/card1"}
		if arg != "" {
			paths = []string{arg}
		}
		return &kmsPanel{paths: paths}, nil
	}
	return nil, fmt.Errorf("--panel %q: auto, sim[:<rates>] or kms[:<card>]", spec)
}
