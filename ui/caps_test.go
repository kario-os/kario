package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// capConstants are the package's Cap* string constants by value, read from
// its sources as tools/tsgen reads them.
func capConstants(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if !strings.HasPrefix(n.Name, "Cap") || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						out[v] = n.Name
					}
				}
			}
		}
	}
	return out
}

// Every capability says which platform level it came in at, one the system
// has reached; the table names nothing else.
func TestCapLevelsComplete(t *testing.T) {
	caps := capConstants(t)
	if len(caps) < 100 {
		t.Fatalf("found only %d capabilities", len(caps))
	}
	for v, name := range caps {
		level, ok := CapLevels[v]
		switch {
		case !ok:
			t.Errorf("%s (%q) has no level in CapLevels (22-developer-api.md §2.2 says which)", name, v)
		case level < 1 || level > PlatformLevel:
			t.Errorf("%s: level %d, not 1 to PlatformLevel (%d)", name, level, PlatformLevel)
		}
	}
	for v := range CapLevels {
		if _, ok := caps[v]; !ok {
			t.Errorf("CapLevels names %q, which is no capability", v)
		}
	}
}
