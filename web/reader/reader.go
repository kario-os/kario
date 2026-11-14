// Package reader is the web view's text side (reading mode, 14 §5): a page's
// main content as Markdown for the system's doc node, which lays it out and
// puts the links in the menu. It runs in the app that shows the page, inside
// that app's sandbox, so untrusted HTML is never parsed by the system.
//
// The main content is the element whose own paragraphs hold the most text
// (an <article> or <main> wins when there is one); scripts, styles, forms,
// navigation, headers, footers and asides are dropped.
package reader

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type Page struct {
	Title    string
	Markdown string
	Links    int
}

// MaxBytes bounds the HTML read.
const MaxBytes = 2 << 20

// cellBar stands in for a "|" in a table cell, which would end the cell:
// U+FF0F FULLWIDTH SOLIDUS.
const cellBar = "\uff0f"

var dropped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Iframe: true,
	atom.Form: true, atom.Button: true, atom.Input: true, atom.Select: true, atom.Textarea: true,
	atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Aside: true, atom.Svg: true,
}

// Extract reads a page; base resolves relative links and images.
func Extract(r io.Reader, base *url.URL) (*Page, error) {
	doc, err := html.Parse(io.LimitReader(r, MaxBytes))
	if err != nil {
		return nil, err
	}
	p := &Page{Title: strings.TrimSpace(textOf(find(doc, atom.Title)))}
	root := mainContent(doc)
	w := &writer{base: base}
	w.block(root)
	p.Markdown = strings.TrimSpace(collapseBlank(w.b.String()))
	if p.Title == "" {
		p.Title = strings.TrimSpace(textOf(find(root, atom.H1)))
	}
	p.Links = w.links
	return p, nil
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, a); f != nil {
			return f
		}
	}
	return nil
}

func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && dropped[n.DataAtom] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// mainContent: an <article> or <main>, else the element whose direct <p>
// children hold the most text, else <body>.
func mainContent(doc *html.Node) *html.Node {
	for _, a := range []atom.Atom{atom.Article, atom.Main} {
		if n := find(doc, a); n != nil {
			return n
		}
	}
	var best *html.Node
	bestScore := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && dropped[n.DataAtom] {
			return
		}
		score := 0
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && (c.DataAtom == atom.P || c.DataAtom == atom.Pre || c.DataAtom == atom.Blockquote) {
				score += len([]rune(textOf(c)))
			}
		}
		if score > bestScore {
			best, bestScore = n, score
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if best != nil {
		return best
	}
	if b := find(doc, atom.Body); b != nil {
		return b
	}
	return doc
}

type writer struct {
	b     strings.Builder
	base  *url.URL
	links int
	list  []string // "-" or a number per open list
}

func (w *writer) resolve(href string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	if w.base != nil {
		u = w.base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// block writes block-level content, each block separated by a blank line.
func (w *writer) block(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			if t := strings.TrimSpace(c.Data); t != "" {
				w.b.WriteString(strings.Join(strings.Fields(t), " ") + "\n\n")
			}
			continue
		}
		if c.Type != html.ElementNode || dropped[c.DataAtom] {
			continue
		}
		switch c.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			level := int(c.Data[1] - '0')
			if t := w.inline(c); t != "" {
				w.b.WriteString(strings.Repeat("#", level) + " " + t + "\n\n")
			}
		case atom.P:
			if t := w.inline(c); t != "" {
				w.b.WriteString(t + "\n\n")
			}
		case atom.Ul, atom.Ol:
			w.list = append(w.list, map[bool]string{true: "-", false: "1."}[c.DataAtom == atom.Ul])
			for li := c.FirstChild; li != nil; li = li.NextSibling {
				if li.Type == html.ElementNode && li.DataAtom == atom.Li {
					if t := w.inline(li); t != "" {
						w.b.WriteString(strings.Repeat("  ", len(w.list)-1) + w.list[len(w.list)-1] + " " + t + "\n")
					}
				}
			}
			w.list = w.list[:len(w.list)-1]
			w.b.WriteString("\n")
		case atom.Pre:
			w.b.WriteString("```\n" + strings.TrimRight(rawText(c), "\n") + "\n```\n\n")
		case atom.Blockquote:
			if t := w.inline(c); t != "" {
				w.b.WriteString("> " + t + "\n\n")
			}
		case atom.Img:
			w.image(c)
		case atom.Table:
			w.table(c)
		case atom.Hr:
			w.b.WriteString("---\n\n")
		default:
			w.block(c)
		}
	}
}

func (w *writer) image(n *html.Node) {
	alt := strings.TrimSpace(attr(n, "alt"))
	if alt == "" {
		alt = tr.T("Image")
	}
	w.b.WriteString(tr.T("[%s]", alt) + "\n\n")
}

// inline is a block's text with links as Markdown links.
func (w *writer) inline(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
			return
		case n.Type != html.ElementNode || dropped[n.DataAtom]:
			return
		case n.DataAtom == atom.A:
			text := textOf(n)
			if href := w.resolve(attr(n, "href")); href != "" && text != "" {
				w.links++
				b.WriteString("[" + strings.NewReplacer("[", "(", "]", ")").Replace(text) + "](" + href + ")")
				return
			}
		case n.DataAtom == atom.Img:
			alt := strings.TrimSpace(attr(n, "alt"))
			if alt != "" {
				b.WriteString(tr.T("[%s]", alt))
			}
			return
		case n.DataAtom == atom.Br:
			b.WriteString(" ")
			return
		case n.DataAtom == atom.Strong || n.DataAtom == atom.B:
			if t := textOf(n); t != "" {
				b.WriteString("**" + t + "**")
			}
			return
		case n.DataAtom == atom.Code:
			if t := textOf(n); t != "" {
				b.WriteString("`" + t + "`")
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func rawText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// table writes a Markdown table from the rows' cells.
func (w *writer) table(n *html.Node) {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
			var row []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
					row = append(row, strings.ReplaceAll(w.inline(c), "|", cellBar))
				}
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	for i, r := range rows {
		for len(r) < cols {
			r = append(r, "")
		}
		fmt.Fprintf(&w.b, "| %s |\n", strings.Join(r, " | "))
		if i == 0 {
			w.b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
		}
	}
	w.b.WriteString("\n")
}

func collapseBlank(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
