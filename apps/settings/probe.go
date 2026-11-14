package main

import (
	"os"
	"strings"

	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

var layerNames = []struct{ id, label string }{
	{"uid", "Own uid"}, {"ns", "Namespaces"}, {"nonet", "No network"}, {"landlock", "Landlock"},
	{"seccomp", "seccomp"}, {"rlimit", "Resource limits"},
}

// probe tries what a malicious app would try and reports what the sandbox
// allowed. Checks whose layer is not active (e.g. no root on a developer
// machine, or a kernel without Landlock) are skipped rather than failed.
func probe(app *sdk.App) ui.Screen {
	applied := sandbox.ParseApplied(os.Getenv(sandbox.Env))
	checks := sandbox.SelfChecks(app.DataDir)
	var layers []string
	for _, l := range layerNames {
		if applied[l.id] {
			layers = append(layers, tr.T(l.label))
		}
	}
	if len(layers) == 0 {
		layers = []string{tr.T("None")}
	}
	s := ui.Screen{ID: "probe", Title: tr.T("Sandbox check"), Body: []ui.Node{
		sdk.Styled(ui.StyleDim, tr.T("This process: uid=%d", os.Getuid())),
		sdk.Styled(ui.StyleDim, tr.T("Restrictions in force: %s", strings.Join(layers, " · "))),
	}}
	pass, ran := 0, 0
	for _, c := range checks {
		if c.Needs != "" && !applied[c.Needs] {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, Title: "– " + c.Name, Trailing: tr.T("Skipped")})
			continue
		}
		ran++
		denied := c.Try() != nil
		ok := denied == c.WantDenied
		if ok {
			pass++
		}
		result := tr.T("Allowed")
		if denied {
			result = tr.T("Denied")
		}
		mark := "✓"
		if !ok {
			mark = "✗"
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, Title: mark + " " + c.Name, Trailing: result})
	}
	style := ui.StyleOK
	if pass != ran {
		style = ui.StyleWarn
	}
	summary := tr.T("Sandbox check: %d/%d as expected", pass, ran)
	if skipped := len(checks) - ran; skipped > 0 {
		summary += tr.T(" (%d skipped)", skipped)
	}
	s.Body = append(s.Body, sdk.Styled(style, summary))
	return s
}
