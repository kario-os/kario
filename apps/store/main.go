// The store: the catalogue of the store backend (PHONE_STORE_SERVER),
// installed apps, and signed packages put in its downloads/ directory
// (developer mode). Installing, updating and uninstalling all go through
// the system (pkg.*), which verifies the signature against the stores the
// device trusts and asks the user. Each app's page shows who publishes it
// (a company's credit code) and its APP filing number, and takes reports to
// the store's staff.
//
// In the background two jobs run: the takedown list every few hours, so a
// revoked app stops even if the store is never opened, and, on the charger
// and an unmetered network (Wi-Fi not metered; what the user downloads goes
// on any), the updates, which the system installs without asking when they change
// nothing the user agreed to (the rest stay "update available"); when the
// system stops that job (the phone left unmetered Wi-Fi), the downloads end
// and the job's next run takes them up. Which version this
// phone is offered of a staged update the system decides (pkg.catalog).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type entry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Developer string `json:"developer"`
	Summary   string `json:"summary"`
	Size      int    `json:"size"`
	SHA256    string `json:"sha256"`
	// Thinned: the package as this phone's architecture downloads it,
	// when it differs from the whole one (pkg.Thinned).
	Thinned map[string]pkg.Thinned `json:"thinned"`
	// Credit is the developer's unified social credit code when it is a
	// company; Filing the app's APP filing number.
	Credit      string   `json:"developer_credit"`
	Filing      string   `json:"filing"`
	Permissions []string `json:"permissions"`
	// Purposes: why it asks for each runtime permission; Privacy its
	// privacy label (none: not provided).
	Purposes map[string]string `json:"purposes"`
	Privacy  *ui.Privacy       `json:"privacy"`
	// Native: it brings native code (its manifest's "native").
	Native bool `json:"native"`
	// MinPlatform: the lowest platform level it runs on.
	MinPlatform int `json:"min_platform"`
	// Lang is the language of Name, Summary and Purposes; Locales them in
	// others.
	Lang    string `json:"lang"`
	Locales map[string]struct {
		Name     string            `json:"name"`
		Summary  string            `json:"summary"`
		Purposes map[string]string `json:"purposes"`
	} `json:"locales"`
	// listed: from the catalogue (an installed app's own entry knows
	// nothing of its permissions).
	listed bool
}

