package shell

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	gtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// Markdown documents are rendered by the system, not by a browser: the same
// node shows up as styled text here and as rich layout in a pixel renderer.

type srune struct {
	r  rune
	st cv.Style
}

type docLink struct {
	text, url string
}

type docResult struct {
	lines [][]srune
	links []docLink
}

var (
	md = goldmark.New(goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithInlineParsers(util.Prioritized(mathParser{}, 500))))
	colCode   = cv.RGB{232, 182, 120}
	colMath   = cv.RGB{140, 200, 235}
	colCodeBG = cv.RGB{34, 36, 46}
	colQuote  = cv.RGB{150, 155, 170}
)

var kindMath = ast.NewNodeKind("Math")

type mathSpan struct {
	ast.BaseInline
	tex string
}

func (n *mathSpan) Kind() ast.NodeKind         { return kindMath }
func (n *mathSpan) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// mathParser takes $…$ as inline math with pandoc's rules, so prices stay
// text: no space after the opening $, none before the closing one, and the
// closing $ is not followed by a digit. $$ is left to the paragraph.
type mathParser struct{}

func (mathParser) Trigger() []byte { return []byte{'$'} }

func (mathParser) Parse(parent ast.Node, block gtext.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 3 || line[1] == ' ' || line[1] == '$' {
		return nil
	}
	for i := 2; i < len(line) && line[i] != '\n'; i++ {
		if line[i] == '\\' {
			i++
			continue
		}
		if line[i] == '$' && line[i-1] != ' ' && (i+1 >= len(line) || line[i+1] < '0' || line[i+1] > '9') {
			block.Advance(i + 1)
			return &mathSpan{tex: string(line[1:i])}
		}
	}
	return nil
}

// displayMath returns the formula of a $$…$$ paragraph.
func displayMath(n ast.Node, src []byte) (string, bool) {
	var b strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		b.Write(seg.Value(src))
	}
	t := strings.TrimSpace(b.String())
	if len(t) < 5 || !strings.HasPrefix(t, "$$") || !strings.HasSuffix(t, "$$") {
		return "", false
	}
	return t[2 : len(t)-2], true
}

func (r *docRenderer) formula(tex string, prefix, cont []srune) {
	sty := cv.Style{FG: colMath, BG: colBG, Italic: true}
	for _, part := range strings.Split(texToUnicode(tex), "; ") {
		p := append(append([]srune{}, prefix...), runesOf("  ", stBase)...)
		r.wrap(runesOf(part, sty), p, append(append([]srune{}, cont...), runesOf("  ", stBase)...))
		prefix = cont
	}
}

type docRenderer struct {
	src   []byte
	w     int
	lines [][]srune
	links []docLink
}

func runesOf(s string, st cv.Style) []srune {
	out := make([]srune, 0, len(s))
	for _, r := range s {
		out = append(out, srune{r, st})
	}
	return out
}

func renderDoc(text string, w int) *docResult {
	r := &docRenderer{src: []byte(text), w: w}
	root := md.Parser().Parse(gtext.NewReader(r.src))
	first := true
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		if !first {
			r.lines = append(r.lines, nil)
		}
		first = false
		r.block(n, nil, nil)
	}
	return &docResult{lines: r.lines, links: r.links}
}

// links extracts link targets without laying out, for building actions.
func docLinks(text string) []docLink {
	return renderDoc(text, 80).links
}

