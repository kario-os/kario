package main

import (
	"encoding/json"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dop251/goja"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/sdk"
)

// counterDir is the sample mini-program the selftest signs and installs.
const counterDir = "../../examples/mp-counter"

func TestTranslate(t *testing.T) {
	msgs := map[string]string{"%s has %d notes": "%s: %d", "100%": "all"}
	rt := goja.New()
	vals := func(xs ...any) []goja.Value {
		var out []goja.Value
		for _, x := range xs {
			out = append(out, rt.ToValue(x))
		}
		return out
	}
	for _, c := range []struct {
		msg  string
		args []goja.Value
		want string
	}{
		{"%s has %d notes", vals("Ann", 3.9), "Ann: 3"},
		{"%s has %d notes", vals("Ann", -0.5), "Ann: 0"},
		{"%s has %d notes", vals("Ann", "12"), "Ann: 12"},
		{"%s has %d notes", vals("Ann", math.NaN()), "Ann: NaN"},
		{"%s has %d notes", vals("Ann"), "Ann: %d"},
		{"%s has %d notes", vals("Ann", 2, "extra"), "Ann: 2"},
		{"Not translated: %s", vals(goja.Undefined()), "Not translated: undefined"},
		{"%s is %s", vals("%s", "x"), "%s is x"},
		{"%d%% done, %x", vals(1e21), "1000000000000000000000% done, %x"},
		{"100%", nil, "all"},
		{"50%% as written", nil, "50%% as written"},
	} {
		if got := translate(msgs, c.msg, c.args); got != c.want {
			t.Errorf("translate(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
}

var (
	tCall = regexp.MustCompile(`phone\.t\(\s*("(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*')`)
	verbs = regexp.MustCompile(`%[sd]`)
)

// TestCatalogs: every phone.t("literal") of the built-in mini-programs and
// the sample has a translation with the same verbs, and every translation
// is used.
func TestCatalogs(t *testing.T) {
	programs := map[string]fs.FS{"mp-counter": os.DirFS(counterDir)}
	dirs, err := fs.ReadDir(programsFS, "programs")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if d.IsDir() {
			programs[d.Name()], _ = fs.Sub(programsFS, "programs/"+d.Name())
		}
	}
	rt := goja.New()
	for name, files := range programs {
		msgs, err := readCatalog(files, i18n.DefaultLang)
		if err != nil || len(msgs) == 0 {
			t.Errorf("%s: no catalog (%v)", name, err)
			continue
		}
		used := map[string]bool{}
		for _, src := range []string{"app.js", "app.ts", "app.tsx"} {
			raw, err := fs.ReadFile(files, src)
			if err != nil {
				continue
			}
			for _, m := range tCall.FindAllStringSubmatch(string(raw), -1) {
				v, err := rt.RunString(m[1])
				if err != nil {
					t.Fatalf("%s/%s: %s: %v", name, src, m[1], err)
				}
				msg := v.String()
				used[msg] = true
				tr, ok := msgs[msg]
				switch {
				case !ok:
					t.Errorf("%s/%s: no translation for %q", name, src, msg)
				case !slices.Equal(verbs.FindAllString(msg, -1), verbs.FindAllString(tr, -1)):
					t.Errorf("%s: %q and its translation have different verbs", name, msg)
				}
			}
		}
		for msg := range msgs {
			if !used[msg] {
				t.Errorf("%s: %q is not used", name, msg)
			}
		}
	}
}

// A message a language's catalog lacks comes from the languages it falls
// back to (zh-Hant: zh-Hans), never from another language's catalog.
func TestCatalogFallback(t *testing.T) {
	files := fstest.MapFS{
		"locale/zh-Hans.json": {Data: []byte(`{"Pay": "hans pay", "Done": "hans done"}`)},
		"locale/zh-Hant.json": {Data: []byte(`{"Pay": "hant pay"}`)},
		"locale/es.json":      {Data: []byte(`{"Done": "Listo"}`)},
	}
	for _, c := range []struct {
		lang string
		want map[string]string
	}{
		{"zh-Hant", map[string]string{"Pay": "hant pay", "Done": "hans done"}},
		{"zh-TW", map[string]string{"Pay": "hant pay", "Done": "hans done"}},
		{"zh-Hans", map[string]string{"Pay": "hans pay", "Done": "hans done"}},
		{"es", map[string]string{"Done": "Listo"}},
		{"en", nil},
	} {
		got, err := readCatalog(files, c.lang)
		if err != nil || !maps.Equal(got, c.want) {
			t.Errorf("%s: %v (%v), want %v", c.lang, got, err, c.want)
		}
	}
	if _, err := readCatalog(fstest.MapFS{"locale/zh-Hans.json": {Data: []byte("{")}}, "zh-Hant"); err == nil {
		t.Error("a broken fallback catalog was accepted")
	}

	order, _ := fs.Sub(programsFS, "programs/order")
	for lang, want := range map[string]string{"zh-Hant": "\u53bb\u4ed8\u6b3e", "es": "Pagar", "en": "Pay"} {
		msgs, err := readCatalog(order, lang)
		if err != nil {
			t.Fatal(err)
		}
		if got := translate(msgs, "Pay", nil); got != want {
			t.Errorf("order in %s: %q, want %q", lang, got, want)
		}
	}
}

func TestBuiltinProgramsBoot(t *testing.T) {
	h := &host{app: &sdk.App{DataDir: t.TempDir()}, pages: map[string]*page{}}
	if err := h.load(); err != nil {
		t.Fatal(err)
	}
	for _, p := range h.progs {
		if err := h.boot(p); err != nil {
			t.Errorf("%s: %v", p.meta.ID, err)
		}
	}
}

// A signed mini-program's catalog is one of its signed files: it is
// installed with the program, used by phone.t, and a changed or added
// catalog fails verification like changed code.
func TestSignedProgramCarriesItsCatalog(t *testing.T) {
	t.Setenv("PHONE_LANG", i18n.DefaultLang)
	data := t.TempDir()
	h := &host{app: &sdk.App{DataDir: data}, pages: map[string]*page{}}
	store, err := pkg.DevStore(filepath.Join(t.TempDir(), "store"), filepath.Join(data, "trust"))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := pkg.DevDeveloper(filepath.Join(t.TempDir(), "developers"), "test", store)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pkg.Build(counterDir, dev)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "counter.mpk")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.install(file); err != nil {
		t.Fatal(err)
	}
	h.loadInstalled()
	if len(h.rejected) != 0 || len(h.progs) != 1 {
		t.Fatalf("rejected %v, loaded %d", h.rejected, len(h.progs))
	}
	p := h.progs[0]
	want, err := readCatalog(os.DirFS(counterDir), i18n.DefaultLang)
	if err != nil || len(want) == 0 || !maps.Equal(p.msgs, want) {
		t.Fatalf("installed catalog %v, want %v (%v)", p.msgs, want, err)
	}
	if err := h.boot(p); err != nil {
		t.Fatal(err)
	}
	render, _ := goja.AssertFunction(p.pages["index"].Get("render"))
	this := p.rt.NewObject()
	_ = this.Set("data", map[string]any{"n": 2})
	v, err := render(this)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	out, _ := json.Marshal(v.Export())
	if err := json.Unmarshal(out, &nodes); err != nil || len(nodes) == 0 {
		t.Fatalf("render: %s", out)
	}
	if text := strings.Replace(want["Clicked %d times"], "%d", "2", 1); nodes[0]["text"] != text {
		t.Fatalf("render: %v, want %q", nodes[0], text)
	}

	locale := filepath.Join(h.installedDir(), p.meta.ID, "locale")
	for _, change := range []func() error{
		func() error {
			f, err := os.OpenFile(filepath.Join(locale, i18n.DefaultLang+".json"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = f.WriteString(" ")
			return err
		},
		func() error { return os.WriteFile(filepath.Join(locale, "en.json"), []byte("{}"), 0o644) },
	} {
		if _, err := h.install(file); err != nil {
			t.Fatal(err)
		}
		if err := change(); err != nil {
			t.Fatal(err)
		}
		h.progs = nil
		h.loadInstalled()
		if len(h.rejected) != 1 || len(h.progs) != 0 {
			t.Fatalf("changed catalog: rejected %v, loaded %d", h.rejected, len(h.progs))
		}
	}
}
