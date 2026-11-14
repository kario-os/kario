package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
	sdkts "github.com/kario-os/kario/sdk/ts"
	"github.com/kario-os/kario/ui"
)

type buildOptions struct {
	out      string
	bytecode bool
	jsrt     string
	minify   bool
	quiet    bool
	// sourcemap writes main.js.map beside main.js (to debug the TypeScript
	// under Node.js); inline puts the map inside main.js (kario test).
	sourcemap, inline bool
}

type buildResult struct {
	dir                  string
	main, bytecode, icon int
	locales              []string
	// noLabel: the app has network access and no privacy label.
	noLabel bool
}

func (r *buildResult) summary() string {
	s := fmt.Sprintf("%s: main.js %s", r.dir, kb(r.main))
	if r.bytecode > 0 {
		s += ", main.jsc " + kb(r.bytecode)
	}
	if len(r.locales) > 0 {
		s += ", locale " + strings.Join(r.locales, " ")
	}
	if r.noLabel {
		s += "\nno privacy label: the store will show \"not provided\" (manifest \"privacy\", 33-app-guidelines.md §3)"
	}
	return s
}

func kb(n int) string { return fmt.Sprintf("%.1f KB", float64(n)/1024) }

// manifest is what build checks in a project's manifest.json.
type manifest struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Runtime     string                  `json:"runtime"`
	Main        string                  `json:"main"`
	Exec        string                  `json:"exec"`
	IconImage   string                  `json:"icon_image"`
	Permissions []string                `json:"permissions"`
	Purposes    map[string]string       `json:"purposes"`
	Granted     []string                `json:"granted"`
	Lang        string                  `json:"lang"`
	Locales     map[string]i18n.AppText `json:"locales"`
	Settings    []struct {
		Key     string   `json:"key"`
		Choices []string `json:"choices"`
	} `json:"settings"`
	Commands []struct {
		ID string `json:"id"`
	} `json:"commands"`
	Intents []struct {
		Action string `json:"action"`
	} `json:"intents"`
	NotifyActions []struct {
		ID string `json:"id"`
	} `json:"notify_actions"`
	Privacy *ui.Privacy `json:"privacy"`
}

// The installer's pattern for app ids (shell/install.go).
var appID = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// check applies what the installer and the store's review apply to a
// manifest (22-developer-api.md §1, §12), so a build that passes installs.
func (m *manifest) check() error {
	switch {
	case !appID.MatchString(m.ID) || strings.HasPrefix(m.ID, "system."):
		return fmt.Errorf("id %q: a reverse domain name like com.example.notes", m.ID)
	case strings.TrimSpace(m.Name) == "":
		return errors.New("the app needs a name")
	case m.Runtime != jsrt.Runtime:
		return fmt.Errorf(`a TypeScript app's manifest says "runtime": %q`, jsrt.Runtime)
	case m.Main != "" && m.Main != jsrt.DefaultMain:
		return fmt.Errorf(`"main" is %q, the module kario build writes`, jsrt.DefaultMain)
	case m.Exec != "":
		return errors.New(`a TypeScript app has no "exec"`)
	case len(m.Granted) > 0:
		return errors.New(`"granted" is for the system image only`)
	}
	for _, p := range m.Permissions {
		_, runtime := ui.RuntimePerms[p]
		_, declared := ui.DeclaredPerms[p]
		if !runtime && !declared {
			return fmt.Errorf("permission %q: not one an app can declare", p)
		}
		if runtime && strings.TrimSpace(m.Purposes[p]) == "" {
			return fmt.Errorf("permission %q needs a purpose: why the app asks for it (purposes)", p)
		}
	}
	for p, why := range m.Purposes {
		if !slices.Contains(m.Permissions, p) {
			return fmt.Errorf("a purpose for %q, which the app does not declare", p)
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(why)); n == 0 || n > ui.MaxPurpose {
			return fmt.Errorf("the purpose for %q is 1–%d characters", p, ui.MaxPurpose)
		}
	}
	if err := ui.CheckPrivacy(m.Privacy, m.Permissions); err != nil {
		return err
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
	return i18n.CheckAppText(m.Lang, m.Locales, r)
}

func readManifest(dir string) (*manifest, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("manifest.json: %w", err)
	}
	if err := m.check(); err != nil {
		return nil, nil, fmt.Errorf("manifest.json: %w", err)
	}
	return &m, raw, nil
}

