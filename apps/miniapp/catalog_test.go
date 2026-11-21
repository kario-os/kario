package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// counterDir is the sample mini-program the selftest signs and installs.
const counterDir = "../../examples/mp-counter"

// TestTranslate: phone.t in a program's runtime, its catalog read by the
// host.
func TestTranslate(t *testing.T) {
	t.Setenv("PHONE_LANG", "es")
	cases := []struct{ call, want string }{
		{`"%s has %d notes", "Ann", 3.9`, "Ann: 3"},
		{`"%s has %d notes", "Ann", -0.5`, "Ann: 0"},
		{`"%s has %d notes", "Ann", "12"`, "Ann: 12"},
		{`"%s has %d notes", "Ann", NaN`, "Ann: NaN"},
		{`"%s has %d notes", "Ann", -Infinity`, "Ann: -Infinity"},
		{`"%s has %d notes", "Ann"`, "Ann: %d"},
		{`"%s has %d notes", "Ann", 2, "extra"`, "Ann: 2"},
		{`"Not translated: %s", undefined`, "Not translated: undefined"},
		{`"%s is %s", "%s", "x"`, "%s is x"},
		{`"%d%% done, %x", 1e21`, "1000000000000000000000% done, %x"},
		{`"%d", -1.2345e25`, "-12345000000000000000000000"},
		{`"100%"`, "all"},
		{`"50%% as written"`, "50%% as written"},
		{`"toString"`, "toString"},
		{`"__proto__"`, "proto"},
	}
	var calls []string
	for _, c := range cases {
		calls = append(calls, "ui.text(phone.t("+c.call+"))")
	}
	p := testProgram(t, "tr", fstest.MapFS{
		"app.json":       {Data: []byte(`{"name": "tr"}`)},
		"app.js":         {Data: []byte(`Page("index", { render: function () { return [` + strings.Join(calls, ", ") + `]; } });`)},
		"locale/es.json": {Data: []byte(`{"%s has %d notes": "%s: %d", "100%": "all", "__proto__": "proto"}`)},
	})
	s := startHost(t, p)
	page := s.open("tr")
	if len(page.Body) != len(cases) {
		t.Fatalf("body %v", page.Body)
	}
	for i, c := range cases {
		if got := page.Body[i].Text; got != c.want {
			t.Errorf("phone.t(%s) = %q, want %q", c.call, got, c.want)
		}
	}
}

var (
	tCall = regexp.MustCompile(`phone\.t\(\s*("(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*')`)
	verbs = regexp.MustCompile(`%[sd]`)
)

// jsString is the value of a JavaScript string literal.
func jsString(lit string) (string, error) {
	body := lit[1 : len(lit)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			b.WriteByte(body[i])
			continue
		}
		if i++; i == len(body) {
			return "", fmt.Errorf("%s: a lone backslash", lit)
		}
		switch e := body[i]; e {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'u':
			end := i + 5
			if i+1 < len(body) && body[i+1] == '{' {
				end = strings.IndexByte(body[i:], '}') + i + 1
			}
			if end > len(body) || end <= i {
				return "", fmt.Errorf("%s: a short \\u escape", lit)
			}
			n, err := strconv.ParseUint(strings.Trim(body[i+1:end], "{}"), 16, 32)
			if err != nil {
				return "", fmt.Errorf("%s: %w", lit, err)
			}
			b.WriteRune(rune(n))
			i = end - 1
		default:
			b.WriteByte(e)
		}
	}
	return b.String(), nil
}

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
				msg, err := jsString(m[1])
				if err != nil {
					t.Fatalf("%s/%s: %v", name, src, err)
				}
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

