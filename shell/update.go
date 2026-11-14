package shell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/ota/verity"
)

// System updates (R37): two system slots (phone-ota), RAUC writes the one not
// running and phone-initramfs boots it next. The settings app drives it
// through system.update; RAUC checks the bundle's signature against the
// image's keyring and that it is for this machine, and a slot that never
// comes up healthy is left for the last good one. Where latest.json has an
// incremental bundle from the system the phone runs, the phone installs that
// one (its hook rebuilds the slot from the running one, ota/delta), else the
// full bundle, adaptively.

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
}

type updater struct {
	state string
	err   string
	held  string
	offer *updateOffer
	// rauc and slots are RAUC's command and phone-ota's state directory
	// (tests point them elsewhere).
	rauc  string
	slots string
}

func (u *updater) init() {
	if u.rauc == "" {
		u.rauc = "rauc"
	}
	if u.slots == "" {
		u.slots = "/var/lib/slots"
	}
	if u.state == "" {
		u.state = "idle"
	}
}

var slotLine = regexp.MustCompile(`^(slot|good|booted)=([ab])$`)

func (c *Core) updateStatus() UpdateStatus {
	u := &c.update
	u.init()
	s := UpdateStatus{Version: shownVersion(), State: u.state, Error: u.err, Available: u.offer, Held: u.held}
	raw, err := os.ReadFile(filepath.Join(u.slots, "boot"))
	if _, lerr := exec.LookPath(u.rauc); err != nil || lerr != nil {
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

// checkOfferSignature: with key set, latest.json must carry X-Offer-Issued
// and X-Offer-Signature (Ed25519 over ota.OfferMessage), not be older than
// offerMaxAge, and not older than the last one this phone accepted
// (seenPath keeps that time).
func checkOfferSignature(key ed25519.PublicKey, h http.Header, body []byte, seenPath string, now time.Time) error {
	if key == nil {
		return nil
	}
	issued, err := strconv.ParseInt(h.Get("X-Offer-Issued"), 10, 64)
	sig, serr := base64.StdEncoding.DecodeString(h.Get("X-Offer-Signature"))
	if err != nil || serr != nil || !ed25519.Verify(key, ota.OfferMessage(issued, body), sig) {
		return errors.New("update server: latest.json is not signed by the update server's key")
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

func fetchOffer(manifest string, key ed25519.PublicKey, seenPath string, now time.Time) (*updateOffer, error) {
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
	u.state, u.err = "installing", ""
	c.postFrame()
	target, full := u.offer.URL, ""
	if d := u.offer.Delta; d != nil {
		target, full = d.URL, u.offer.URL
	}
	c.log.Printf("system update: installing %s", target)
	go func() {
		out, err := exec.Command(u.rauc, "install", target).CombinedOutput()
		if err != nil && full != "" {
			c.log.Printf("system update: the incremental bundle did not install (%s); installing %s", strings.TrimSpace(string(out)), full)
			out, err = exec.Command(u.rauc, "install", full).CombinedOutput()
		}
		c.Post(func() {
			if err != nil {
				u.state, u.err = "failed", raucError(string(out))
				c.log.Printf("system update failed: %s", strings.TrimSpace(string(out)))
				reply(c.updateStatus(), errors.New(u.err))
				return
			}
			u.state = "installed"
			c.log.Printf("system update installed")
			st := c.updateStatus()
			c.rememberInstall(updateReport{Version: u.offer.Version, Slot: st.Pending, Server: c.cfg.UpdateURL})
			reply(st, nil)
		})
	}()
}

// raucError is the user's reading of why RAUC refused or failed.
func raucError(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "signature") || strings.Contains(low, "certificate") || strings.Contains(low, "verif"):
		return tr.T("The update's signature is not trusted; it was not installed")
	case strings.Contains(low, "version mismatch"):
		return tr.T("This update is older than the system on the phone and cannot be installed")
	case strings.Contains(low, "compatible"):
		return tr.T("This update is not for this device")
	case strings.Contains(low, "download") || strings.Contains(low, "http") || strings.Contains(low, "connect"):
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
	c.log.Printf("system update: restarting into the new slot")
	go func() { _ = exec.Command("systemctl", "reboot").Run() }()
}
