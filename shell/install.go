package shell

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Installing apps (R13): a signed package (fw/pkg) from the store app is
// checked, shown to the user with its permissions in a system dialog, and
// unpacked under StateDir/packages/<id>. Installed apps are never system
// apps: they cannot hold system-only permissions or preset grants, run only
// their own code, and cannot take the id of an app in the system image.

// systemOnly permissions are honoured only for apps in the system image.
var systemOnly = map[string]bool{ui.PermSystem: true, ui.PermInstall: true}

var appIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// installRecord sits next to an installed package.
type installRecord struct {
	Developer string    `json:"developer"`
	DevPub    string    `json:"dev_pub"`
	Store     string    `json:"store"`
	Version   string    `json:"version"`
	At        time.Time `json:"installed_at"`
}

const installRecordFile = ".install.json"

func (c *Core) packagesDir() string { return filepath.Join(c.cfg.StateDir, "packages") }

// storeRoots are the stores whose developer certificates the device trusts:
// the image's (share/phone/trust), the ones provisioned at the factory
// (StateDir/factory/stores) and, in developer mode, StateDir/trust.
func (c *Core) storeRoots() map[string]ed25519.PublicKey {
	dirs := []string{filepath.Join(filepath.Dir(c.cfg.AppsDir), "trust"), filepath.Join(c.cfg.StateDir, "factory", "stores")}
	if c.settings.DeveloperMode {
		dirs = append(dirs, filepath.Join(c.cfg.StateDir, "trust"))
	}
	return pkg.LoadRoots(dirs...)
}

// verifyInstalled checks an installed app's files against its signature
// before its first start after boot (and after developer mode changed): a
// file changed or planted since installation, or a store no longer trusted,
// keeps it from running.
func (c *Core) verifyInstalled(a *App) error {
	if rv, ok := c.revokedApp(a); ok {
		c.log.Printf("%s: not started: taken down by the store: %s", a.M.ID, rv.Reason)
		return errors.New(tr.T("Taken down by the store: %s", rv.Reason))
	}
	if !a.M.installed || a.verified {
		return nil
	}
	err := pkg.VerifyDir(a.M.dir, c.storeRoots(), installRecordFile)
	switch {
	case errors.Is(err, pkg.ErrUntrustedStore):
		c.log.Printf("%s: not started: %v", a.M.ID, err)
		return errors.New(tr.T("Turn on developer mode first"))
	case err != nil:
		c.log.Printf("%s: not started: installed files fail verification: %v", a.M.ID, err)
		return errors.New(tr.T("The package failed verification"))
	}
	a.verified = true
	return nil
}

// knownPerms are the permissions a package may declare.
func knownPerms() map[string]bool {
	out := map[string]bool{ui.PermVoIP: true, ui.PermNet: true, ui.PermNotify: true}
	for _, ps := range capPerms {
		for _, p := range ps {
			out[p] = true
		}
	}
	for p := range ui.RuntimePerms {
		out[p] = true
	}
	return out
}

