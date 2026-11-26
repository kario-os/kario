package shell

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// within runs f and fails when it does not end in time.
func within(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("did not end")
	}
}

// FuzzDoc is a doc node's Markdown, as any app may send it: headings,
// lists, tables, code, links, TeX math and Mermaid diagrams.
func FuzzDoc(f *testing.F) {
	f.Add("# Title\n\nSome *text* with `code` and a [link](https://example.com).\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n> quote\n\n- one\n- two\n", uint8(40))
	f.Add("Inline $x^2 + \\frac{a}{b}$ and\n\n$$\\sum_{i=0}^{n} \\sqrt{x_i} \\mathbb{R}$$\n", uint8(20))
	f.Add("```mermaid\nflowchart LR\n  A[Start] --> B{Ok?}\n  B -->|yes| C[Done]\n```\n", uint8(30))
	f.Fuzz(func(t *testing.T, text string, w uint8) {
		if len(text) > 4096 {
			return
		}
		within(t, 10*time.Second, func() {
			r := renderDoc(text, int(w))
			for _, l := range r.lines {
				if n := cv.StringWidth(string(plain(l))); w >= 48 && n > int(w) {
					t.Errorf("a line %d cells wide at width %d: %q", n, w, string(plain(l)))
				}
			}
			texToUnicode(text)
		})
	})
}

// FuzzCanvas is a canvas node's shapes: painting stays inside the canvas
// and within its work budget.
func FuzzCanvas(f *testing.F) {
	f.Add([]byte(`{"w":10,"h":8,"bg":"#000000","ops":[{"op":"rect","x":1,"y":1,"w":3,"h":2,"color":"#ff0000","fill":true},
		{"op":"line","x":0,"y":7,"x2":9,"y2":7,"color":"#ffffff","width":3},{"op":"circle","x":7,"y":3,"r":2,"color":"#fff","fill":true},
		{"op":"poly","pts":[5,0,9,0,9,1],"color":"#ff0000","fill":true},{"op":"dot","x":40,"y":-3,"color":"#ffffff"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var cn ui.Canvas
		if json.Unmarshal(raw, &cn) != nil {
			return
		}
		within(t, 10*time.Second, func() {
			r := paintCanvas(&cn)
			if len(r.pix) != r.w*r.h*3 || r.w > ui.MaxCanvasSide || r.h > ui.MaxCanvasSide {
				t.Errorf("a %dx%d raster of %d bytes", r.w, r.h, len(r.pix))
			}
		})
	})
}

// FuzzMermaid is a diagram's source, drawn by the JavaScript renderer
// under its time limit.
func FuzzMermaid(f *testing.F) {
	f.Add("flowchart LR\n  A[Start] --> B{Ok?}\n  B -->|yes| C[Done]\n  B -->|no| A\n")
	f.Add("sequenceDiagram\n  Alice->>Bob: Hi\n  Bob-->>Alice: Hello\n")
	f.Add("graph TD\n" + strings.Repeat("  A --> B\n", 20))
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 2048 {
			return
		}
		within(t, 10*time.Second, func() { renderMermaid(src, 40) })
	})
}

// FuzzScreen is a page an app sends: once limited, every field, action
// list and canvas is within what a page may carry.
func FuzzScreen(f *testing.F) {
	f.Add([]byte(`{"id":"home","title":"Notes","actions":[{"id":"new","label":"New","key":"1"}],"body":[{"kind":"item","title":"A","actions":[{"id":"open","label":"Open","primary":true}]},{"kind":"doc","text":"# x"},{"kind":"canvas","canvas":{"w":4,"h":4,"ops":[{"op":"dot","color":"#ffffff"}]}}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var s ui.Screen
		if json.Unmarshal(raw, &s) != nil {
			return
		}
		limitContent(&s)
		over := func(v string, n int) bool { return len(v) > n+len("…") }
		canvases := 0
		for _, n := range s.Body {
			if len(n.Actions) > maxNodeActions || over(n.Text, maxNodeText) || over(n.Title, maxNodeField) || over(n.Value, maxNodeText) {
				t.Fatalf("a node over the limits: %d actions, %d bytes of text", len(n.Actions), len(n.Text))
			}
			if n.Canvas != nil {
				canvases++
				if len(n.Canvas.Ops) > ui.MaxCanvasOps {
					t.Fatalf("%d canvas ops", len(n.Canvas.Ops))
				}
			}
		}
		if canvases > maxScreenCanvases || len(s.Actions) > maxNodeActions || over(s.Title, maxNodeField) {
			t.Fatalf("a page over the limits: %d canvases, %d actions", canvases, len(s.Actions))
		}
	})
}

// A document of deeply nested quotes lays out in about the memory its text
// takes, not in gigabytes of indentation.
func TestNestedQuotesAreBounded(t *testing.T) {
	r := renderDoc(strings.Repeat(">", 1000)+" "+strings.Repeat("x ", 20000), 30)
	n := 0
	for _, l := range r.lines {
		n += len(l)
	}
	if n > 1<<20 {
		t.Fatalf("%d runes laid out for a 41 KB document", n)
	}
}
