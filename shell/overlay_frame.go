package shell

import (
	"strings"
	"time"
)

// What each system overlay shows, for the pixel renderer (see frame.go). The
// wording matches the text renderer's.

func (m *menuOverlay) frame(c *Core) *FrameOverlay {
	o := &FrameOverlay{Kind: "menu", Title: m.title}
	if m.palette {
		q := m.query
		o.Query = &q
	}
	last := ""
	for i, e := range m.visible() {
		if e.group != last {
			o.Items = append(o.Items, FrameMenuItem{Header: true, Text: e.group})
			last = e.group
		}
		o.Items = append(o.Items, FrameMenuItem{Text: e.label, Key: e.key, Sel: i == m.sel})
	}
	if len(o.Items) == 0 {
		o.Lines = []string{tr.T("(No match)")}
	}
	return o
}

func (o *confirmOverlay) frame(c *Core) *FrameOverlay {
	yes, no := o.labels()
	return &FrameOverlay{Kind: "confirm", Lines: strings.Split(o.text, "\n"), Choices: []string{yes, no}}
}

func (o *permOverlay) frame(c *Core) *FrameOverlay {
	f := &FrameOverlay{Kind: "perm", Title: tr.T("System permission"), Choices: []string{tr.T("Allow"), tr.T("Deny")}, Choice: 1}
	if o.yes {
		f.Choice = 0
	}
	if o.origin != "" {
		f.Lines = []string{tr.T("In “%s”: “%s”", o.app.M.Name, o.origin), tr.T("Asks to use (granted to “%s”):", o.app.M.Name)}
	} else {
		f.Lines = []string{tr.T("“%s” asks to use:", o.app.M.Name)}
	}
	for _, p := range o.perms {
		f.Items = append(f.Items, FrameMenuItem{Text: o.label(p)})
	}
	return f
}

func (o *authOverlay) frame(c *Core) *FrameOverlay {
	f := &FrameOverlay{Kind: "auth", Title: tr.T("System security check"), PIN: [2]int{len(o.pin), pinLen},
		Lines: []string{tr.T("App: %s (%s)", o.app.M.Name, o.app.M.ID), o.prompt},
		Footer: []string{tr.T("Digit keys to enter · Back cancel"),
			tr.T("The system draws this screen; apps cannot cover it or read what is typed. What is signed is what is shown above.")}}
	if o.origin != "" {
		f.Warn = tr.T("Source: %s (hosted by this app)", o.origin)
	}
	f.Lines = append(f.Lines, o.bioLines()...)
	switch {
	case o.busy:
		f.Note = tr.T("Verifying…")
	case o.err != "":
		f.Note, f.NoteBad = o.err, true
	}
	return f
}

func (o *scanOverlay) frame(c *Core) *FrameOverlay {
	f := &FrameOverlay{Kind: "scan", Title: tr.T("Scan · System camera"),
		Lines:  []string{tr.T("Put the QR code in the frame · asked by %s", o.who()), tr.T("The app gets only what is read, never the picture")},
		Footer: []string{tr.T("Back cancel")}, Image: c.frameStream(o.stream, mediaW, mediaW, false)}
	f.Phase = float64(time.Since(o.t0).Milliseconds()%2400) / 2400
	return f
}

func (o *incomingOverlay) frame(c *Core) *FrameOverlay {
	kind := tr.T("Video call invitation")
	if o.n.Body != "" {
		kind = o.n.Body
	}
	return &FrameOverlay{Kind: "incoming", Title: o.n.Title, Lines: []string{kind, tr.T("From “%s”", o.app.M.Name)},
		Choices: []string{tr.T("Answer"), tr.T("Decline")}, Phase: float64(time.Since(o.at).Milliseconds()%1500) / 1500,
		Footer: []string{tr.T("OK answer · Back decline")}}
}
