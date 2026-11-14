package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// Dump is the shell's state for diagnosis (phone-shell --dump, or SIGUSR1 to
// a running shell): apps and their processes, cgroups, queued pushes, the
// screen stack, overlays, settings and the low-level counters.
func (c *Core) Dump() map[string]any {
	type appDump struct {
		ID, Name, Version   string
		Running, Frozen     bool
		Installed, Verified bool
		PID                 int     `json:",omitempty"`
		MemoryMB            float64 `json:",omitempty"`
		Screens             []string
		QueuedPushes        int          `json:",omitempty"`
		Crashes             int          `json:",omitempty"`
		VoIPStrikes         int          `json:",omitempty"`
		Commands            []AppCommand `json:",omitempty"`
	}
	var apps []appDump
	for _, a := range c.apps {
		d := appDump{ID: a.M.ID, Name: a.M.Name, Version: a.M.Version, Running: a.running, Frozen: a.frozen,
			Installed: a.M.installed, Verified: a.verified, QueuedPushes: len(a.pushQ), VoIPStrikes: len(a.voipStrikes),
			Commands: a.M.Commands}
		if a.running && a.proc != nil {
			d.PID = a.proc.Pid
			d.MemoryMB = float64(c.appMemory(a)) / (1 << 20)
		}
		for _, v := range a.stack {
			d.Screens = append(d.Screens, v.S.ID)
		}
		for _, r := range c.Crashes() {
			if r.App == a.M.ID {
				d.Crashes++
			}
		}
		apps = append(apps, d)
	}
	var overlays []string
	for _, o := range c.overlays {
		overlays = append(overlays, typeName(o))
	}
	fg := ""
	if c.fg != nil {
		fg = c.fg.M.ID
	}
	out := map[string]any{
		"time": c.now().Format(time.RFC3339), "system": Brand, ui.StateForeground: fg, "apps": apps,
		"overlays": overlays, "settings": c.settings, "keylock": c.keylock,
		"cgroups": c.cg != nil, "sandboxed": c.Sandboxed(), "low_memory_kills": c.lowMem.count,
		"notifications": len(c.notifs), "push_connected": c.pushUp, "telephony": c.HasTelephony(),
	}
	if c.renderer != nil {
		out["renderer"] = c.renderer
	}
	return out
}

func typeName(v any) string { return strings.TrimPrefix(fmt.Sprintf("%T", v), "*shell.") }

// WriteDump writes Dump to RunDir/shell-dump.json.
func (c *Core) WriteDump() error {
	raw, err := json.MarshalIndent(c.Dump(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(DumpPath(c.cfg.RunDir), raw, 0o600)
}

func DumpPath(runDir string) string { return filepath.Join(runDir, "shell-dump.json") }
func PIDPath(runDir string) string  { return filepath.Join(runDir, "shell.pid") }