func (r *docRenderer) block(n ast.Node, prefix, cont []srune) {
	switch b := n.(type) {
	case *ast.Heading:
		hs := bold(stBase)
		if b.Level == 1 {
			hs.FG = colAccent
		} else if b.Level >= 3 {
			hs.FG = cv.RGB{200, 205, 220}
		}
		mark := strings.Repeat("#", b.Level) + " "
		r.wrap(r.inline(b, hs), append(append([]srune{}, prefix...), runesOf(mark, st(colDim, colBG))...), cont)
	case *ast.Paragraph, *ast.TextBlock:
		if tex, ok := displayMath(n, r.src); ok {
			r.formula(tex, prefix, cont)
			return
		}
		r.wrap(r.inline(n, stBase), prefix, cont)
	case *ast.List:
		i := b.Start
		if i == 0 {
			i = 1
		}
		for item := b.FirstChild(); item != nil; item = item.NextSibling() {
			marker := "• "
			if b.IsOrdered() {
				marker = fmt.Sprintf("%d. ", i)
				i++
			}
			mp := append(append([]srune{}, cont...), runesOf(marker, st(colAccent, colBG))...)
			pad := append(append([]srune{}, cont...), runesOf(strings.Repeat(" ", cv.StringWidth(marker)), stBase)...)
			if item == b.FirstChild() {
				mp = append(append([]srune{}, prefix...), runesOf(marker, st(colAccent, colBG))...)
			}
			firstChild := true
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				if firstChild {
					r.block(c, mp, pad)
				} else {
					r.block(c, pad, pad)
				}
				firstChild = false
			}
		}
	case *ast.Blockquote:
		bar := runesOf("│ ", st(colDim, colBG))
		p := append(append([]srune{}, prefix...), bar...)
		c := append(append([]srune{}, cont...), bar...)
		firstChild := true
		for ch := b.FirstChild(); ch != nil; ch = ch.NextSibling() {
			if !firstChild {
				r.lines = append(r.lines, c)
			}
			r.block(ch, p, c)
			p, firstChild = c, false
		}
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		code := st(colCode, colCodeBG)
		if fb, ok := b.(*ast.FencedCodeBlock); ok && string(fb.Language(r.src)) == "mermaid" {
			var src strings.Builder
			for i := 0; i < n.Lines().Len(); i++ {
				seg := n.Lines().At(i)
				src.Write(seg.Value(r.src))
			}
			avail := r.w - cv.StringWidth(string(plain(prefix)))
			art, note := renderMermaid(src.String(), avail)
			if note != "" {
				r.lines = append(r.lines, append(append([]srune{}, prefix...), runesOf(note, st(colDim, colBG))...))
				prefix = cont
			}
			if art != nil {
				for _, l := range art.lines {
					r.lines = append(r.lines, append(append([]srune{}, prefix...), l...))
					prefix = cont
				}
				return
			}
		}
		var lx *lexer
		if fb, ok := b.(*ast.FencedCodeBlock); ok {
			lang := string(fb.Language(r.src))
			if lang == "math" {
				var src strings.Builder
				for i := 0; i < n.Lines().Len(); i++ {
					seg := n.Lines().At(i)
					src.WriteString(strings.TrimRight(string(seg.Value(r.src)), "\n") + " \\\\ ")
				}
				r.formula(strings.TrimSuffix(strings.TrimSpace(src.String()), "\\\\"), prefix, cont)
				return
			}
			if lang != "" {
				r.lines = append(r.lines, append(append([]srune{}, prefix...), runesOf(lang, st(colDim, colBG))...))
				prefix = cont
			}
			lx = lexerFor(lang)
		}
		inBlock := false
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			s := strings.TrimRight(string(seg.Value(r.src)), "\n")
			s = strings.ReplaceAll(s, "\t", "    ")
			rs := runesOf(" "+s, code)
			if lx != nil {
				rs = append(runesOf(" ", code), lx.highlight(s, &inBlock, colCodeBG)...)
			}
			r.wrapCode(rs, prefix, cont, code)
			prefix = cont
		}
	case *ast.ThematicBreak:
		used := cv.StringWidth(string(plain(prefix)))
		r.lines = append(r.lines, append(append([]srune{}, prefix...), runesOf(strings.Repeat("─", max(0, r.w-used)), st(colDim, colBG))...))
	case *east.Table:
		r.table(b, prefix)
	case *ast.HTMLBlock:
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			r.wrap(runesOf(strings.TrimRight(string(seg.Value(r.src)), "\n"), st(colDim, colBG)), prefix, cont)
			prefix = cont
		}
	default:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			r.block(c, prefix, cont)
			prefix = cont
		}
	}
}

