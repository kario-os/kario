package reader

import (
	"net/url"
	"strings"
	"testing"
)

const page = `<!doctype html><html><head><title>Platform update | Hongyan official account</title>
<script>track()</script><style>body{}</style></head>
<body>
<header><nav><a href="/">Home</a> <a href="/login">Log in</a></nav></header>
<aside><p>Advertisement: a very, very long advertising text that has no place in reading mode, even though it is a little longer than a paragraph of the article.</p></aside>
<div class="content">
  <h1>The second spring of keyboard phones</h1>
  <p>This is the first paragraph, on <strong>why</strong> small keyboard phones are back; <a href="/arch">platform architecture</a> is another article.</p>
  <p>Second paragraph: the semantic UI makes <code>phone.Ui</code> easy to use on a 9-key pad too.</p>
  <img src="/a.png" alt="Prototype photo">
  <img src="/b.png">
  <ul><li>Long battery life</li><li>Reliable keys</li></ul>
  <table><tr><th>Model</th><th>Screen</th></tr><tr><td>Bar | T9</td><td>2.8 in</td></tr></table>
  <form><input name="q"><button>Search</button></form>
</div>
<footer><p>All rights reserved; name the source when reposting.</p></footer>
</body></html>`

func TestExtract(t *testing.T) {
	base, _ := url.Parse("http://example.com/post/1")
	p, err := Extract(strings.NewReader(page), base)
	if err != nil {
		t.Fatal(err)
	}
	md := p.Markdown
	for _, want := range []string{"# The second spring of keyboard phones", "**why**", "[platform architecture](http://example.com/arch)",
		"`phone.Ui`", tr.T("[%s]", "Prototype photo"), tr.T("[%s]", tr.T("Image")), "- Long battery life", "| Model | Screen |",
		"| Bar " + cellBar + " T9 | 2.8 in |"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	for _, not := range []string{"track()", "Advertisement", "Log in", "All rights reserved", "Search"} {
		if strings.Contains(md, not) {
			t.Errorf("%q leaked into:\n%s", not, md)
		}
	}
	if p.Title != "Platform update | Hongyan official account" || p.Links != 1 {
		t.Fatalf("title %q, %d links", p.Title, p.Links)
	}
	// Only web links: no javascript: or file: in the menu.
	p, _ = Extract(strings.NewReader(`<article><p><a href="javascript:alert(1)">x</a> <a href="file:///etc/passwd">y</a> text</p></article>`), base)
	if p.Links != 0 || strings.Contains(p.Markdown, "](") {
		t.Fatalf("unsafe links kept: %s", p.Markdown)
	}
}