// localize puts the entry's text in the phone's language, through the same
// fallback chain as app manifests.
// The shapes the store gives an app's ID and version (appstore's safeID and
// safeVersion, the installer's app ID): they name the downloaded file and
// the request's path.
var (
	entryID      = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	entryVersion = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,9}){0,7}$`)
)

func listable(e entry) bool { return entryID.MatchString(e.ID) && entryVersion.MatchString(e.Version) }

// runs: the entry runs on a phone of this platform level (22-developer-api.md
// §2.2).
func runs(e entry, platform int) bool { return e.MinPlatform <= platform }

func catalogURL(server string, platform int) string {
	return server + "/v1/catalog?platform=" + strconv.Itoa(platform)
}

func (e *entry) localize() {
	texts := map[string]i18n.AppText{}
	for l, t := range e.Locales {
		texts[l] = i18n.AppText{Name: t.Name, Description: t.Summary, Purposes: t.Purposes}
	}
	name := i18n.PickText(tr.Lang(), e.Lang, texts, e.Name, func(t i18n.AppText) string { return t.Name })
	summary := i18n.PickText(tr.Lang(), e.Lang, texts, e.Summary, func(t i18n.AppText) string { return t.Description })
	e.Name, e.Summary = name, summary
	for perm, own := range e.Purposes {
		e.Purposes[perm] = i18n.PickText(tr.Lang(), e.Lang, texts, own, func(t i18n.AppText) string { return t.Purposes[perm] })
	}
}

// newer compares dotted numeric versions.
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// page is the store's main screen: the packages in downloads/, the
// installed apps and the catalogue.
type page struct {
	server, downloads string
	installed         []ui.PkgInfo
	catalog           []entry
	catalogErr        string
	// revoked are the store's takedowns (app → reason), as last fetched.
	revoked map[string]string
	// loaded: the installed apps and the catalogue have come in once.
	loaded bool
}

func (pg *page) version(id string) string {
	for _, p := range pg.installed {
		if p.ID == id {
			return p.Version
		}
	}
	return ""
}

func (pg *page) screen() ui.Screen {
	s := ui.Screen{ID: "main", Title: tr.T("App Store"), Actions: []ui.Action{{ID: "refresh", Label: tr.T("Refresh"), Key: "r"}}}
	s.Body = append(s.Body, sdk.Section(tr.T("Ready to install")))
	files, _ := filepath.Glob(filepath.Join(pg.downloads, "*.phoneapp"))
	sort.Strings(files)
	for _, f := range files {
		name := filepath.Base(f)
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "f:" + name, Icon: tr.T("P"), Title: name,
			Actions: []ui.Action{{ID: "install", Label: tr.T("Install"), Primary: true}}})
	}
	if len(files) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No packages")))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Installed")))
	for _, p := range pg.installed {
		n := ui.Node{Kind: ui.KindItem, ID: "p:" + p.ID, Icon: tr.T("A"), Title: p.Name,
			Subtitle: tr.T("Version %s · %s", p.Version, p.Developer),
			Actions:  []ui.Action{{ID: "uninstall", Label: tr.T("Uninstall"), Key: "d"}, {ID: "detail", Label: tr.T("Details"), Key: "i"}}}
		if why, ok := pg.revoked[p.ID]; ok {
			n.Trailing, n.Subtitle = tr.T("Taken down"), tr.T("Taken down by the store: %s", why)
		}
		s.Body = append(s.Body, n)
	}
	switch {
	case len(pg.installed) > 0:
	case !pg.loaded:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Loading…")))
	default:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No apps installed yet")))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Store catalogue")))
	for _, e := range pg.catalog {
		n := ui.Node{Kind: ui.KindItem, ID: "s:" + e.ID, Icon: tr.T("S"), Title: tr.T("Store · %s", e.Name),
			Subtitle: tr.T("Version %s · %s · %d KB", e.Version, e.Developer, (e.Size+1023)/1024)}
		switch have := pg.version(e.ID); {
		case have == "":
			n.Actions = []ui.Action{{ID: "get", Label: tr.T("Install"), Primary: true}}
		case newer(e.Version, have):
			n.Trailing = tr.T("Update available")
			n.Actions = []ui.Action{{ID: "get", Label: tr.T("Update to %s", e.Version), Primary: true}}
		default:
			n.Trailing = tr.T("Installed")
		}
		if len(n.Actions) > 0 {
			n.Actions = append(n.Actions, ui.Action{ID: "detail", Label: tr.T("Details"), Key: "i"})
		}
		s.Body = append(s.Body, n)
	}
	switch {
	case pg.server == "":
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No store backend configured")))
	case !pg.loaded:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Loading…")))
	case pg.catalogErr != "":
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, pg.catalogErr))
	case len(pg.catalog) == 0:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The catalogue is empty")))
	}
	return s
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	server := strings.TrimSuffix(os.Getenv(ui.EnvStoreServer), "/")
	cl := &http.Client{Timeout: 30 * time.Second}
	downloads := filepath.Join(app.DataDir, "downloads")
	pg := &page{server: server, downloads: downloads, revoked: map[string]string{}}
	// fetching: downloads in progress, so a second press does not start
	// another one.
	fetching := map[string]bool{}
	var background autoFetch
	// detail is an app's page: who publishes it, its filing number, what it
	// asks for, and a way to report it.
	detail := func(e entry) ui.Screen {
		s := ui.Screen{ID: "detail:" + e.ID, Title: e.Name, Actions: []ui.Action{{ID: "report", Label: tr.T("Report"), Key: "j"}}}
		line := func(k, v string) {
			if v != "" {
				s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Text: tr.T("%s: %s", k, v)})
			}
		}
		line(tr.T("Version"), e.Version)
		line(tr.T("Developer"), e.Developer)
		line(tr.T("Unified social credit code"), e.Credit)
		line(tr.T("APP filing number"), e.Filing)
		line(tr.T("Summary"), e.Summary)
		if e.Filing == "" {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("This app has no APP filing number")))
		}
		if e.listed {
			s.Body = append(s.Body, permissionNodes(e)...)
			s.Body = append(s.Body, nativeNodes(e)...)
			s.Body = append(s.Body, privacyNodes(e)...)
		}
		return s
	}
	reportScreen := func(e entry) ui.Screen {
		return ui.Screen{ID: "report:" + e.ID, Title: tr.T("Report \"%s\"", e.Name), Body: []ui.Node{
			sdk.Styled(ui.StyleDim, tr.T("Describe the problem with this app; the store's staff will handle it.")),
			{Kind: ui.KindInput, ID: "reason", Placeholder: tr.T("Reason for the report"), Submit: "send_report"},
		}}
	}
	find := func(id string) (entry, bool) {
		for _, c := range pg.catalog {
			if c.ID == id {
				return c, true
			}
		}
		return entry{}, false
	}
	// fetchCatalog runs off the dispatch goroutine: the store's signed
	// catalogue, which the system checks (pkg.catalog) before it is shown.
	// The store leaves out what this phone's platform runs no release of,
	// the system the rest it cannot run.
	fetchCatalog := func() (string, error) {
		if server == "" {
			return "", nil
		}
		res, err := cl.Get(catalogURL(server, app.Platform))
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return string(raw), err
	}
	// fetchRevocations gets the store's signed takedown list; the system
	// checks the signature, so the app only carries it.
	fetchRevocations := func() string {
		if server == "" {
			return ""
		}
		res, err := cl.Get(server + "/v1/revocations")
		if err != nil {
			return ""
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return string(raw)
	}
	// reload fetches the catalogue and the takedowns, then the installed
	// apps; then runs once all three are in.
	reload := func(show func(ui.Screen), then func()) {
		go func() {
			raw, cerr := fetchCatalog()
			rv := fetchRevocations()
			app.Post(func() {
				listed := func() {
					app.Go(ui.CapPkgList, nil, &pg.installed, func(error) {
						pg.loaded = true
						show(pg.screen())
						if then != nil {
							then()
						}
					})
				}
				pg.catalogErr = ""
				if cerr != nil {
					pg.catalog, pg.catalogErr = nil, tr.T("Cannot reach the store: %s", cerr.Error())
				}
				if raw != "" {
					var res ui.CatalogRes
					app.Go(ui.CapPkgCatalog, ui.CatalogArgs{Data: raw}, &res, func(err error) {
						var cat []entry
						if err == nil {
							err = json.Unmarshal(res.Apps, &cat)
						}
						if err != nil {
							pg.catalog, pg.catalogErr = nil, tr.T("The store catalogue failed verification: %s", err.Error())
						} else {
							cat = slices.DeleteFunc(cat, func(e entry) bool { return !listable(e) || !runs(e, app.Platform) })
							for i := range cat {
								cat[i].localize()
								cat[i].listed = true
								if t, ok := cat[i].Thinned[pkg.HostArch()]; ok {
									cat[i].Size, cat[i].SHA256 = t.Size, t.SHA256
								}
							}
							pg.catalog = cat
						}
						listed()
					})
				} else {
					listed()
				}
				var list struct {
					Revoked []struct{ App, Reason string } `json:"revoked"`
				}
				if rv != "" && json.Unmarshal([]byte(rv), &list) == nil {
					app.Go(ui.CapPkgRevocations, ui.RevocationArgs{Data: rv}, nil, func(err error) {
						if err != nil {
							log.Printf("revocations: %v", err)
							return
						}
						pg.revoked = map[string]string{}
						for _, r := range list.Revoked {
							pg.revoked[r.App] = r.Reason
						}
						app.Update(pg.screen())
					})
				}
			})
		}()
	}
	done := func(err error) {
		if err != nil && !ui.Cancelled(err) {
			log.Printf("store: %v", err)
			app.Toast(tr.T("Could not finish: %s", err.Error()))
		}
		reload(app.Update, nil)
	}
	// installed after a background update: the user hears of it, the
	// ones the system leaves to the user stay "update available".
	autoDone := func(e entry) func(error) {
		return func(err error) {
			switch {
			case err == nil:
				app.Notify(ui.Notification{ID: "updated:" + e.ID, Title: tr.T("Updated automatically"), Body: tr.T("\"%s\" was updated to %s", e.Name, e.Version)})
			case strings.HasSuffix(err.Error(), ui.ErrNeedsUser):
				log.Printf("%s %s: left for the user", e.ID, e.Version)
			default:
				log.Printf("%s %s: background update failed: %v", e.ID, e.Version, err)
			}
			reload(app.Update, nil)
		}
	}
	// download fetches a catalogue package (a delta from the installed
	// version when the store has one), and hands it to the system installer
	// with the catalogue's digest, which the whole package must have.
	download := func(e entry, auto bool) {
		key := e.ID + "@" + e.Version
		if fetching[key] {
			return
		}
		fetching[key] = true
		finish := done
		if auto {
			finish = autoDone(e)
		} else {
			app.Toast(tr.T("Downloading \"%s\"", e.Name))
		}
		from, ctx := pg.version(e.ID), background.context(auto)
		go func() {
			name := ".store-" + e.ID + "-" + e.Version + ".phoneapp"
			err := func() error {
				// Only this phone's architecture's libraries (the digest
				// is the catalogue's for it).
				url := server + "/v1/packages/" + e.ID + "/" + e.Version + "?arch=" + pkg.HostArch()
				if from != "" {
					url += "&from=" + from
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					return err
				}
				res, err := cl.Do(req)
				if err != nil {
					return err
				}
				defer res.Body.Close()
				if res.StatusCode != 200 {
					return errors.New(tr.T("The store answered %d", res.StatusCode))
				}
				raw, err := io.ReadAll(io.LimitReader(res.Body, int64(e.Size)+1))
				if err != nil {
					return err
				}
				if sum := sha256.Sum256(raw); res.Header.Get("X-Phoneapp-Delta") == "" && hex.EncodeToString(sum[:]) != e.SHA256 {
					return errors.New(tr.T("The downloaded package does not match the catalogue"))
				}
				// Into the app's own directory: downloads/ may hold packages
				// someone else put there.
				return os.WriteFile(filepath.Join(app.DataDir, name), raw, 0o600)
			}()
			app.Post(func() {
				delete(fetching, key)
				if err != nil {
					finish(err)
					return
				}
				app.Go(ui.CapPkgInstall, ui.PkgArgs{File: name, SHA256: e.SHA256, Auto: auto}, nil, func(err error) {
					_ = os.Remove(filepath.Join(app.DataDir, name))
					finish(err)
				})
			})
		}()
	}
	autoUpdate := func() {
		var v map[string]any
		app.Go(ui.CapAppSettings, nil, &v, func(error) {
			if on, ok := v["auto_update"].(bool); ok && !on {
				return
			}
			for _, e := range pg.catalog {
				if have := pg.version(e.ID); have != "" && newer(e.Version, have) && pg.revoked[e.ID] == "" {
					download(e, true)
				}
			}
		})
	}
	app.OnStart = func(string) {
		_ = os.MkdirAll(downloads, 0o700)
		app.Reset(pg.screen())
		reload(app.Update, nil)
		app.Go(ui.CapJobSchedule, ui.JobArgs{ID: "revocations", EveryMin: 6 * 60}, nil, func(error) {})
		app.Go(ui.CapJobSchedule, ui.JobArgs{ID: "updates", EveryMin: 6 * 60, Charging: true, Unmetered: true}, nil, func(error) {})
	}
	app.OnEvent = func(name, value string) {
		switch {
		case name == ui.EventJob && value == "revocations":
			reload(app.Update, nil)
		case name == ui.EventJob && value == "updates":
			background.start()
			reload(app.Update, autoUpdate)
		case name == ui.EventJobStop:
			var st ui.JobStop
			if json.Unmarshal([]byte(value), &st) == nil && st.ID == "updates" {
				log.Printf("background updates stopped: %s", st.Reason)
				background.stop()
			}
		}
	}
	app.OnLifecycle = func(state string) {
		if state == ui.StateForeground {
			reload(app.Update, nil)
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		switch e.Action {
		case "refresh":
			reload(app.Update, nil)
		case "install":
			file := "downloads/" + strings.TrimPrefix(e.Node, "f:")
			app.Go(ui.CapPkgInstall, ui.PkgArgs{File: file}, nil, done)
		case "uninstall":
			app.Go(ui.CapPkgUninstall, ui.PkgArgs{ID: strings.TrimPrefix(e.Node, "p:")}, nil, done)
		case "get":
			for _, c := range pg.catalog {
				if "s:"+c.ID == e.Node {
					download(c, false)
				}
			}
		case "detail":
			id := strings.TrimPrefix(strings.TrimPrefix(e.Node, "s:"), "p:")
			if c, ok := find(id); ok {
				app.Push(detail(c))
				return
			}
			for _, p := range pg.installed {
				if p.ID == id {
					app.Push(detail(entry{ID: p.ID, Name: p.Name, Version: p.Version, Developer: p.Developer}))
				}
			}
		case "report":
			if c, ok := find(strings.TrimPrefix(e.Screen, "detail:")); ok {
				app.Push(reportScreen(c))
			}
		case "send_report":
			id, reason := strings.TrimPrefix(e.Screen, "report:"), strings.TrimSpace(e.Value)
			if reason == "" {
				return
			}
			go func() {
				raw, _ := json.Marshal(map[string]string{"app": id, "reason": reason})
				res, err := cl.Post(server+"/v1/reports", "application/json", strings.NewReader(string(raw)))
				if err == nil {
					res.Body.Close()
					if res.StatusCode != 200 {
						err = errors.New(tr.T("The store refused the report (%d)", res.StatusCode))
					}
				}
				app.Post(func() {
					if err != nil {
						app.Toast(tr.T("Could not send: %s", err.Error()))
						return
					}
					app.Pop()
					app.Toast(tr.T("Report sent; the store will handle it"))
				})
			}()
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
