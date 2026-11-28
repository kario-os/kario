package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/kario-os/kario/jsrt"
	sdkts "github.com/kario-os/kario/sdk/ts"
	"github.com/kario-os/kario/ui"
)

// kario preview (29-app-development.md §3, as SwiftUI's previews): the
// pages an app's src/previews.ts names with KarioUI's preview(), each
// evaluated with its sample state under Node.js without the app running,
// then drawn by the developer machine's Shell and phone-paint, headless, on
// each form factor or SKU asked for, as PNGs.

// previewsFile is where an app's previews are; kario build leaves it out.
const previewsFile = "previews.ts"

type previewOptions struct {
	out, lang, node, shell, gui, native string
	devices, skus                       []string
	skin                                bool
}

// previewImage is one picture kario preview drew.
type previewImage struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	File   string `json:"file"`
	Skin   string `json:"skin,omitempty"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type previewScreen struct {
	Name   string     `json:"name"`
	Screen *ui.Screen `json:"screen"`
	Error  string     `json:"error"`
}

// previewEntry is the module kario preview bundles: the app's code, its
// previews, and KarioUI's function that evaluates them.
func previewEntry() string {
	return `import "./src/main.ts";
import "./src/` + previewsFile + `";
export { renderPreviews } from "@kario/ui";
`
}

// bundlePreviews writes the previews' module to file.
func bundlePreviews(dir, file string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(abs, "src", previewsFile)); err != nil {
		return fmt.Errorf("no src/%s in %s: name the pages to preview there with preview() from @kario/ui", previewsFile, dir)
	}
	res := api.Build(api.BuildOptions{
		AbsWorkingDir: abs,
		Stdin:         &api.StdinOptions{Contents: previewEntry(), ResolveDir: abs, Sourcefile: "kario-preview.ts", Loader: api.LoaderTS},
		Outfile:       file,
		Bundle:        true,
		Write:         true,
		Format:        api.FormatESModule,
		Target:        api.ES2023,
		Platform:      api.PlatformNeutral,
		MainFields:    []string{"module", "main"},
		External:      []string{"kario", jsrt.NativeScheme + "*"},
		Plugins:       []api.Plugin{sdkPlugin},
		Charset:       api.CharsetUTF8,
		LogLevel:      api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return esbuildError(res.Errors)
	}
	return nil
}

// evalPreviews evaluates the previews under Node.js (sdk/ts/node/preview.mjs)
// against the built app in appDir.
func evalPreviews(node, rt, bundle, appDir, lang string) ([]previewScreen, error) {
	cmd := exec.Command(node, slices.Concat(sdkts.NodeFlags, []string{"--enable-source-maps", filepath.Join(rt, "preview.mjs"), bundle, appDir})...)
	cmd.Env = append(os.Environ(), "KARIO_PREVIEW_LANG="+lang)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var screens []previewScreen
	if err := json.Unmarshal(out, &screens); err != nil {
		return nil, fmt.Errorf("the previews' output: %w", err)
	}
	if len(screens) == 0 {
		return nil, fmt.Errorf("src/%s names no previews (preview() from @kario/ui)", previewsFile)
	}
	var bad []string
	for _, s := range screens {
		if s.Error != "" || s.Screen == nil {
			bad = append(bad, fmt.Sprintf("preview %q: %s", s.Name, s.Error))
		}
	}
	if len(bad) > 0 {
		return nil, errors.New(strings.Join(bad, "\n"))
	}
	return screens, nil
}

var notSlug = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// slugs are the previews' file names: their names in letters, digits and
// dashes, numbered where two would be the same.
func slugs(names []string) []string {
	seen := map[string]int{}
	out := make([]string, len(names))
	for i, n := range names {
		s := strings.Trim(notSlug.ReplaceAllString(n, "-"), "-.")
		if s == "" {
			s = "preview"
		}
		if seen[s]++; seen[s] > 1 {
			s = fmt.Sprintf("%s-%d", s, seen[s])
		}
		out[i] = s
	}
	return out
}

// previewTarget is a form factor or a SKU the pages are drawn on.
type previewTarget struct {
	name, device, skuFile string
}

func previewTargets(o previewOptions, cache string) ([]previewTarget, error) {
	var out []previewTarget
	for _, d := range o.devices {
		out = append(out, previewTarget{name: d, device: d})
	}
	for _, id := range o.skus {
		s, err := skuByID(id)
		if err != nil {
			return nil, err
		}
		p, err := writeSKU(s, filepath.Join(cache, "kario", "sku"))
		if err != nil {
			return nil, err
		}
		out = append(out, previewTarget{name: s.ID, skuFile: p})
	}
	if len(out) == 0 {
		for _, d := range []string{"bar-t9", "bar-qwerty", "communicator"} {
			out = append(out, previewTarget{name: d, device: d})
		}
	}
	return out, nil
}

// pagesArgs are phone-shell's arguments to draw the pages file on t.
func pagesArgs(app, pages, renderer, dir string, t previewTarget, skin bool) []string {
	args := []string{"--dev-app", app, "--render-pages", pages, "--gui", renderer, "--pixel-dir", dir}
	if t.skuFile != "" {
		args = append(args, "--sku", t.skuFile)
	}
	if t.device != "" {
		args = append(args, "--device", t.device)
	}
	if skin {
		args = append(args, "--skin")
	}
	return args
}

func commaList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func printImages(images []previewImage, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(images)
	}
	for _, i := range images {
		fmt.Printf("%s: %s (%s, %dx%d)\n", i.Target, i.File, i.Name, i.Width, i.Height)
	}
	return nil
}

func previewApp(dir string, o previewOptions) ([]previewImage, error) {
	if o.out == "" {
		o.out = filepath.Join(dir, ".kario", "preview")
	}
	out, err := filepath.Abs(o.out)
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	targets, err := previewTargets(o, cache)
	if err != nil {
		return nil, err
	}
	shell, err := findShell(o.shell)
	if err != nil {
		return nil, err
	}
	renderer, err := findRenderer(o.gui, shell)
	if err != nil {
		return nil, errors.New("kario preview draws with a pixel renderer (make paint in kario, or -gui PATH)")
	}
	node, err := findNode(o.node)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	app := filepath.Join(out, "app")
	if _, err := build(dir, buildOptions{out: app, quiet: true, native: o.native}); err != nil {
		return nil, err
	}
	bundle := filepath.Join(out, "preview.mjs")
	if err := bundlePreviews(dir, bundle); err != nil {
		return nil, err
	}
	rt := filepath.Join(cache, "kario", "node")
	if err := writeNode(rt); err != nil {
		return nil, err
	}
	if err := nativeAddon(app, rt); err != nil {
		return nil, err
	}
	lang := o.lang
	if lang == "" {
		lang = "en"
	}
	screens, err := evalPreviews(node, rt, bundle, app, lang)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range screens {
		names = append(names, s.Name)
	}
	type page struct {
		Name   string     `json:"name"`
		Screen *ui.Screen `json:"screen"`
	}
	pages := make([]page, len(screens))
	for i, slug := range slugs(names) {
		pages[i] = page{slug, screens[i].Screen}
	}
	raw, err := json.Marshal(pages)
	if err != nil {
		return nil, err
	}
	pagesFile := filepath.Join(out, "pages.json")
	if err := os.WriteFile(pagesFile, raw, 0o644); err != nil {
		return nil, err
	}
	var images []previewImage
	for _, t := range targets {
		tdir := filepath.Join(out, t.name)
		if err := os.RemoveAll(tdir); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(tdir, 0o755); err != nil {
			return nil, err
		}
		cmd := exec.Command(shell, pagesArgs(app, pagesFile, renderer, tdir, t, o.skin)...)
		cmd.Env = append(os.Environ(), "PHONE_LANG="+lang)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		drawn := map[string]string{}
		sc := bufio.NewScanner(bytes.NewReader(stdout))
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "PHONE-PAGE: "); ok {
				if name, file, ok := strings.Cut(rest, " "); ok {
					drawn[name] = file
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: phone-shell: %v: %s", t.name, err, strings.TrimSpace(stderr.String()))
		}
		for i, p := range pages {
			file := drawn[p.Name]
			f, err := os.Open(file)
			if err != nil {
				return nil, fmt.Errorf("%s: preview %q was not drawn", t.name, names[i])
			}
			cfg, err := png.DecodeConfig(f)
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			img := previewImage{Name: names[i], Target: t.name, File: file, Width: cfg.Width, Height: cfg.Height}
			if skin := strings.TrimSuffix(file, ".png") + ".skin.png"; o.skin {
				if _, err := os.Stat(skin); err == nil {
					img.Skin = skin
				}
			}
			images = append(images, img)
		}
	}
	return images, nil
}
