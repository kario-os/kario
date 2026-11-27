package shell

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/ota"
)

// The selftest's simulated phone for system updates (ota ...): A/B slots
// as phone-ota keeps them (a boot state of its own), RAUC as the shell
// drives it (a stand-in that downloads the bundle piece by piece with
// range requests, printing its progress the way rauc install does, and
// sets the other slot for the next start), the restart, the network the
// phone is on once ota net names it (until then the phone's own, metered
// or not: metered.go), and an update server on this machine that signs its
// latest.json with a key of its own and offers a newer system. The
// machine's own slots, RAUC and update server are left alone, and ota off
// puts everything back. The real slots and RAUC are tested on the emulator
// (yocto/qemu-ota.sh, qemu-ota-delta.sh). An automatic install downloads
// through the shell's proxy, as on a phone, and stopping it cuts the
// stand-in's download as it cuts RAUC's (updateproxy.go).

const (
	otaSimBundle = 4 << 20
	otaSimPiece  = 256 << 10
	otaSimSlow   = 400 * time.Millisecond
)

type otaSim struct {
	srv  *http.Server
	url  string
	dir  string
	slow time.Duration
	// The updater, settings and files as they were before the simulation.
	saved      updater
	savedURL   string
	savedKey   string
	savedFiles map[string][]byte
	mu         sync.Mutex
	cuts       int // downloads still to stop halfway
	restarts   int
	wifi       bool // the network ota net names
}

func (r *Runner) otaStep(arg string) error {
	f := strings.Fields(arg)
	if len(f) == 0 {
		return errors.New("ota sim [slow [ms]] | ota cut <n> | ota net wifi|mobile | ota clock <HH:MM> | ota auto <text> [ms] | ota installed [ms] | ota restarts <n> [ms] | ota off")
	}
	c, s := r.C, r.ota
	if f[0] != "sim" && s == nil {
		return errors.New("ota: no simulated phone (ota sim)")
	}
	ms := func(def int) int {
		if n, err := strconv.Atoi(f[len(f)-1]); err == nil && len(f) > 1 {
			f = f[:len(f)-1]
			return n
		}
		return def
	}
	switch f[0] {
	case "sim":
		if s != nil {
			return errors.New("ota: already simulating")
		}
		piece := time.Duration(ms(int(otaSimSlow/time.Millisecond))) * time.Millisecond
		if len(f) < 2 || f[1] != "slow" {
			piece = 0
		}
		s, err := r.otaSimulate(piece)
		if err != nil {
			return err
		}
		r.ota = s
	case "cut":
		n, err := strconv.Atoi(f[len(f)-1])
		if err != nil || len(f) != 2 {
			return errors.New("ota cut <n>")
		}
		s.mu.Lock()
		s.cuts = n
		s.mu.Unlock()
	case "net":
		if len(f) != 2 || (f[1] != "wifi" && f[1] != "mobile") {
			return errors.New("ota net wifi|mobile")
		}
		s.wifi = f[1] == "wifi"
		c.update.unmetered = func(then func(bool)) { then(s.wifi) }
	case "clock":
		t, err := time.Parse("15:04", f[len(f)-1])
		if err != nil || len(f) != 2 {
			return errors.New("ota clock <HH:MM>")
		}
		now := c.now()
		next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		c.SetClockSkew(next.Sub(now))
		r.pump(600 * time.Millisecond)
	case "auto":
		wait := ms(10000)
		text := strings.Join(f[1:], " ")
		if _, ok := r.waitFor(func(string) bool { return strings.Contains(c.update.auto.last, text) }, wait); !ok {
			return fmt.Errorf("the last automatic update says %q, not %q", c.update.auto.last, text)
		}
	case "installed":
		if _, ok := r.waitFor(func(string) bool { return c.update.state == "installed" }, ms(30000)); !ok {
			return fmt.Errorf("the update is %s (%s)", c.update.state, c.update.err)
		}
	case "restarts":
		wait := ms(5000)
		n, err := strconv.Atoi(f[len(f)-1])
		if err != nil || len(f) != 2 {
			return errors.New("ota restarts <n> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return s.restarts == n }, wait); !ok {
			return fmt.Errorf("restarted %d times, want %d", s.restarts, n)
		}
	case "off":
		r.ota = nil
		return s.end(c)
	default:
		return fmt.Errorf("ota %s: unknown", f[0])
	}
	return nil
}

// otaSimulate starts the simulated phone; its RAUC takes slow over each
// piece of the bundle it downloads.
func (r *Runner) otaSimulate(slow time.Duration) (*otaSim, error) {
	c := r.C
	c.update.init()
	s := &otaSim{saved: c.update, savedURL: c.cfg.UpdateURL, savedKey: c.cfg.UpdateKey, savedFiles: map[string][]byte{}, slow: slow}
	for _, p := range s.stateFiles(c) {
		if raw, err := os.ReadFile(p); err == nil {
			s.savedFiles[p] = raw
		}
	}
	dir, err := os.MkdirTemp(c.cfg.RunDir, "ota-sim-")
	if err != nil {
		return nil, err
	}
	s.dir = dir
	if err := os.WriteFile(filepath.Join(dir, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644); err != nil {
		return nil, err
	}
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	keyFile := filepath.Join(dir, "update-key.pub")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return nil, err
	}
	bundle := make([]byte, otaSimBundle)
	offer, _ := json.Marshal(map[string]any{"version": "9.0.0", "notes": "Selftest update", "size": len(bundle), "url": "phone.raucb"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/latest.json":
			issued := time.Now().Unix()
			_ = ota.SignHeaders(w.Header(), key, issued, ota.OfferMessage(issued, offer))
			_, _ = w.Write(offer)
		case "/phone.raucb":
			var from int
			_, _ = fmt.Sscanf(req.Header.Get("Range"), "bytes=%d-", &from)
			s.mu.Lock()
			cut := from >= len(bundle)/2 && s.cuts > 0
			if cut {
				s.cuts--
			}
			s.mu.Unlock()
			if cut && from+otaSimPiece <= len(bundle) {
				// Half the piece, then the connection goes: the client has
				// the response and cannot quietly send the request again.
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, from+otaSimPiece-1, len(bundle)))
				w.Header().Set("Content-Length", strconv.Itoa(otaSimPiece))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(bundle[from : from+otaSimPiece/2])
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				panic(http.ErrAbortHandler)
			}
			http.ServeContent(w, req, "phone.raucb", time.Time{}, bytes.NewReader(bundle))
		default:
			http.NotFound(w, req)
		}
	})}
	go func() { _ = s.srv.Serve(ln) }()
	s.url = "http://" + ln.Addr().String()
	c.cfg.UpdateURL, c.cfg.UpdateKey = s.url, keyFile
	u := &c.update
	u.state, u.err, u.held, u.offer, u.plan, u.retryAt, u.auto = "idle", "", "", nil, nil, time.Time{}, autoUpdate{loaded: true}
	u.slots, u.deltaLog, u.stopFile = dir, filepath.Join(dir, "phone-ota-delta.log"), filepath.Join(dir, "phone-ota-delta.stop")
	u.install = func(url string, line func(string)) (string, error) { return s.install(url, line) }
	u.info = func(path string) string {
		if simBundleSigned(path) {
			return "9.0.0"
		}
		return ""
	}
	u.restart = func() { s.restarts++ }
	u.unmetered, u.cutStream = nil, nil
	c.Trace("ota sim: an A/B phone, its update server at %s", s.url)
	return s, nil
}