func cvTruncate(s string, w int) string { return cv.Truncate(s, w) }

func plain(rs []srune) []rune {
	out := make([]rune, len(rs))
	for i, x := range rs {
		out[i] = x.r
	}
	return out
}

func isASCIIWord(r rune) bool { return r < 128 && r != ' ' }

// inline flattens inline content into styled runes.
func (r *docRenderer) inline(n ast.Node, base cv.Style) []srune {
	var out []srune
	var walk func(n ast.Node, sty cv.Style)
	walk = func(n ast.Node, sty cv.Style) {
		dim := st(colDim, sty.BG)
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch x := c.(type) {
			case *ast.Text:
				out = append(out, runesOf(string(x.Segment.Value(r.src)), sty)...)
				if x.HardLineBreak() {
					out = append(out, srune{'\n', sty})
				} else if x.SoftLineBreak() && len(out) > 0 && isASCIIWord(out[len(out)-1].r) {
					out = append(out, srune{' ', sty})
				}
			case *ast.String:
				out = append(out, runesOf(string(x.Value), sty)...)
			case *mathSpan:
				m := sty
				m.FG, m.Italic = colMath, true
				out = append(out, runesOf(texToUnicode(x.tex), m)...)
			case *ast.Emphasis:
				s := sty
				if x.Level >= 2 {
					s.Bold = true
				} else {
					s.Italic = true
				}
				walk(x, s)
			case *ast.CodeSpan:
				s := sty
				s.FG, s.BG = colCode, colCodeBG
				walk(x, s)
			case *ast.Link:
				s := sty
				s.FG, s.Under = colAccent, true
				start := len(out)
				walk(x, s)
				r.links = append(r.links, docLink{text: string(plain(out[start:])), url: string(x.Destination)})
				out = append(out, runesOf(fmt.Sprintf("[%d]", len(r.links)), dim)...)
			case *ast.AutoLink:
				s := sty
				s.FG, s.Under = colAccent, true
				label := string(x.Label(r.src))
				out = append(out, runesOf(label, s)...)
				r.links = append(r.links, docLink{text: label, url: string(x.URL(r.src))})
				out = append(out, runesOf(fmt.Sprintf("[%d]", len(r.links)), dim)...)
			case *ast.Image:
				alt := strings.TrimSpace(string(plain(r.inline(x, sty))))
				out = append(out, runesOf(tr.T("[Image: %s]", alt), dim)...)
			case *east.Strikethrough:
				s := sty
				s.Dim = true
				walk(x, s)
			case *east.TaskCheckBox:
				box := "☐ "
				if x.IsChecked {
					box = "☑ "
				}
				out = append(out, runesOf(box, st(colAccent, sty.BG))...)
			case *ast.RawHTML:
				for i := 0; i < x.Segments.Len(); i++ {
					seg := x.Segments.At(i)
					out = append(out, runesOf(string(seg.Value(r.src)), dim)...)
				}
			default:
				walk(c, sty)
			}
		}
	}
	walk(n, base)
	return out
}

// wrap lays runes into lines of r.w cells; ASCII words move whole to the next
// line when possible, CJK breaks anywhere.
func (r *docRenderer) wrap(rs []srune, prefix, cont []srune) {
	start := len(r.lines)
	line := append([]srune{}, prefix...)
	lo := len(prefix)
	width := cv.StringWidth(string(plain(prefix)))
	contW := cv.StringWidth(string(plain(cont)))
	flush := func() {
		r.lines = append(r.lines, line)
		line, lo, width = append([]srune{}, cont...), len(cont), contW
	}
	for _, x := range rs {
		if x.r == '\n' {
			flush()
			continue
		}
		rw := cv.RuneWidth(x.r)
		if width+rw > r.w {
			var carry []srune
			if cv.NoLineStart(x.r) && len(line) > lo+1 {
				carry = append(carry, line[len(line)-1])
				line = line[:len(line)-1]
			} else if isASCIIWord(x.r) {
				i := len(line) - 1
				for i >= lo && isASCIIWord(line[i].r) {
					i--
				}
				if i >= lo && i < len(line)-1 && line[i].r == ' ' {
					carry = append(carry, line[i+1:]...)
					line = line[:i]
				}
			}
			flush()
			for _, c := range carry {
				line = append(line, c)
				width += cv.RuneWidth(c.r)
			}
			if x.r == ' ' {
				continue
			}
		}
		line = append(line, x)
		width += rw
	}
	if len(line) > lo || len(r.lines) == start {
		r.lines = append(r.lines, line)
	}
}

