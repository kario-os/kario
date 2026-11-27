package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/ui"
)

// A manifest's problems, by the field they are about ("permissions.camera",
// "purposes.location", "privacy", ""), so that Kario Studio's manifest
// editor shows each beside its field. kario build, the installer and the
// store's review apply the same rules; the editor asks kario manifest check
// rather than having its own.
type problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Icons the Shell draws: a PNG beside manifest.json of at most this size.
const (
	maxIconBytes = 512 << 10
	maxIconSide  = 256
)

func (m *manifest) problems() []problem {
	var out []problem
	add := func(field, format string, args ...any) {
		out = append(out, problem{field, fmt.Sprintf(format, args...)})
	}
	if !appID.MatchString(m.ID) || strings.HasPrefix(m.ID, "system.") {
		add("id", "id %q: a reverse domain name like com.example.notes", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		add("name", "the app needs a name")
	}
	if m.Runtime != jsrt.Runtime {
		add("runtime", `a TypeScript app's manifest says "runtime": %q`, jsrt.Runtime)
	}
	if m.Main != "" && m.Main != jsrt.DefaultMain {
		add("main", `"main" is %q, the module kario build writes`, jsrt.DefaultMain)
	}
	if m.Exec != "" {
		add("exec", `a TypeScript app has no "exec"`)
	}
	if len(m.Granted) > 0 {
		add("granted", `"granted" is for the system image only`)
	}
	if m.MinPlatform < 0 || m.MinPlatform > ui.PlatformLevel {
		add("min_platform", "min_platform %d: a platform level up to %d, the newest this SDK knows", m.MinPlatform, ui.PlatformLevel)
	}
	if err := ui.CheckCapabilities(m.Capabilities, m.MinPlatform); err != nil {
		add("capabilities", "%v", err)
	}
	if err := jsrt.CheckNativeNames(m.Native); err != nil {
		add("native", "%v", err)
	}
	for _, p := range m.Permissions {
		_, runtime := ui.RuntimePerms[p]
		_, declared := ui.DeclaredPerms[p]
		if !runtime && !declared {
			add("permissions."+p, "permission %q: not one an app can declare", p)
		}
		if runtime && strings.TrimSpace(m.Purposes[p]) == "" {
			add("purposes."+p, "permission %q needs a purpose: why the app asks for it (purposes)", p)
		}
	}
	if p, q := ui.MissingPerm(m.Permissions); p != "" {
		add("permissions."+p, "permission %q needs %q too", p, q)
	}
	purposes := make([]string, 0, len(m.Purposes))
	for p := range m.Purposes {
		purposes = append(purposes, p)
	}
	sort.Strings(purposes)
	for _, p := range purposes {
		if !slices.Contains(m.Permissions, p) {
			add("purposes."+p, "a purpose for %q, which the app does not declare", p)
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(m.Purposes[p])); n == 0 || n > ui.MaxPurpose {
			add("purposes."+p, "the purpose for %q is 1–%d characters", p, ui.MaxPurpose)
		}
	}
	if err := ui.CheckPrivacy(m.Privacy, m.Permissions); err != nil {
		add("privacy", "%v", err)
	}
	r := i18n.DefaultAppTextRules()
	r.MaxPurpose = ui.MaxPurpose
	r.Settings = map[string]int{}
	for _, s := range m.Settings {
		r.Settings[s.Key] = len(s.Choices)
	}
	r.Permissions = m.Permissions
	for _, c := range m.Commands {
		r.Commands = append(r.Commands, c.ID)
	}
	for _, d := range m.Intents {
		r.Intents = append(r.Intents, d.Action)
	}
	for _, a := range m.NotifyActions {
		r.NotifyActions = append(r.NotifyActions, a.ID)
	}
	if err := i18n.CheckAppText(m.Lang, m.Locales, r); err != nil {
		field := "locales"
		if strings.HasPrefix(err.Error(), "lang ") {
			field = "lang"
		}
		add(field, "%v", err)
	}
	return out
}

// iconProblem is what build would say of the manifest's icon_image in dir.
func iconProblem(dir, name string) *problem {
	if name == "" {
		return nil
	}
	if filepath.Base(name) != name {
		return &problem{"icon_image", fmt.Sprintf("icon_image %q: a file beside manifest.json", name)}
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return &problem{"icon_image", fmt.Sprintf("icon_image %s: no such file beside manifest.json", name)}
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || len(raw) > maxIconBytes || cfg.Width > maxIconSide || cfg.Height > maxIconSide {
		return &problem{"icon_image", fmt.Sprintf("icon_image %s: a PNG of at most %d KB and %d×%d", name, maxIconBytes>>10, maxIconSide, maxIconSide)}
	}
	return nil
}

// permInfo is a permission an app can declare, as the editor offers it.
type permInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Runtime: the user is asked on first use, and the manifest says why
	// (purposes); the others are granted with the installation.
	Runtime bool `json:"runtime"`
	// Data is the kind of data it opens, which a privacy label of an app
	// with network access has to account for.
	Data string `json:"data,omitempty"`
}

// manifestSchema is what the manifest editor lays its form out from.
type manifestSchema struct {
	Runtime     string            `json:"runtime"`
	Main        string            `json:"main"`
	Permissions []permInfo        `json:"permissions"`
	MaxPurpose  int               `json:"max_purpose"`
	PrivacyData []string          `json:"privacy_data"`
	PrivacyUses []string          `json:"privacy_uses"`
	NetPerm     string            `json:"net_permission"`
	Languages   []string          `json:"languages"`
	Limits      i18n.AppTextRules `json:"limits"`
	// PlatformLevel is the newest min_platform the SDK knows.
	PlatformLevel int `json:"platform_level"`
}

func schema() manifestSchema {
	s := manifestSchema{Runtime: jsrt.Runtime, Main: jsrt.DefaultMain, MaxPurpose: ui.MaxPurpose, PrivacyData: ui.PrivacyData,
		PrivacyUses: ui.PrivacyUses, NetPerm: ui.PermNet, Limits: i18n.DefaultAppTextRules(), PlatformLevel: ui.PlatformLevel}
	s.Limits.MaxPurpose = ui.MaxPurpose
	for name, label := range ui.RuntimePerms {
		s.Permissions = append(s.Permissions, permInfo{Name: name, Label: label, Runtime: true, Data: ui.PermData[name]})
	}
	for name, label := range ui.DeclaredPerms {
		s.Permissions = append(s.Permissions, permInfo{Name: name, Label: label})
	}
	sort.Slice(s.Permissions, func(i, j int) bool {
		a, b := s.Permissions[i], s.Permissions[j]
		if a.Runtime != b.Runtime {
			return a.Runtime
		}
		return a.Name < b.Name
	})
	for _, l := range i18n.Languages() {
		s.Languages = append(s.Languages, l.ID)
	}
	return s
}

// manifestReport is kario manifest check -json.
type manifestReport struct {
	OK       bool           `json:"ok"`
	Problems []problem      `json:"problems"`
	Schema   manifestSchema `json:"schema"`
}

// checkManifest checks a manifest: a project directory's, a manifest.json,
// or (path "-") the one on stdin, which has no directory to find its icon in.
func checkManifest(path string, stdin io.Reader) (manifestReport, error) {
	var raw []byte
	var err error
	dir := ""
	switch st, statErr := os.Stat(path); {
	case path == "-":
		raw, err = io.ReadAll(stdin)
	case statErr == nil && st.IsDir():
		dir = path
		raw, err = os.ReadFile(filepath.Join(path, "manifest.json"))
	default:
		dir = filepath.Dir(path)
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return manifestReport{}, err
	}
	rep := manifestReport{Problems: []problem{}, Schema: schema()}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		rep.Problems = append(rep.Problems, problem{"", "manifest.json: " + err.Error()})
		return rep, nil
	}
	rep.Problems = append(rep.Problems, m.problems()...)
	if dir != "" {
		if p := iconProblem(dir, m.IconImage); p != nil {
			rep.Problems = append(rep.Problems, *p)
		}
	}
	rep.OK = len(rep.Problems) == 0
	return rep, nil
}

// errSilent fails a command that has already said why.
var errSilent = errors.New("")

// manifestCommand is kario manifest check: the problems, one a line, or the
// report as JSON; it fails when there are any.
func manifestCommand(path string, asJSON bool, stdin io.Reader, stdout io.Writer) error {
	rep, err := checkManifest(path, stdin)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		for _, p := range rep.Problems {
			fmt.Fprintln(stdout, "manifest.json: "+p.Message)
		}
		if rep.OK {
			fmt.Fprintln(stdout, "manifest.json: OK")
		}
	}
	if !rep.OK {
		return errSilent
	}
	return nil
}
