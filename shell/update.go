package shell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/keyset"
	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/ota/delta"
	"github.com/kario-os/kario/ota/verity"
)

// System updates (R37): two system slots (phone-ota), RAUC writes the one not
// running and phone-initramfs boots it next. The settings app drives it
// through system.update; RAUC checks the bundle's signature against the
// image's keyring and that it is for this machine, and a slot that never
// comes up healthy is left for the last good one. Where latest.json has an
// incremental bundle from the system the phone runs, the phone installs that
// one (its hook rebuilds the slot from the running one, ota/delta), else the
// full bundle, adaptively. A download that stops is tried again, an
// incremental one resuming at its applier's checkpoint; updateauto.go
// installs updates by itself.

type updateOffer struct {
	Version string `json:"version"`
	Build   string `json:"build,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Notes   string `json:"notes,omitempty"`
	URL     string `json:"url"`
	// A staged rollout: the share of phones (percent) offered it so far,
	// all without; paused stops it for everyone. Each phone decides by its
	// own bucket, which never leaves it.
	Rollout *float64 `json:"rollout,omitempty"`
	Paused  bool     `json:"paused,omitempty"`
	// Incremental bundles, each from one earlier system: the version, and
	// the verity root hash of its slot image where the server knows it.
	Deltas []updateDelta `json:"deltas,omitempty"`
	// Delta is the one for the system this phone runs, nil for none.
	Delta *updateDelta `json:"delta,omitempty"`
}

type updateDelta struct {
	From         string `json:"from"`
	FromRootHash string `json:"from_root_hash,omitempty"`
	URL          string `json:"url"`
	Size         int64  `json:"size,omitempty"`
}

// UpdateStatus is what system.update answers.
type UpdateStatus struct {
	// Supported: RAUC and an A/B disk (the state partition).
	Supported bool         `json:"supported"`
	Version   string       `json:"version"`
	Slot      string       `json:"slot,omitempty"`    // the running slot, a or b
	Good      string       `json:"good,omitempty"`    // the last slot marked good
	Pending   string       `json:"pending,omitempty"` // installed, boots next
	State     string       `json:"state"`             // idle | installing | installed | failed
	Error     string       `json:"error,omitempty"`
	Available *updateOffer `json:"available,omitempty"`
	// Held: why the server's version is not offered here yet.
	Held string `json:"held,omitempty"`
	// Progress of the install running; Retry: its download stopped and is
	// tried again then.
	Progress *UpdateProgress `json:"progress,omitempty"`
	Retry    *UpdateRetry    `json:"retry,omitempty"`
	// Automatic: installed by automatic updates (updateauto.go); Tonight:
	// the phone restarts into it tonight; Paused: why an automatic install
	// stopped part way waits, to go on by itself.
	Automatic bool   `json:"automatic,omitempty"`
	Tonight   bool   `json:"tonight,omitempty"`
	Paused    string `json:"paused,omitempty"`
}

// UpdateProgress: RAUC's percent and, for an incremental bundle, the
// applier's operations done of all, and the one it resumed at.
type UpdateProgress struct {
	Percent int `json:"percent"`
	Done    int `json:"done,omitempty"`
	Total   int `json:"total,omitempty"`
	Resumed int `json:"resumed,omitempty"`
}

// UpdateRetry: the attempt waiting, of how many, at what time of day.
type UpdateRetry struct {
	Attempt int    `json:"attempt"`
	Of      int    `json:"of"`
	At      string `json:"at"`
}

// An install whose download stops (the network went away) is tried again
// updateRetries times, updateRetryBase after the first failure and four
// times longer after each next; an incremental bundle resumes where its
// applier's checkpoint is (ota/delta).
const (
	updateRetries   = 3
	updateRetryBase = 30 * time.Second
)

type updater struct {
	state string
	err   string
	held  string
	offer *updateOffer
	// rauc and slots are RAUC's command and phone-ota's state directory,
	// deltaLog the incremental bundle hook's log of its applier, stopFile
	// the file that stops its applier (tests point them elsewhere).
	rauc     string
	slots    string
	deltaLog string
	stopFile string
	// install runs one install, handing over its output line by line
	// (rauc install); restart restarts into the new slot (systemctl
	// reboot); unmetered, when set, says whether the phone is on unmetered
	// Wi-Fi instead of a look at the network (metered.go); cutStream, when
	// set (tests), ends the download of the bundle installing, which
	// otherwise the proxy does. The selftest's simulated phone stands in
	// for the first three.
	install   func(url string, line func(string)) (string, error)
	restart   func()
	unmetered func(func(bool))
	cutStream func() error
	// info stands in for rauc info: a bundle's version ("" unknown).
	info func(path string) string
	// out is what RAUC said as the last install failed.
	out string
	// proxy: what an automatic install downloads through (updateproxy.go).
	proxy *installProxy
	// The install going on: its version, the bundles left to try (the
	// incremental one first), the network retries so far and when the next
	// is due, RAUC's last percent, and who to answer (none for an automatic
	// install). A check meanwhile may replace the offer.
	version   string
	plan      []string
	retries   int
	retryAt   time.Time
	percent   int
	automatic bool
	reply     func(any, error)
	auto      autoUpdate
	// An automatic install being stopped (updateauto.go): why, since when,
	// whether its download was ended, and a look at the network under way
	// and when the last began; one stopped, paused: why, and when its
	// conditions were last checked.
	stopping  string
	stopAt    time.Time
	streamCut bool
	watching  bool
	watchAt   time.Time
	paused    string
	pausedAt  time.Time
}

func (u *updater) init() {
	if u.rauc == "" {
		u.rauc = "rauc"
	}
	if u.slots == "" {
		u.slots = "/var/lib/slots"
	}
	if u.deltaLog == "" {
		u.deltaLog = "/run/phone-ota-delta.log"
	}
	if u.stopFile == "" {
		u.stopFile = "/run/phone-ota-delta.stop"
	}
	if u.state == "" {
		u.state = "idle"
	}
}

var slotLine = regexp.MustCompile(`^(slot|good|booted)=([ab])$`)

func (c *Core) updateStatus() UpdateStatus {
	u := &c.update
	u.init()
	s := UpdateStatus{Version: shownVersion(), State: u.state, Error: u.err, Available: u.offer, Held: u.held,
		Automatic: u.automatic && u.state == "installed", Tonight: u.auto.Tonight}
	if u.state == "idle" {
		s.Paused = pauseText(u.paused)
	}
	if u.state == "installing" {
		s.Progress = &UpdateProgress{Percent: u.percent}
		if len(u.plan) > 1 {
			if raw, err := os.ReadFile(u.deltaLog); err == nil {
				p := delta.ParseLog(raw)
				s.Progress.Done, s.Progress.Total, s.Progress.Resumed = p.Done, p.Total, p.Resumed
			}
		}
		if !u.retryAt.IsZero() {
			s.Retry = &UpdateRetry{Attempt: u.retries, Of: updateRetries, At: c.clockText(u.retryAt)}
		}
	}
	raw, err := os.ReadFile(filepath.Join(u.slots, "boot"))
	if _, lerr := exec.LookPath(u.rauc); err != nil || (lerr != nil && u.install == nil) {
		return s
	}
	s.Supported = true
	kv := map[string]string{}
	for _, l := range strings.Split(string(raw), "\n") {
		if m := slotLine.FindStringSubmatch(l); m != nil {
			kv[m[1]] = m[2]
		}
	}
	s.Slot, s.Good = kv["booted"], kv["good"]
	if kv["slot"] != "" && kv["slot"] != kv["booted"] {
		s.Pending = kv["slot"]
	}
	return s
}

// updateCheck reads the update server's latest.json.
func (c *Core) updateCheck(reply func(any, error)) {
	if c.cfg.UpdateURL == "" {
		reply(nil, errors.New("no update server is configured"))
		return
	}
	base := strings.TrimSuffix(c.cfg.UpdateURL, "/") + "/latest.json"
	bucket := c.updateBucket()
	key, err := updateServerKey(c.cfg.UpdateKey)
	if err != nil {
		reply(nil, err)
		return
	}
	seenPath := filepath.Join(c.cfg.StateDir, "shell", "update-issued")
	c.update.init()
	go func() {
		offer, err := fetchOffer(base, key, seenPath, time.Now())
		c.Post(func() {
			if err == nil {
				c.update.offer, c.update.held = offer, ""
				if why := held(offer, bucket); why != "" {
					c.update.offer, c.update.held = nil, why
					c.log.Printf("system update: %s %s held: %s", offer.Version, offer.URL, why)
				} else if releaseVersion.MatchString(SystemVersion) && !versionNewer(offer.Version, SystemVersion) {
					c.update.offer, c.update.held = nil, tr.T("Already the latest version (%s)", SystemVersion)
				} else if offer.Delta = pickDelta(offer.Deltas, SystemVersion, runningRootHash(c.update.slots)); offer.Delta != nil {
					c.log.Printf("system update: %s from %s incrementally (%s)", offer.Version, offer.Delta.From, offer.Delta.URL)
				}
			}
			reply(c.updateStatus(), err)
		})
	}()
}

// pickDelta is the incremental bundle for the system this phone runs: by
// the root hash of its slot image where the server gives one (an image is
// that exactly), else by its release version; nil for the full bundle.
func pickDelta(deltas []updateDelta, version, rootHash string) *updateDelta {
	for i := range deltas {
		d := &deltas[i]
		switch {
		case d.FromRootHash != "":
			if rootHash != "" && strings.EqualFold(d.FromRootHash, rootHash) {
				return d
			}
		case d.From != "" && d.From == version && releaseVersion.MatchString(version):
			return d
		}
	}
	return nil
}

// runningRootHash is the verity root hash of the slot running, from its
// descriptor in the state partition ("" when there is none).
func runningRootHash(slots string) string {
	raw, err := os.ReadFile(filepath.Join(slots, "boot"))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if m := slotLine.FindStringSubmatch(l); m != nil && m[1] == "booted" {
			desc, err := os.ReadFile(filepath.Join(slots, "rootfs_"+m[2]+".verity"))
			if err != nil {
				return ""
			}
			d, err := verity.ParseDescriptor(desc)
			if err != nil {
				return ""
			}
			return hex.EncodeToString(d.RootHash[:])
		}
	}
	return ""
}

// held says why a phone in bucket (0–9999) does not get the offer yet, ""
// when it does.
func held(o *updateOffer, bucket int) string {
	switch {
	case o.Paused:
		return tr.T("This version is paused; check again later")
	case o.Rollout != nil && float64(bucket) >= *o.Rollout*100:
		return tr.T("This version is rolling out in stages (%g%%); this device's turn has not come yet", *o.Rollout)
	}
	return ""
}

// releaseVersion is a release's version (make release, PHONE_VERSION); a
// development build's ("0.6 (prototype)") is not, and is offered any version.
var releaseVersion = regexp.MustCompile(`^\d+(\.\d+)*(-[0-9A-Za-z.]+)?$`)

// versionNewer: a is a later release than b, comparing dot-separated
// numbers (0.10.0 after 0.9.2); a pre-release (0.8.0-rc1) comes before its
// release. A release's own image refuses older bundles in RAUC too
// (min-bundle-version), whatever latest.json says.
func versionNewer(a, b string) bool {
	core := func(v string) ([]string, string) {
		c, pre, _ := strings.Cut(v, "-")
		return strings.Split(c, "."), pre
	}
	ac, apre := core(a)
	bc, bpre := core(b)
	part := func(p []string, i int) string {
		if i < len(p) {
			return p[i]
		}
		return "0"
	}
	for i := 0; i < max(len(ac), len(bc)); i++ {
		x, y := part(ac, i), part(bc, i)
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		if xerr != nil || yerr != nil {
			if x != y {
				return x > y
			}
			continue
		}
		if xn != yn {
			return xn > yn
		}
	}
	switch {
	case apre == bpre:
		return false
	case apre == "":
		return true
	case bpre == "":
		return false
	}
	return apre > bpre
}

// updateBucket is this phone's place in staged rollouts, drawn once and
// kept with the data both system slots share.
func (c *Core) updateBucket() int {
	path := filepath.Join(c.cfg.StateDir, "shell", "update-bucket")
	var b int
	if raw, err := os.ReadFile(path); err == nil {
		if _, err := fmt.Sscan(string(raw), &b); err == nil && b >= 0 && b < 10000 {
			return b
		}
	}
	var r [2]byte
	_, _ = rand.Read(r[:])
	b = int(binary.BigEndian.Uint16(r[:])) % 10000
	_ = os.WriteFile(path, []byte(fmt.Sprintln(b)), 0o600)
	return b
}

// offerMaxAge: a signed latest.json older than this is refused, so a
// server or mirror that stopped being updated cannot keep a phone on an old
// release by serving yesterday's answer.
const offerMaxAge = 7 * 24 * time.Hour

// checkOfferSignature: with keys set, latest.json must carry X-Offer-Issued
// and X-Offer-Signature (Ed25519 over ota.OfferMessage) by the key
// X-Offer-Key names (any of keys without it), not be older than
// offerMaxAge, and not older than the last one this phone accepted
// (seenPath keeps that time).
func checkOfferSignature(keys keyset.Set, h http.Header, body []byte, seenPath string, now time.Time) error {
	if keys == nil {
		return nil
	}
	issued, err := ota.VerifyHeaders(h, keys, func(at int64) []byte { return ota.OfferMessage(at, body) })
	if err != nil {
		return fmt.Errorf("update server: latest.json is not signed by the update server's key: %w", err)
	}
	at := time.Unix(issued, 0)
	switch {
	case now.Sub(at) > offerMaxAge:
		return fmt.Errorf("update server: latest.json was signed %s, too long ago", at.Format(time.DateOnly))
	case at.Sub(now) > 24*time.Hour:
		return errors.New("update server: latest.json is signed in the future (is the clock right?)")
	}
	if raw, err := os.ReadFile(seenPath); err == nil {
		if last, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && issued < last {
			return errors.New("update server: latest.json is older than one already seen")
		}
	}
	_ = os.WriteFile(seenPath, []byte(strconv.FormatInt(issued, 10)+"\n"), 0o600)
	return nil
}

func fetchOffer(manifest string, key keyset.Set, seenPath string, now time.Time) (*updateOffer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update server: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("update server: %w", err)
	}
	if err := checkOfferSignature(key, resp.Header, body, seenPath, now); err != nil {
		return nil, err
	}
	var o updateOffer
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, fmt.Errorf("update server: %w", err)
	}
	ref, err := url.Parse(o.URL)
	if err != nil || o.URL == "" {
		return nil, errors.New("update server: no bundle")
	}
	base, _ := url.Parse(manifest)
	o.URL = base.ResolveReference(ref).String()
	deltas := o.Deltas
	o.Deltas, o.Delta = nil, nil
	for _, d := range deltas {
		if ref, err := url.Parse(d.URL); err == nil && d.URL != "" {
			d.URL = base.ResolveReference(ref).String()
			o.Deltas = append(o.Deltas, d)
		}
	}
	return &o, nil
}

// updateInstall streams the offered bundle into the other slot (rauc
// install verifies it first); the request is answered when it is done. An
// incremental bundle that does not install is followed by the full one.
func (c *Core) updateInstall(reply func(any, error)) {
	u := &c.update
	u.init()
	st := c.updateStatus()
	switch {
	case !st.Supported:
		reply(nil, errors.New("this device has no A/B system slots"))
		return
	case u.offer == nil:
		reply(nil, errors.New("check for an update first"))
		return
	case u.state == "installing":
		reply(nil, errors.New("an update is being installed"))
		return
	}
	c.startInstall(reply)
}

// startInstall installs the offer, its incremental bundle first if it has
// one; reply hears how it ended (nil: an automatic install, which posts a
// notification instead).
func (c *Core) startInstall(reply func(any, error)) {
	u := &c.update
	u.state, u.err, u.reply, u.automatic, u.retries, u.version = "installing", "", reply, reply == nil, 0, u.offer.Version
	u.paused = ""
	u.plan = []string{u.offer.URL}
	if d := u.offer.Delta; d != nil {
		u.plan = []string{d.URL, u.offer.URL}
	}
	c.installNext()
}

var raucPercent = regexp.MustCompile(`^\s*(\d{1,3})% `)

// installNext runs the first install of the plan.
func (c *Core) installNext() {
	u := &c.update
	target, run := u.plan[0], u.install
	if run == nil {
		rauc := u.rauc
		run = func(url string, line func(string)) (string, error) { return raucInstall(rauc, url, line) }
	}
	u.percent, u.retryAt = 0, time.Time{}
	u.stopping, u.streamCut = "", false
	_ = os.Remove(u.stopFile)
	if len(u.plan) > 1 {
		// What an earlier install's hook left must not read as this one's.
		_ = os.Remove(u.deltaLog)
	}
	c.postFrame()
	c.log.Printf("system update: installing %s%s", target, map[bool]string{true: " (automatic)"}[u.automatic])
	from := target
	if u.automatic {
		if p, err := newInstallProxy(target); err != nil {
			c.log.Printf("system update: %s straight from the server: %v", target, err)
		} else {
			u.proxy, from = p, p.url
		}
	}
	go func() {
		out, err := run(from, func(line string) {
			if m := raucPercent.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				c.Post(func() { u.percent = min(n, 100) })
			}
		})
		c.Post(func() { c.installEnded(target, out, err) })
	}()
}

// installEnded goes on from one install of the plan: done, paused after an
// automatic install stopped on purpose, tried again after a download that
// stopped, the full bundle after an incremental one that did not install,
// or failed.
func (c *Core) installEnded(target, out string, err error) {
	u := &c.update
	var hookLog []byte
	if len(u.plan) > 1 {
		hookLog, _ = os.ReadFile(u.deltaLog)
	}
	stopping := u.stopping
	u.stopping, u.streamCut = "", false
	_ = os.Remove(u.stopFile)
	if u.proxy != nil {
		u.proxy.close()
		u.proxy = nil
	}
	switch {
	case err != nil && stopping != "":
		u.state, u.plan, u.paused, u.pausedAt = "idle", nil, stopping, c.now()
		c.log.Printf("system update: %s stopped (%s)", target, lastLines(out, 3))
		c.autoSays("paused (%s)", stopping)
		c.postFrame()
		return
	case err == nil:
		u.state, u.plan = "installed", nil
		c.log.Printf("system update installed")
		st := c.updateStatus()
		c.rememberInstall(updateReport{Version: u.version, Slot: st.Pending, Server: c.cfg.UpdateURL})
		if u.reply != nil {
			u.reply(st, nil)
		} else {
			c.autoInstalled()
		}
		u.reply = nil
		return
	case u.retries < updateRetries && (downloadStopped(out, hookLog) || u.retries > 0 && stillDown(out, hookLog)):
		u.retries++
		u.retryAt = c.now().Add(updateRetryBase << (2 * (u.retries - 1)))
		c.log.Printf("system update: the download of %s stopped (%s); trying again at %s (%d of %d)", target, lastLines(out, 3), u.retryAt.Format(time.TimeOnly), u.retries, updateRetries)
		c.postFrame()
		return
	case len(u.plan) > 1:
		c.log.Printf("system update: the incremental bundle did not install (%s); installing %s", lastLines(out, 3), u.plan[1])
		u.plan = u.plan[1:]
		c.installNext()
		return
	}
	u.state, u.err, u.plan, u.out = "failed", raucError(out), nil, out
	c.log.Printf("system update failed: %s", strings.TrimSpace(out))
	if u.reply != nil {
		u.reply(c.updateStatus(), errors.New(u.err))
	} else {
		c.autoFailed()
	}
	u.reply = nil
}

// retryInstall starts the retry that is due (the shell's tick); an
// automatic install goes on only while the phone still meets its
// conditions.
func (c *Core) retryInstall() {
	u := &c.update
	if u.state != "installing" || u.retryAt.IsZero() || c.now().Before(u.retryAt) {
		return
	}
	if !u.automatic {
		c.installNext()
		return
	}
	u.retryAt = time.Time{}
	c.autoConditions(func(why string) {
		if u.state != "installing" || len(u.plan) == 0 {
			return
		}
		if why != "" {
			u.state, u.plan, u.paused, u.pausedAt = "idle", nil, why, c.now()
			c.autoSays("paused (%s)", why)
			return
		}
		c.installNext()
	})
}

// raucInstall runs rauc install, handing over its output line by line as it
// comes (its progress: "NN% <step>").
func raucInstall(rauc, url string, line func(string)) (string, error) {
	cmd := exec.Command(rauc, "install", url)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Split(scanLinesOrCR)
		for sc.Scan() {
			out.WriteString(sc.Text() + "\n")
			line(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := cmd.Run()
	_ = pw.Close()
	<-done
	return out.String(), err
}

// scanLinesOrCR splits at a newline or a carriage return (a progress line
// redrawn in place).
func scanLinesOrCR(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// downloadStopped: the install failed because the bundle stopped arriving
// (RAUC's download, or the payload under an incremental bundle's hook, as
// its applier logs it), not because of what the bundle holds.
func downloadStopped(out string, hookLog []byte) bool {
	return delta.ParseLog(hookLog).Stalled || raucKind(out) == "download"
}

// stillDown: a retry of a stopped download that failed before the bundle's
// hook ran, for no reason RAUC names. The network is most likely still
// away: RAUC fetching the bundle from a server that does not answer says
// only that the install failed.
func stillDown(out string, hookLog []byte) bool {
	return len(bytes.TrimSpace(hookLog)) == 0 && raucKind(out) == ""
}

// lastLines: out's last n lines, on one line for the log.
func lastLines(out string, n int) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], " | ")
}

var urlInText = regexp.MustCompile(`\bhttps?://\S+`)

// raucKind is why RAUC refused or failed: signature, version, compatible,
// download or "" (anything else). The bundle's URL in its messages says
// nothing of the network.
func raucKind(out string) string {
	low := strings.ToLower(urlInText.ReplaceAllString(out, ""))
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("signature", "certificate", "verif"):
		return "signature"
	case has("version mismatch"):
		return "version"
	case has("compatible"):
		return "compatible"
	case has("download", "http", "connect", "timed out", "timeout", "network", "resolve", "nbd", "curl", "stream bundle"):
		return "download"
	}
	return ""
}

// raucError is the user's reading of why RAUC refused or failed.
func raucError(out string) string {
	switch raucKind(out) {
	case "signature":
		return tr.T("The update's signature is not trusted; it was not installed")
	case "version":
		return tr.T("This update is older than the system on the phone and cannot be installed")
	case "compatible":
		return tr.T("This update is not for this device")
	case "download":
		return tr.T("Downloading the update failed")
	}
	return tr.T("Installation failed")
}

// updateReboot restarts into the newly installed slot.
func (c *Core) updateReboot(reply func(any, error)) {
	if c.update.state != "installed" {
		reply(nil, errors.New("no update is waiting for a restart"))
		return
	}
	reply(map[string]bool{"ok": true}, nil)
	c.restartIntoUpdate("Settings")
}

// restartIntoUpdate restarts into the newly installed slot.
func (c *Core) restartIntoUpdate(why string) {
	c.log.Printf("system update: restarting into the new slot (%s)", why)
	if c.update.restart != nil {
		c.update.restart()
		return
	}
	go func() { _ = exec.Command("systemctl", "reboot").Run() }()
}
