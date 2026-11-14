package main

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// loadSource returns a program's JavaScript. TypeScript (app.ts) is stripped
// of types by esbuild's Go API, so mini-programs can be written in TS without
// a Node toolchain on the device; type checking happens at development time
// with tsc against types/phone.d.ts.
func loadSource(id string) (string, error) {
	sub, err := fs.Sub(programsFS, "programs/"+id)
	if err != nil {
		return "", err
	}
	return loadSourceFS(sub, id)
}

// loadSourceFS reads a mini-program's app.tsx or app.ts (stripped of types,
// JSX turned into h() calls that build phone.Ui nodes, see prelude.js) or
// its app.js.
func loadSourceFS(fsys fs.FS, id string) (string, error) {
	for _, src := range []struct {
		name   string
		loader api.Loader
	}{{"app.tsx", api.LoaderTSX}, {"app.ts", api.LoaderTS}} {
		raw, err := fs.ReadFile(fsys, src.name)
		if err != nil {
			continue
		}
		return transform(string(raw), id+"/"+src.name, src.loader)
	}
	raw, err := fs.ReadFile(fsys, "app.js")
	return string(raw), err
}

func transform(code, file string, loader api.Loader) (string, error) {
	{
		res := api.Transform(code, api.TransformOptions{
			Loader:      loader,
			Target:      api.ES2017,
			Sourcefile:  file,
			JSXFactory:  "h",
			JSXFragment: "Fragment",
		})
		if len(res.Errors) > 0 {
			var msgs []string
			for _, e := range res.Errors {
				loc := ""
				if e.Location != nil {
					loc = fmt.Sprintf("%d:%d ", e.Location.Line, e.Location.Column)
				}
				msgs = append(msgs, loc+e.Text)
			}
			return "", fmt.Errorf("%s: %s", file, strings.Join(msgs, "; "))
		}
		return string(res.Code), nil
	}
}
