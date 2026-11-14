package jsrt

import (
	"strings"
	"testing"

	"github.com/kario-os/kario/pkg"
)

func TestCheck(t *testing.T) {
	js := []pkg.File{{Path: "manifest.json", Mode: 0o644}, {Path: "main.js", Mode: 0o644}, {Path: "locale/zh-Hans.json", Mode: 0o644}}
	for _, c := range []struct {
		name, runtime, main, exec string
		files                     []pkg.File
		want                      string // "" passes, else a part of the error
	}{
		{"native", "", "", "app", []pkg.File{{Path: "app", Mode: 0o755}}, ""},
		{"main without runtime", "", "main.js", "app", nil, `"main" goes with`},
		{"unknown runtime", "lua", "main.lua", "", nil, "unknown runtime"},
		{"js", "js", "main.js", "", js, ""},
		{"default main", "js", "", "", js, ""},
		{"js with exec", "js", "main.js", "app", js, `no "exec"`},
		{"main in a directory", "js", "src/main.js", "", js, "package root"},
		{"main not js", "js", "main.ts", "", js, "package root"},
		{"main missing", "js", "app.js", "", js, "not in the package"},
		{"native code", "js", "main.js", "", append(js, pkg.File{Path: "helper", Mode: 0o755}), "no executable file"},
		{"executable module", "js", "main.js", "", []pkg.File{{Path: "main.js", Mode: 0o755}}, "no executable file"},
		{"bytecode", "js", "main.js", "", append(js, pkg.File{Path: "main.jsc", Mode: 0o644}), "not bytecode"},
	} {
		err := Check(c.runtime, c.main, c.exec, c.files)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
