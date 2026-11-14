package shell

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	cv "github.com/kario-os/kario/canvas"
)

// Mermaid fences in doc nodes are drawn as Unicode box art by lovely-mermaid
// (Apache-2.0, the library dsh-TUI uses), bundled into one script and run in
// goja: no browser and no DOM. Regenerate with `make mermaid-bundle`.
//
//go:embed mermaid/lovely-mermaid.iife.js
var mermaidJS string

var mermaidRT struct {
	sync.Mutex
	rt     *goja.Runtime
	render goja.Callable
	err    error
}

type mermaidArt struct {
	lines [][]srune
	width int
}

// goja has no Intl; lovely-mermaid only needs grapheme segmentation. Code
// points with combining marks, ZWJ sequences and variation selectors folded
// in are enough for CJK and common symbols.
const intlSegmenterShim = `
var Intl = globalThis.Intl || {};
if (!Intl.Segmenter) {
  Intl.Segmenter = function () {
    this.segment = function (s) {
      var cps = Array.from(String(s)), out = [], index = 0;
      for (var k = 0; k < cps.length; k++) {
        var g = cps[k];
        while (k + 1 < cps.length && /[\u0300-\u036f\u200d\ufe0e\ufe0f\u20e3]/.test(cps[k + 1])) {
          g += cps[++k];
          if (cps[k] === "\u200d" && k + 1 < cps.length) g += cps[++k];
        }
        out.push({ segment: g, index: index, input: s });
        index += g.length;
      }
      return out;
    };
  };
}
globalThis.Intl = Intl;
`

func loadMermaid() error {
	if mermaidRT.rt != nil || mermaidRT.err != nil {
		return mermaidRT.err
	}
	rt := goja.New()
	if _, err := rt.RunString(intlSegmenterShim); err != nil {
		mermaidRT.err = err
		return err
	}
	if _, err := rt.RunString(mermaidJS); err != nil {
		mermaidRT.err = err
		return err
	}
	lib := rt.Get("LovelyMermaid").ToObject(rt)
	fn, ok := goja.AssertFunction(lib.Get("render"))
	if !ok {
		mermaidRT.err = fmt.Errorf("lovely-mermaid: render not found")
		return mermaidRT.err
	}
	mermaidRT.rt, mermaidRT.render = rt, fn
	return nil
}

var mermaidStyles = map[string]cv.Style{
	"border": st(colAccent, colBG),
	"edge":   st(colDim, colBG),
	"text":   stBase,
}

func renderMermaidOnce(src string) (*mermaidArt, error) {
	mermaidRT.Lock()
	defer mermaidRT.Unlock()
	if err := loadMermaid(); err != nil {
		return nil, err
	}
	rt := mermaidRT.rt
	t := time.AfterFunc(800*time.Millisecond, func() { rt.Interrupt("mermaid render timeout") })
	defer func() {
		t.Stop()
		rt.ClearInterrupt()
	}()
	v, err := mermaidRT.render(goja.Undefined(), rt.ToValue(src))
	if err != nil {
		return nil, err
	}
	if v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return nil, fmt.Errorf("unsupported or empty diagram")
	}
	obj := v.ToObject(rt)
	art := &mermaidArt{width: int(obj.Get("width").ToInteger())}
	var styled []any
	if err := rt.ExportTo(obj.Get("styled"), &styled); err != nil {
		return nil, err
	}
	for _, rowAny := range styled {
		row, _ := rowAny.([]any)
		var line []srune
		for _, segAny := range row {
			seg, _ := segAny.(map[string]any)
			text, _ := seg["text"].(string)
			role, _ := seg["role"].(string)
			s, ok := mermaidStyles[role]
			if !ok {
				s = stBase
			}
			line = append(line, runesOf(text, s)...)
		}
		art.lines = append(art.lines, line)
	}
	return art, nil
}

var horizontal = regexp.MustCompile(`(?m)^(\s*(?:flowchart|graph)\s+)(LR|RL)\b`)

// renderMermaid fits a diagram to a small screen: a too-wide left-to-right
// flowchart is redrawn top-to-bottom before giving up.
func renderMermaid(src string, width int) (*mermaidArt, string) {
	art, err := renderMermaidOnce(src)
	if err != nil {
		return nil, tr.T("Cannot draw: %s", strings.SplitN(err.Error(), "\n", 2)[0])
	}
	if art.width <= width {
		return art, ""
	}
	if horizontal.MatchString(src) {
		if tall, err := renderMermaidOnce(horizontal.ReplaceAllString(src, "${1}TD")); err == nil && tall.width <= width {
			return tall, tr.T("(The screen is narrow; laid out top to bottom)")
		}
	}
	return nil, tr.T("(The diagram is %d columns wide, wider than the screen's %d; showing the source)", art.width, width)
}