// sdkPlugin answers @kario/ui and @kario/test with the SDK this program
// carries (sdk/ts), so a project needs no package manager.
var sdkPlugin = api.Plugin{Name: "kario-sdk", Setup: func(b api.PluginBuild) {
	b.OnResolve(api.OnResolveOptions{Filter: `^@kario/(ui|test)$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
		return api.OnResolveResult{Path: a.Path, Namespace: "kario-sdk"}, nil
	})
	b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "kario-sdk"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
		raw, err := sdkts.Source(a.Path)
		src := string(raw)
		return api.OnLoadResult{Contents: &src, Loader: api.LoaderTS}, err
	})
}}

func esbuildError(msgs []api.Message) error {
	var out []string
	for _, e := range msgs {
		at := ""
		if e.Location != nil {
			at = fmt.Sprintf("%s:%d:%d: ", e.Location.File, e.Location.Line, e.Location.Column+1)
		}
		out = append(out, at+e.Text)
	}
	return errors.New(strings.Join(out, "\n"))
}

func sourceMap(o buildOptions) api.SourceMap {
	switch {
	case o.inline:
		return api.SourceMapInline
	case o.sourcemap:
		return api.SourceMapLinked
	}
	return api.SourceMapNone
}

// bundle compiles src/main.ts and what it imports into one ES2023 module
// (outFile, where its source map points from); "kario" stays an import,
// which kario-jsrt answers, @kario/ui is bundled in.
func bundle(dir, outFile string, o buildOptions) (code, srcMap []byte, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	entry := filepath.Join(abs, "src", "main.ts")
	if _, err := os.Stat(entry); err != nil {
		return nil, nil, fmt.Errorf("no src/main.ts in %s", dir)
	}
	if outFile, err = filepath.Abs(outFile); err != nil {
		return nil, nil, err
	}
	res := api.Build(api.BuildOptions{
		AbsWorkingDir:     abs,
		EntryPoints:       []string{entry},
		Outfile:           outFile,
		Bundle:            true,
		Write:             false,
		Format:            api.FormatESModule,
		Target:            api.ES2023,
		Platform:          api.PlatformNeutral,
		MainFields:        []string{"module", "main"},
		External:          []string{"kario"},
		Plugins:           []api.Plugin{sdkPlugin},
		Sourcemap:         sourceMap(o),
		Charset:           api.CharsetUTF8,
		LegalComments:     api.LegalCommentsEndOfFile,
		MinifyWhitespace:  o.minify,
		MinifySyntax:      o.minify,
		MinifyIdentifiers: o.minify,
		LogLevel:          api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return nil, nil, esbuildError(res.Errors)
	}
	for _, f := range res.OutputFiles {
		switch filepath.Base(f.Path) {
		case jsrt.DefaultMain:
			code = f.Contents
		case jsrt.DefaultMain + ".map":
			srcMap = f.Contents
		}
	}
	if code == nil {
		return nil, nil, errors.New("esbuild wrote no main.js")
	}
	return code, srcMap, nil
}

// localeDir is where the project keeps its catalogs: src/locale beside the
// code (where scripts/i18n-check.py looks), or locale/ at the top.
func localeDir(dir string) (string, error) {
	var found []string
	for _, d := range []string{filepath.Join(dir, "src", "locale"), filepath.Join(dir, "locale")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", errors.New("translations in both src/locale and locale: keep one")
}

// copyLocales copies <language>.json catalogs (objects of strings) into
// out/locale.
func copyLocales(dir, out string) ([]string, error) {
	src, err := localeDir(dir)
	if err != nil || src == "" {
		return nil, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	var langs []string
	for _, e := range entries {
		lang, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		offered := false
		for _, l := range i18n.Languages() {
			offered = offered || l.ID == lang
		}
		if !offered {
			return nil, fmt.Errorf("%s: %s is not a language the system offers", e.Name(), lang)
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return nil, err
		}
		var msgs map[string]string
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return nil, fmt.Errorf("%s: an object of English messages and their translations: %w", e.Name(), err)
		}
		if err := os.MkdirAll(filepath.Join(out, "locale"), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(out, "locale", e.Name()), raw, 0o644); err != nil {
			return nil, err
		}
		langs = append(langs, lang)
	}
	return langs, nil
}

// copyIcon copies the manifest's icon_image (a PNG of at most 512 KB and
// 256×256, as the Shell draws it).
func copyIcon(dir, out, name string) (int, error) {
	if name == "" {
		return 0, nil
	}
	if filepath.Base(name) != name {
		return 0, fmt.Errorf("icon_image %q: a file beside manifest.json", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || len(raw) > 512<<10 || cfg.Width > 256 || cfg.Height > 256 {
		return 0, fmt.Errorf("icon_image %s: a PNG of at most 512 KB and 256×256", name)
	}
	return len(raw), os.WriteFile(filepath.Join(out, name), raw, 0o644)
}

// findJSRT is kario-jsrt for -bytecode: the flag's, $KARIO_JSRT, beside
// this program, in the staged layout (bin/../lib/phone/bin), or on PATH.
func findJSRT(flagPath string) (string, error) {
	cands := []string{flagPath, os.Getenv("KARIO_JSRT")}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		cands = append(cands, filepath.Join(d, jsrt.Program), filepath.Join(d, "..", "lib", "phone", "bin", jsrt.Program))
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return c, nil
		}
		if c == flagPath {
			return "", fmt.Errorf("-jsrt %s: not an executable", c)
		}
	}
	if p, err := exec.LookPath(jsrt.Program); err == nil {
		return p, nil
	}
	return "", errors.New("-bytecode needs kario-jsrt (make jsrt, then -jsrt build/jsrt/kario-jsrt)")
}

// prepareOut empties out for a build: a directory that does not exist yet,
// an empty one, or an earlier build (manifest.json and main.js); anything
// else is not overwritten.
func prepareOut(out string) error {
	entries, err := os.ReadDir(out)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case len(entries) > 0:
		_, e1 := os.Stat(filepath.Join(out, "manifest.json"))
		_, e2 := os.Stat(filepath.Join(out, jsrt.DefaultMain))
		if e1 != nil || e2 != nil {
			return fmt.Errorf("%s is not an earlier build: not overwriting it", out)
		}
		if err := os.RemoveAll(out); err != nil {
			return err
		}
	}
	return os.MkdirAll(out, 0o755)
}

func build(dir string, o buildOptions) (*buildResult, error) {
	m, raw, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	out := o.out
	if out == "" {
		out = filepath.Join(dir, "build")
	}
	code, srcMap, err := bundle(dir, filepath.Join(out, jsrt.DefaultMain), o)
	if err != nil {
		return nil, err
	}
	var compiler string
	if o.bytecode {
		if compiler, err = findJSRT(o.jsrt); err != nil {
			return nil, err
		}
	}
	if err := prepareOut(out); err != nil {
		return nil, err
	}
	r := &buildResult{dir: out, main: len(code), noLabel: m.Privacy == nil && slices.Contains(m.Permissions, ui.PermNet)}
	main := filepath.Join(out, jsrt.DefaultMain)
	if err := os.WriteFile(main, code, 0o644); err != nil {
		return nil, err
	}
	if srcMap != nil {
		if err := os.WriteFile(main+".map", srcMap, 0o644); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), raw, 0o644); err != nil {
		return nil, err
	}
	if r.locales, err = copyLocales(dir, out); err != nil {
		return nil, err
	}
	if r.icon, err = copyIcon(dir, out, m.IconImage); err != nil {
		return nil, err
	}
	if err := checkFiles(out, o.bytecode); err != nil {
		return nil, err
	}
	if compiler != "" {
		bc := jsrt.Bytecode(main)
		if msg, err := exec.Command(compiler, "--compile", main, bc).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("bytecode: %v: %s", err, strings.TrimSpace(string(msg)))
		}
		if err := os.Chmod(bc, 0o644); err != nil {
			return nil, err
		}
		st, err := os.Stat(bc)
		if err != nil {
			return nil, err
		}
		r.bytecode = int(st.Size())
	}
	return r, nil
}

// checkFiles applies jsrt.Check to an app directory as a package of it
// would carry it (bytecode aside when it is the system's own build).
func checkFiles(dir string, bytecode bool) error {
	var files []pkg.File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if bytecode && strings.HasSuffix(rel, jsrt.BytecodeExt) {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		mode := uint32(0o644)
		if st.Mode()&0o111 != 0 {
			mode = 0o755
		}
		files = append(files, pkg.File{Path: rel, Mode: mode})
		return nil
	})
	if err != nil {
		return err
	}
	m, _, err := readManifest(dir)
	if err != nil {
		return err
	}
	return jsrt.Check(m.Runtime, m.Main, m.Exec, files)
}
