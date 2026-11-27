package shell

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/ui"
)

// kario device bugreport (adb bugreport): a zip of what a bug report needs
// from the phone, the system's own records only. Nothing of the user's
// goes in (devReportReadme says what stays out and why); the apps' own
// logs stay out too, as an app may print what the user wrote.

const (
	// devReportFileMax: a log longer than this goes in by its end.
	devReportFileMax = 4 << 20
	devReportCmdWait = 15 * time.Second
)

const devReportReadme = `Kario bug report (kario device bugreport)

What is in it:
  about.json      the system: version, kernel, renderer, screen, uptime,
                  developer mode, system update status
  sku.json        the hardware variant the system runs as
  apps.txt        the apps: id, version, system or installed, and the store
                  that signed an installed one
  crashes.json    the app crash records: when, which app and version, why,
                  and native backtraces (without the app's log tail)
  shell.log       the Shell's log of this boot
  renderer.log    the renderer's log of this boot (gui.log)
  journal.txt     systemd's journal of this boot (the phone's services and
                  the system's), on a device
  services/       the services' logs, on a developer machine (no journal)
  dmesg.txt       the kernel's log
  rauc.txt        the system slots as RAUC and phone-ota keep them

What is left out, and stays on the phone:
  - message contents (SMS, MMS, chats), the call log, contacts, calendars
  - photos, videos, music, the user's files
  - keys of any kind: the keystore, app keys, payment cards, the device's
    attestation key, Wi-Fi passphrases and saved networks, VPN profiles,
    Bluetooth pairings
  - accounts and their tokens, push tokens
  - the apps' data and the apps' own logs (an app may log what the user
    wrote: get an app you develop's with kario device logs APP)
  - the settings file
The logs are the system's own; they name apps, services and hardware, not
what the user keeps. Read them before sending the report on.
`

// devReport is what the bug report takes from the Shell's state, read on
// the loop; build adds the files and commands off it.
type devReport struct {
	about   map[string]any
	sku     any
	apps    []string
	crashes []CrashRecord
	runDir  string
	journal string
	rauc    string
	slots   string
}

// devReportState is the report's part of the Shell's state; on the loop.
func (c *Core) devReportState() devReport {
	c.loadCrashes()
	u := &c.update
	u.init()
	about := c.about()
	about[ui.SettingDeveloperMode] = c.settings.DeveloperMode
	about["update"] = c.updateStatus()
	r := devReport{about: about, sku: c.cfg.SKU, runDir: c.cfg.RunDir, journal: c.dev.journal, rauc: u.rauc, slots: u.slots}
	if r.journal == "" && c.Sandboxed() {
		r.journal, _ = exec.LookPath("journalctl")
	}
	for _, a := range c.apps {
		kind := "system"
		if a.M.installed {
			kind = "installed, store " + cmp.Or(readInstallRecord(a.M.dir).Store, "?")
		}
		r.apps = append(r.apps, fmt.Sprintf("%s\t%s\t%s", a.M.ID, cmp.Or(a.M.Version, "-"), kind))
	}
	sort.Strings(r.apps)
	for _, cr := range c.crashes {
		cr.Log = nil
		r.crashes = append(r.crashes, cr)
	}
	return r
}

// run is a command's output, or what kept it from giving one.
func run(name string, args ...string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), devReportCmdWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		out = append(out, fmt.Sprintf("\n(%s %s: %v)\n", name, strings.Join(args, " "), err)...)
	}
	return lastBytes(out)
}

// lastBytes keeps the end of what is longer than devReportFileMax.
func lastBytes(b []byte) []byte {
	if len(b) <= devReportFileMax {
		return b
	}
	return append([]byte("(the beginning left out)\n"), b[len(b)-devReportFileMax:]...)
}

func fileEnd(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return []byte(fmt.Sprintf("(%v)\n", err))
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > devReportFileMax {
		_, _ = f.Seek(st.Size()-devReportFileMax, io.SeekStart)
	}
	raw, _ := io.ReadAll(f)
	return raw
}

// build writes the zip; off the loop.
func (r devReport) build() ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	add := func(name string, body []byte) error {
		w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	jsonOf := func(v any) []byte {
		raw, _ := json.MarshalIndent(v, "", "  ")
		return append(raw, '\n')
	}
	type zipFile struct {
		name string
		body []byte
	}
	files := []zipFile{
		{"README.txt", []byte(devReportReadme)},
		{"about.json", jsonOf(r.about)},
		{"sku.json", jsonOf(r.sku)},
		{"apps.txt", []byte(strings.Join(r.apps, "\n") + "\n")},
		{"crashes.json", jsonOf(r.crashes)},
		{"shell.log", fileEnd(filepath.Join(r.runDir, "shell.log"))},
		{"renderer.log", fileEnd(filepath.Join(r.runDir, "gui.log"))},
		{"dmesg.txt", run("dmesg")},
	}
	if r.journal != "" {
		files = append(files, zipFile{"journal.txt", run(r.journal, "--no-pager", "-o", "short-iso", "-b")})
	} else {
		paths, _ := filepath.Glob(filepath.Join(r.runDir, "*.log"))
		sort.Strings(paths)
		for _, p := range paths {
			name := strings.TrimSuffix(filepath.Base(p), ".log")
			if name == "shell" || name == "gui" || strings.Contains(name, ".") {
				continue
			}
			files = append(files, zipFile{"services/" + filepath.Base(p), fileEnd(p)})
		}
	}
	rauc := []byte("(no RAUC on this phone)\n")
	if p, err := exec.LookPath(r.rauc); err == nil {
		rauc = run(p, "status", "--detailed")
	}
	if boot, err := os.ReadFile(filepath.Join(r.slots, "boot")); err == nil {
		rauc = append(rauc, "\nphone-ota boot state:\n"...)
		rauc = append(rauc, boot...)
	}
	files = append(files, zipFile{"rauc.txt", rauc})
	for _, f := range files {
		if err := add(f.name, f.body); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// devBugreport answers the zip.
func (c *Core) devBugreport(conn net.Conn, req devctl.Request) {
	var r devReport
	err := c.onLoop(func() error {
		if err := c.devGate(req.Via); err != nil {
			return err
		}
		r = c.devReportState()
		return nil
	})
	var raw []byte
	if err == nil {
		raw, err = r.build()
	}
	if err != nil {
		_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
		return
	}
	c.Post(func() { c.log.Printf("dev link: bug report for the computer (%d bytes)", len(raw)) })
	if devctl.WriteReply(conn, devctl.Reply{OK: true, Size: int64(len(raw)), Note: "private data left out: see README.txt in the zip"}) == nil {
		_, _ = conn.Write(raw)
	}
}