func TestJSString(t *testing.T) {
	for lit, want := range map[string]string{
		`"a\"b"`: `a"b`, `'it\'s'`: "it's", `"\u4e2d"`: "\u4e2d", `"\u{1F600}!"`: "\U0001F600!", `"a\\n"`: `a\n`, `"\n"`: "\n",
	} {
		if got, err := jsString(lit); err != nil || got != want {
			t.Errorf("jsString(%s) = %q, %v; want %q", lit, got, err, want)
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
		if got := lookup(msgs, "Pay"); got != want {
			t.Errorf("order in %s: %q, want %q", lang, got, want)
		}
	}
}

// A program's app.json gives its name and description in other languages
// (locales), each picked by the device's language along its fallback chain
// as a manifest's are, and the host's screens show them; the locales are
// checked as a manifest's and translate nothing else.
func TestProgramText(t *testing.T) {
	const hans = "\u4fbf\u7b7e"
	files := fstest.MapFS{
		"app.json": {Data: []byte(`{"name": "Notes", "lang": "en", "description": "Write things down", "icon": "N",
			"locales": {"zh-Hans": {"name": "` + hans + `", "description": "hans description"}, "es": {"name": "Notas"}}}`)},
		"app.js": {Data: []byte(`Page("index", { render: function () { return []; } });`)},
	}
	p := testProgram(t, "notes", files)
	h := newHost(nil)
	h.progs = []*program{p}
	for lang, want := range map[string][2]string{
		"en":      {"Notes", "Write things down"},
		"es":      {"Notas", "Write things down"},
		"zh-Hans": {hans, "hans description"},
		"zh-Hant": {hans, "hans description"},
		"zh-HK":   {hans, "hans description"},
	} {
		t.Setenv("PHONE_LANG", lang)
		if name, desc := p.meta.text(); name != want[0] || desc != want[1] {
			t.Errorf("%s: %q %q, want %q", lang, name, desc, want)
		}
		item := h.home().Body[1]
		info := h.infoScreen(p).Body[0].Text
		page, ok := parsePage(p, json.RawMessage(`{"id": "mp:notes:index:1"}`))
		if item.Title != want[0] || item.Subtitle != want[1] || !strings.HasPrefix(info, "# "+want[0]+"\n\n"+want[1]+"\n") || !ok || page.Title != want[0] {
			t.Errorf("%s: home %q %q, info %q, page %q", lang, item.Title, item.Subtitle, info, page.Title)
		}
	}
	if p.meta.Name != "Notes" {
		t.Errorf("the identity changed: %q", p.meta.Name)
	}

	for raw, want := range map[string]string{
		`{"name": "x", "locales": {"fr": {"name": "x"}}}`:                               `"fr" is not one of`,
		`{"name": "x", "lang": "de"}`:                                                   `lang "de"`,
		`{"name": "x", "locales": {"es": {"name": "` + strings.Repeat("n", 33) + `"}}}`: "1–32 characters",
		`{"name": "x", "locales": {"es": {"purposes": {"camera": "x"}}}}`:               "translated by locale/es.json",
	} {
		var m meta
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		if err := m.check(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
}

// The built-in programs and the sample have their name and description in
// every language the system offers.
func TestBuiltinProgramText(t *testing.T) {
	programs := map[string]fs.FS{"mp-counter": os.DirFS(counterDir)}
	for _, id := range []string{"notes", "notice", "order"} {
		programs[id], _ = fs.Sub(programsFS, "programs/"+id)
	}
	for id, files := range programs {
		p := testProgram(t, id, fstest.MapFS{"app.json": {Data: readFile(t, files, "app.json")}, "app.js": {Data: []byte("")}})
		seen := map[string]bool{}
		for _, l := range i18n.Languages() {
			t.Setenv("PHONE_LANG", l.ID)
			name, desc := p.meta.text()
			if l.ID != i18n.DefaultLang && (name == p.meta.Name && l.ID != "zh-Hant" || desc == p.meta.Desc) {
				t.Errorf("%s in %s: %q %q, as written", id, l.ID, name, desc)
			}
			seen[name+desc] = true
		}
		if len(seen) < 3 {
			t.Errorf("%s: %d different texts", id, len(seen))
		}
	}
}

func readFile(t *testing.T, fsys fs.FS, name string) []byte {
	t.Helper()
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Each built-in program starts on kario-jsrt and shows its index page;
// exiting it keeps its runtime warm (all three fit), ended with the host.
func TestBuiltinProgramsBoot(t *testing.T) {
	s := startHost(t)
	var ids []string
	s.on(func(h *host) {
		for _, p := range h.progs {
			ids = append(ids, p.meta.ID)
		}
	})
	if len(ids) != 3 {
		t.Fatalf("programs %v", ids)
	}
	for _, id := range ids {
		page := s.open(id)
		if len(page.Body) == 0 {
			t.Errorf("%s: an empty index page", id)
		}
		pid := s.runtimePid(id)
		s.act(page.ID, "", "mp.exit", "")
		s.expect(ui.MsgPop)
		s.waitDepth(1)
		s.on(func(h *host) {
			if p := h.progByID(id); !p.warm || p.run == nil || p.run.cmd.Process.Pid != pid {
				t.Errorf("%s: not kept warm after exit", id)
			}
		})
	}
	var pids []int
	for _, id := range ids {
		pids = append(pids, s.runtimePid(id))
	}
	s.on(func(h *host) { h.stopAll() })
	for _, pid := range pids {
		gone(t, pid)
	}
}

// A signed mini-program's catalog is one of its signed files: it is
// installed with the program, used by phone.t, and a changed or added
// catalog fails verification like changed code.
func TestSignedProgramCarriesItsCatalog(t *testing.T) {
	t.Setenv("PHONE_LANG", i18n.DefaultLang)
	s := startHost(t)
	var h *host
	s.on(func(x *host) { h = x })
	data := h.app.DataDir
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
	var p *program
	s.on(func(h *host) {
		if _, err = h.install(file); err != nil {
			return
		}
		h.progs = nil
		h.loadInstalled()
		if len(h.rejected) != 0 || len(h.progs) != 1 {
			err = fmt.Errorf("rejected %v, loaded %d", h.rejected, len(h.progs))
			return
		}
		p = h.progs[0]
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := readCatalog(os.DirFS(counterDir), i18n.DefaultLang)
	if err != nil || len(want) == 0 || !maps.Equal(p.msgs, want) {
		t.Fatalf("installed catalog %v, want %v (%v)", p.msgs, want, err)
	}
	if err := os.MkdirAll(h.storeDir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	store2, _ := json.Marshal(map[string]string{"n": "2"})
	if err := os.WriteFile(filepath.Join(h.storeDir(p), jsrt.StorageFile), store2, 0o600); err != nil {
		t.Fatal(err)
	}
	page := s.open(p.meta.ID)
	if text := strings.Replace(want["Clicked %d times"], "%d", "2", 1); len(page.Body) == 0 || page.Body[0].Text != text {
		t.Fatalf("render: %v, want %q", page.Body, text)
	}
	s.act(page.ID, "", "mp.exit", "")
	s.expect(ui.MsgPop)

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
		s.on(func(h *host) { _, err = h.install(file) })
		if err != nil {
			t.Fatal(err)
		}
		if err := change(); err != nil {
			t.Fatal(err)
		}
		var rejected, loaded int
		s.on(func(h *host) {
			h.progs = nil
			h.loadInstalled()
			rejected, loaded = len(h.rejected), len(h.progs)
		})
		if rejected != 1 || loaded != 0 {
			t.Fatalf("changed catalog: rejected %d, loaded %d", rejected, loaded)
		}
	}
}

// The store a program kept while it ran in the host itself moves into its
// runtime's.
func TestStorageMigrates(t *testing.T) {
	notes := testProgram(t, "notes", subFS(t, "programs/notes"))
	s := startHost(t, notes)
	var dir string
	s.on(func(h *host) { dir = h.storeDir(notes) })
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "storage.json"), []byte(`{"notes": ["milk", "eggs"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	page := s.open("notes")
	if !strings.Contains(screenText(page), "milk") || !strings.Contains(screenText(page), "eggs") {
		t.Fatalf("index after the move: %s", screenText(page))
	}
	if _, err := os.Stat(filepath.Join(dir, "storage.json")); err == nil {
		t.Error("the old store is still there")
	}
}

func subFS(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(programsFS, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := programsFS.ReadFile(path)
		out[strings.TrimPrefix(path, dir+"/")] = &fstest.MapFile{Data: raw}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