func (r *docRenderer) wrapCode(rs []srune, prefix, cont []srune, code cv.Style) {
	line := append([]srune{}, prefix...)
	width := cv.StringWidth(string(plain(prefix)))
	pad := func() {
		for ; width < r.w; width++ {
			line = append(line, srune{' ', code})
		}
	}
	for _, x := range rs {
		rw := cv.RuneWidth(x.r)
		if width+rw > r.w {
			pad()
			r.lines = append(r.lines, line)
			line = append([]srune{}, cont...)
			width = cv.StringWidth(string(plain(cont)))
		}
		line = append(line, x)
		width += rw
	}
	pad()
	r.lines = append(r.lines, line)
}

func (r *docRenderer) table(t *east.Table, prefix []srune) {
	var rows [][]string
	header := 0
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, strings.TrimSpace(string(plain(r.inline(cell, stBase)))))
		}
		if _, ok := row.(*east.TableHeader); ok {
			header = len(rows) + 1
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return
	}
	ncol := 0
	for _, row := range rows {
		ncol = max(ncol, len(row))
	}
	widths := make([]int, ncol)
	for _, row := range rows {
		for i, c := range row {
			widths[i] = max(widths[i], cv.StringWidth(c))
		}
	}
	avail := r.w - cv.StringWidth(string(plain(prefix))) - 3*(ncol-1)
	for total(widths) > avail {
		wi := 0
		for i := range widths {
			if widths[i] > widths[wi] {
				wi = i
			}
		}
		if widths[wi] <= 2 {
			break
		}
		widths[wi]--
	}
	sep := runesOf(" │ ", st(colDim, colBG))
	for ri, row := range rows {
		line := append([]srune{}, prefix...)
		s := stBase
		if ri < header {
			s = bold(stBase)
		}
		for i := 0; i < ncol; i++ {
			c := ""
			if i < len(row) {
				c = row[i]
			}
			c = cv.Truncate(c, widths[i])
			line = append(line, runesOf(c+strings.Repeat(" ", widths[i]-cv.StringWidth(c)), s)...)
			if i < ncol-1 {
				line = append(line, sep...)
			}
		}
		r.lines = append(r.lines, line)
		if ri == header-1 {
			var u []srune
			u = append(u, prefix...)
			for i := 0; i < ncol; i++ {
				u = append(u, runesOf(strings.Repeat("─", widths[i]), st(colDim, colBG))...)
				if i < ncol-1 {
					u = append(u, runesOf("─┼─", st(colDim, colBG))...)
				}
			}
			r.lines = append(r.lines, u)
		}
	}
}

func total(ws []int) int {
	s := 0
	for _, w := range ws {
		s += w
	}
	return s
}

func (c *Core) drawDoc(n *ui.Node, W int, focused bool) *cv.Canvas {
	key := fmt.Sprintf("%d\x00%s", W, n.Text)
	res := c.docCache[key]
	if res == nil {
		if len(c.docCache) > 64 {
			c.docCache = map[string]*docResult{}
		}
		res = renderDoc(n.Text, W-3)
		c.docCache[key] = res
	}
	b := cv.New(W, max(1, len(res.lines)), stBase)
	for y, line := range res.lines {
		x := 2
		for _, sr := range line {
			x += b.Text(x, y, string(sr.r), sr.st, W-x)
		}
	}
	if focused {
		for y := 0; y < b.H; y++ {
			b.Text(0, y, "▌", st(colAccent, colBG), 1)
		}
	}
	return b
}