// stateFiles are what the updater keeps in the state directory, put back
// as they were at ota off.
func (s *otaSim) stateFiles(c *Core) []string {
	return []string{c.autoUpdatePath(), filepath.Join(c.cfg.StateDir, "shell", "update-issued"), c.reportPath(), c.dayReportPath()}
}

// simBundleMagic begins a bundle the simulated RAUC takes as signed by
// the image's keyring (kario device update); anything else it refuses as
// RAUC refuses a bundle whose signature does not verify.
const simBundleMagic = "kario-sim-bundle\n"

func simBundleSigned(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(simBundleMagic))
	_, err = io.ReadFull(f, head)
	return err == nil && string(head) == simBundleMagic
}

// install stands in for rauc install: the bundle in pieces (from the
// update server, or a file on the phone), its progress as RAUC prints it,
// the other slot set for the next start.
func (s *otaSim) install(url string, line func(string)) (string, error) {
	var out strings.Builder
	say := func(format string, a ...any) {
		l := fmt.Sprintf(format, a...)
		out.WriteString(l + "\n")
		line(l)
	}
	say("  0%% Installing")
	if !strings.Contains(url, "://") {
		if !simBundleSigned(url) {
			say("Failed to verify bundle: signature verification failed: no matching signer in keyring")
			return out.String(), errors.New("rauc install: exit status 1")
		}
		for i := 1; i <= 4; i++ {
			say("%3d%% Copying image to rootfs.1", 10+20*i)
			time.Sleep(s.slow)
		}
		err := s.installed(url, say)
		return out.String(), err
	}
	cl := &http.Client{Timeout: 10 * time.Second}
	for off := 0; off < otaSimBundle; off += otaSimPiece {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+otaSimPiece-1))
		resp, err := cl.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err == nil && resp.StatusCode != http.StatusPartialContent {
				err = errors.New(resp.Status)
			}
		}
		if err != nil {
			say("Failed to download bundle %s: %v", url, err)
			return out.String(), errors.New("rauc install: exit status 1")
		}
		say("%3d%% Copying image to rootfs.1", 10+80*(off+otaSimPiece)/otaSimBundle)
		time.Sleep(s.slow)
	}
	err := s.installed(url, say)
	return out.String(), err
}

// installed sets the other slot for the next start, as RAUC's install
// ends.
func (s *otaSim) installed(url string, say func(string, ...any)) error {
	if err := os.WriteFile(filepath.Join(s.dir, "boot"), []byte("slot=b\ngood=a\ntries=3\nbooted=a\n"), 0o644); err != nil {
		return err
	}
	say("100%% Installing done.")
	say("Installing `%s` succeeded", url)
	return nil
}

func (s *otaSim) end(c *Core) error {
	_ = s.srv.Close()
	if c.update.proxy != nil {
		c.update.proxy.close()
	}
	c.update = s.saved
	c.cfg.UpdateURL, c.cfg.UpdateKey = s.savedURL, s.savedKey
	for _, p := range s.stateFiles(c) {
		if raw, ok := s.savedFiles[p]; ok {
			_ = os.WriteFile(p, raw, 0o600)
		} else {
			_ = os.Remove(p)
		}
	}
	for i, n := range c.notifs {
		if n.app == c.sys && n.n.ID == updateNotif {
			c.notifs = append(c.notifs[:i], c.notifs[i+1:]...)
			break
		}
	}
	return os.RemoveAll(s.dir)
}