// checkPackage applies the installer's policy to a verified package.
func (c *Core) checkPackage(p *pkg.Package) (*Manifest, error) {
	m := &Manifest{}
	if err := json.Unmarshal(p.Manifest, m); err != nil {
		return nil, err
	}
	switch {
	case !appIDPattern.MatchString(m.ID):
		return nil, fmt.Errorf("app id %q is not a reverse domain name", m.ID)
	case strings.HasPrefix(m.ID, "system."):
		return nil, fmt.Errorf("app id %q: system. is the system's own (e.g. %s signs update reports)", m.ID, ota.ReportApp)
	case m.Name == "" || m.Exec == "" && m.Runtime == "":
		return nil, errors.New("the manifest needs a name and exec")
	case len(m.Granted) > 0:
		return nil, errors.New(`"granted" is for the system image only`)
	case len(m.Commands) > maxCommands:
		return nil, fmt.Errorf("at most %d commands", maxCommands)
	}
	if len(m.Intents) > maxIntents {
		return nil, fmt.Errorf("at most %d intents", maxIntents)
	}
	for _, d := range m.Intents {
		if !intentAction.MatchString(d.Action) || d.Label == "" || len([]rune(d.Label)) > 8 {
			return nil, fmt.Errorf("intent %q: an action like pay or share.text and a label of 1–8 characters", d.Action)
		}
	}
	for _, cmd := range m.Commands {
		if cmd.Label == "" || len([]rune(cmd.Label)) > 16 || len(cmd.Arg) > 256 || reservedTitle(cmd.Label) {
			return nil, fmt.Errorf("command %q: a label of 1–16 characters (not a system one) and a short deep link", cmd.ID)
		}
	}
	known := knownPerms()
	for _, perm := range m.Permissions {
		if systemOnly[perm] {
			return nil, fmt.Errorf("permission %q is for system apps only", perm)
		}
		if !known[perm] {
			return nil, fmt.Errorf("unknown permission %q", perm)
		}
		if _, runtime := ui.RuntimePerms[perm]; runtime && strings.TrimSpace(m.Purposes[perm]) == "" {
			return nil, fmt.Errorf("permission %q needs a purpose: why the app asks for it (purposes)", perm)
		}
	}
	for perm, why := range m.Purposes {
		if !slices.Contains(m.Permissions, perm) {
			return nil, fmt.Errorf("a purpose for %q, which the app does not declare", perm)
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(why)); n == 0 || n > ui.MaxPurpose {
			return nil, fmt.Errorf("the purpose for %q is 1–%d characters", perm, ui.MaxPurpose)
		}
	}
	if err := i18n.CheckAppText(m.Lang, m.Locales, m.textRules()); err != nil {
		return nil, err
	}
	if err := ui.CheckNFCCards(m.NFC, m.Permissions, offeredLang); err != nil {
		return nil, err
	}
	var label struct {
		Privacy *ui.Privacy `json:"privacy"`
	}
	if err := json.Unmarshal(p.Manifest, &label); err != nil {
		return nil, err
	}
	if err := ui.CheckPrivacy(label.Privacy, m.Permissions); err != nil {
		return nil, err
	}
	m.localize(tr.Lang())
	if err := checkRuntime(m, p.Files); err != nil {
		return nil, err
	}
	exe := filepath.Base(m.Exec)
	found := m.Runtime != ""
	for _, f := range p.Files {
		if f.Path == exe && f.Mode == 0o755 {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("exec %q is not an executable file at the package root", m.Exec)
	}
	if old := c.appByID(m.ID); old != nil {
		if !old.M.installed {
			return nil, fmt.Errorf("%s is a system app", m.ID)
		}
		if rec := readInstallRecord(old.M.dir); rec.DevPub != p.Developer.Pub {
			return nil, fmt.Errorf("an update must be signed by the same developer (%s)", rec.Developer)
		}
	}
	return m, nil
}

func readInstallRecord(dir string) installRecord {
	var rec installRecord
	if raw, err := os.ReadFile(filepath.Join(dir, installRecordFile)); err == nil {
		_ = json.Unmarshal(raw, &rec)
	}
	return rec
}

// appFile resolves a path inside an app's data directory.
func (c *Core) appFile(a *App, rel string) string {
	return filepath.Join(c.cfg.StateDir, "apps", a.M.ID, filepath.Clean("/"+rel))
}

// storeLabel names who vouched for the developer; the development store is
// developer mode, so the user is told it was not reviewed.
func storeLabel(store string) string {
	switch store {
	case "dev":
		return tr.T("Development store (developer mode, not reviewed)")
	case "store":
		return tr.T("App store (reviewed)")
	}
	return tr.T("%s store", store)
}

func permLabels(perms []string, purposes map[string]string) string {
	var out []string
	for _, p := range perms {
		if _, ok := ui.RuntimePerms[p]; ok && purposes[p] != "" {
			out = append(out, tr.T("%s (asked when used: %s)", permLabel(p), purposes[p]))
		} else if ok {
			out = append(out, tr.T("%s (asked when used)", permLabel(p)))
		} else if _, ok := ui.DeclaredPerms[p]; ok {
			out = append(out, permLabel(p))
		} else {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return tr.T("None")
	}
	return strings.Join(out, tr.T(", "))
}

func (c *Core) installPkg(a *App, args ui.PkgArgs, reply func(any, error)) {
	path := c.appFile(a, args.File)
	st, err := os.Stat(path)
	if err != nil || st.Size() > pkg.MaxSize {
		reply(nil, fmt.Errorf("no package at %s", args.File))
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		reply(nil, err)
		return
	}
	note := ""
	if pkg.IsDelta(raw) {
		// The installed copy fills in what the delta leaves out; the result
		// is verified like any package.
		id, from, _, derr := pkg.DeltaInfo(raw)
		old := c.appByID(id)
		if derr != nil || old == nil || !old.M.installed || readInstallRecord(old.M.dir).Version != from {
			reply(nil, fmt.Errorf("the delta updates %s %s, which is not installed", id, from))
			return
		}
		full, derr := pkg.ApplyDelta(raw, old.M.dir)
		if derr != nil {
			c.log.Printf("%s: delta for %s refused: %v", a.M.ID, id, derr)
			reply(nil, derr)
			return
		}
		note = tr.T(" (delta, %s)", sizeLabel(int64(len(raw))))
		raw = full
	}
	if sum := sha256.Sum256(raw); args.SHA256 != "" && hex.EncodeToString(sum[:]) != args.SHA256 {
		reply(nil, errors.New(tr.T("The package does not match the store's catalogue")))
		return
	}
	p, err := pkg.Open(raw, c.storeRoots())
	if err == nil {
		var m *Manifest
		if m, err = c.checkPackage(p); err == nil && !args.Auto {
			c.confirmInstall(a, p, m, note, reply)
			return
		}
		if err == nil {
			if err = c.autoUpdate(a, p, m); err == nil {
				reply(ui.PkgInfo{ID: m.ID, Name: m.Name, Version: p.Version, Developer: p.Developer.Name, Store: p.Developer.Store}, nil)
				return
			}
		}
	}
	c.log.Printf("%s: install %s refused: %v", a.M.ID, args.File, err)
	if errors.Is(err, pkg.ErrUntrustedStore) {
		err = errors.New(tr.T("Turn on developer mode first"))
	}
	reply(nil, err)
}

// autoUpdate installs, without asking, an update that changes nothing the
// user agreed to: of an installed app (checkPackage has made sure it is the
// same developer's), a newer version (an old signed one cannot be slipped
// back in), no permission the installed one lacks. One on the screen, in a
// call or working in the background waits for the user; a frozen or idle one
// is ended and comes back as the new version.
func (c *Core) autoUpdate(a *App, p *pkg.Package, m *Manifest) error {
	old := c.appByID(m.ID)
	switch {
	case old == nil || !old.M.installed:
		return errors.New(ui.ErrNeedsUser)
	case !versionNewer(p.Version, readInstallRecord(old.M.dir).Version):
		return fmt.Errorf("%s %s is not newer than the installed one", m.ID, p.Version)
	case c.fg == old || c.inCall(old) || c.runsInBackground(old):
		return errors.New(ui.ErrNeedsUser)
	}
	for _, perm := range m.Permissions {
		if !slices.Contains(old.M.Permissions, perm) {
			return errors.New(ui.ErrNeedsUser)
		}
	}
	if err := c.unpack(p, m, old); err != nil {
		return err
	}
	c.log.Printf("%s: updated %s to %s in the background", a.M.ID, m.ID, p.Version)
	return nil
}

func (c *Core) confirmInstall(a *App, p *pkg.Package, m *Manifest, note string, reply func(any, error)) {
	old := c.appByID(m.ID)
	ask, done := tr.T("Install “%s” %s%s?\nDeveloper: %s\nSource: %s\nPermissions: %s"), tr.T("Installed “%s”")
	if old != nil {
		ask, done = tr.T("Update “%s” %s%s?\nDeveloper: %s\nSource: %s\nPermissions: %s"), tr.T("Updated “%s”")
	}
	text := fmt.Sprintf(ask, m.Name, p.Version, note, p.Developer.Name, storeLabel(p.Developer.Store), permLabels(m.Permissions, m.Purposes))
	c.pushOverlay(&confirmOverlay{text: text,
		onYes: func() {
			if err := c.unpack(p, m, old); err != nil {
				c.log.Printf("install %s: %v", m.ID, err)
				reply(nil, err)
				return
			}
			c.log.Printf("%s: installed %s %s from developer %s", a.M.ID, m.ID, p.Version, p.Developer.Name)
			c.showToast(fmt.Sprintf(done, m.Name))
			reply(ui.PkgInfo{ID: m.ID, Name: m.Name, Version: p.Version, Developer: p.Developer.Name, Store: p.Developer.Store}, nil)
		},
		onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// unpack replaces the app's code atomically: a new directory, then a rename.
func (c *Core) unpack(p *pkg.Package, m *Manifest, old *App) error {
	if err := os.MkdirAll(c.packagesDir(), 0o755); err != nil {
		return err
	}
	final := filepath.Join(c.packagesDir(), m.ID)
	tmp := final + fmt.Sprintf(".new-%d", time.Now().UnixNano())
	if err := p.Extract(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	rec, _ := json.MarshalIndent(installRecord{Developer: p.Developer.Name, DevPub: p.Developer.Pub,
		Store: p.Developer.Store, Version: p.Version, At: time.Now()}, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, installRecordFile), rec, 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if old != nil {
		c.kill(old)
	}
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	m.dir, m.installed = final, true
	if old != nil {
		old.M, old.verified, old.tomb = m, false, nil
	} else {
		c.apps = append(c.apps, &App{M: m})
		c.appUse().Opened[m.ID] = c.now().UnixMilli()
		c.saveUse()
		sort.SliceStable(c.apps, func(i, j int) bool { return c.apps[i].M.Order < c.apps[j].M.Order })
	}
	c.refreshSystemScreens()
	return nil
}

func (c *Core) uninstallPkg(a *App, id string, reply func(any, error)) {
	t := c.appByID(id)
	switch {
	case t == nil:
		reply(nil, fmt.Errorf("%s is not installed", id))
		return
	case !t.M.installed:
		reply(nil, fmt.Errorf("%s is a system app", id))
		return
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("Uninstall “%s”? Its data is deleted with it.", t.M.Name),
		onYes: func() {
			c.removeApp(t)
			c.log.Printf("%s: uninstalled %s", a.M.ID, id)
			c.showToast(tr.T("Uninstalled “%s”", t.M.Name))
			reply(map[string]bool{"uninstalled": true}, nil)
		},
		onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// removeApp deletes an installed app and everything the system kept for it.
func (c *Core) removeApp(t *App) {
	c.kill(t)
	id := t.M.ID
	_ = os.RemoveAll(t.M.dir)
	_ = os.RemoveAll(filepath.Join(c.cfg.StateDir, "apps", id))
	delete(c.broker.perms, id)
	for key := range c.broker.perms {
		if strings.HasPrefix(key, id+"/") {
			delete(c.broker.perms, key)
		}
	}
	c.broker.save()
	c.broker.forgetUsage(id)
	c.harvestTraffic()
	c.forgetTraffic(id)
	for i := 0; i < len(c.jobs); i++ {
		if c.jobs[i].App == id {
			c.jobs = append(c.jobs[:i], c.jobs[i+1:]...)
			i--
		}
	}
	c.saveJobs()
	c.alarms.removeApp(id)
	c.media.revokeApp(id)
	c.broker.call(svc.Keystored, svc.KeyForget, map[string]string{"app": id}, nil, func(err error) {
		if err != nil {
			c.log.Printf("uninstall %s: keys not deleted: %v", id, err)
		}
	})
	if c.pushUp {
		c.broker.call(svc.Pushd, svc.PushUnregister, map[string]string{"app": id}, nil, func(err error) {
			if err != nil {
				c.log.Printf("uninstall %s: push tokens not revoked: %v", id, err)
			}
		})
	}
	if c.fg == t {
		c.setForeground(nil)
	}
	c.removeMRU(t)
	for i, x := range c.apps {
		if x == t {
			c.apps = append(c.apps[:i], c.apps[i+1:]...)
			break
		}
	}
	c.refreshSystemScreens()
}

func (c *Core) listPkgs() []ui.PkgInfo {
	out := []ui.PkgInfo{}
	for _, a := range c.apps {
		if !a.M.installed {
			continue
		}
		rec := readInstallRecord(a.M.dir)
		out = append(out, ui.PkgInfo{ID: a.M.ID, Name: a.M.Name, Version: rec.Version, Developer: rec.Developer,
			Store: rec.Store, Permissions: a.M.Permissions})
	}
	return out
}

// refreshSystemScreens redraws the system's own screens (the main menu
// lists the apps) after the set of apps changed.
func (c *Core) refreshSystemScreens() {
	for _, v := range c.sys.stack {
		if systemRebuilt(v.S.ID) {
			v.replace(c.systemScreen(v.S.ID))
		}
	}
}

// buildExample signs a sample app with the development store (the selftest
// standing in for a developer and the store backend) and puts the package
// into an app's data dir, as if downloaded.
func (c *Core) buildExample(name, appID, file string, tamper bool) error {
	return c.signExample(name, appID, file, tamper, filepath.Join(c.cfg.StateDir, "devstore"), filepath.Join(c.cfg.StateDir, "trust"))
}

// buildMiniProgram does the same for a sample mini-program, signed by a
// development mini-program store whose root the system puts in the host's
// trust/ (a host ships its vendor's root in its code).
func (c *Core) buildMiniProgram(name, hostID, file string, tamper bool) error {
	trust := filepath.Join(c.cfg.StateDir, "apps", hostID, "trust")
	return c.signExample(name, hostID, file, tamper, filepath.Join(c.cfg.StateDir, "mpstore"), trust)
}

func (c *Core) signExample(name, appID, file string, tamper bool, storeDir, trustDir string) error {
	a := c.appByID(appID)
	if a == nil {
		return fmt.Errorf("no app %s", appID)
	}
	store, err := pkg.DevStore(storeDir, trustDir)
	if err != nil {
		return err
	}
	signer, err := pkg.DevDeveloper(filepath.Join(storeDir, "developers"), exampleDeveloper, store)
	if err != nil {
		return err
	}
	raw, err := pkg.Build(filepath.Join(filepath.Dir(c.cfg.AppsDir), "examples", name), signer)
	if err != nil {
		return err
	}
	if tamper {
		raw[len(raw)/2] ^= 0x20
	}
	dst := c.appFile(a, file)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0o644)
}

// Takedowns: the store app hands over the store's signed revocation list;
// the system keeps the newest it has verified and refuses to start the
// installed apps on it (and ends them if they run).

type revocations struct {
	Issued  int64         `json:"issued"`
	Revoked []pkg.Revoked `json:"revoked"`
}

func (c *Core) revocationsPath() string {
	return filepath.Join(c.cfg.StateDir, "shell", "revocations.json")
}

func (c *Core) loadRevocations() revocations {
	var r revocations
	if raw, err := os.ReadFile(c.revocationsPath()); err == nil {
		_ = json.Unmarshal(raw, &r)
	}
	return r
}

// Revoked: the device holds the store's takedown of the app (selftest).
func (c *Core) Revoked(id string) bool {
	a := c.appByID(id)
	if a == nil {
		return false
	}
	_, ok := c.revokedApp(a)
	return ok
}

func (c *Core) revokedApp(a *App) (pkg.Revoked, bool) {
	if !a.M.installed {
		return pkg.Revoked{}, false
	}
	for _, rv := range c.loadRevocations().Revoked {
		if rv.App == a.M.ID {
			return rv, true
		}
	}
	return pkg.Revoked{}, false
}

// checkCatalog verifies a store's catalogue against the trusted stores and
// its age; one older than the last accepted from that store (an update
// hidden by replaying a previous list) is refused.
func (c *Core) checkCatalog(data string) (ui.CatalogRes, error) {
	cat, err := pkg.VerifyCatalog([]byte(data), c.storeRoots(), c.now())
	if err != nil {
		return ui.CatalogRes{}, err
	}
	path := filepath.Join(c.cfg.StateDir, "shell", "catalog-issued.json")
	seen := map[string]int64{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &seen)
	}
	if cat.Issued < seen[cat.Store] {
		return ui.CatalogRes{}, fmt.Errorf("the catalog is older than one already seen from %s", cat.Store)
	}
	seen[cat.Store] = cat.Issued
	raw, _ := json.Marshal(seen)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return ui.CatalogRes{}, err
	}
	bucket := c.updateBucket()
	apps, err := pkg.OfferedApps(cat.Apps, func(id string, percent int) bool { return pkg.InRollout(bucket, id, percent) })
	if err != nil {
		return ui.CatalogRes{}, err
	}
	return ui.CatalogRes{Store: cat.Store, Issued: cat.Issued, Apps: apps}, nil
}

// applyRevocations verifies a list against the trusted stores; an older one
// than kept (a replay from before a takedown) is ignored.
func (c *Core) applyRevocations(data string) (int, error) {
	r, err := pkg.VerifyRevocations([]byte(data), c.storeRoots())
	if err != nil {
		return 0, err
	}
	if r.Issued < c.loadRevocations().Issued {
		return 0, nil
	}
	raw, _ := json.Marshal(revocations{Issued: r.Issued, Revoked: r.Revoked})
	if err := os.WriteFile(c.revocationsPath(), raw, 0o600); err != nil {
		return 0, err
	}
	n := 0
	for _, a := range c.apps {
		if rv, ok := c.revokedApp(a); ok {
			n++
			if a.running {
				c.log.Printf("%s: taken down by the store (%s); ending it", a.M.ID, rv.Reason)
				c.kill(a)
			}
		}
	}
	c.refreshSystemScreens()
	return n, nil
}
