package main

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kario-os/kario/ui"
)

// cjk is Chinese JSX text: it must come through the transform as it is.
const cjk = "\u5171 {2} \u6761"

// render may give one element instead of a list: the page has that node.
func TestTSXOneElement(t *testing.T) {
	src := `Page("index", { render() { return <text>hello</text>; } });`
	p := testProgram(t, "one", fstest.MapFS{"app.json": {Data: []byte(`{"name": "one"}`)}, "app.tsx": {Data: []byte(src)}})
	s := startHost(t, p)
	if nodes := s.open("one").Body; len(nodes) != 1 || nodes[0].Kind != ui.KindText || nodes[0].Text != "hello" {
		t.Fatalf("nodes: %+v", nodes)
	}
}

func TestTSXBuildsUiNodes(t *testing.T) {
	src := `
function Row(p: { n: string; children?: unknown[] }) {
  return <item id={p.n} title={p.n} />;
}
const out = (
  <>
    <section>` + cjk + `</section>
    {["a", "b"].map((n) => <Row n={n} />)}
    {false}
    <button id="go">Go</button>
  </>
);
Page("index", { render() { return out; } });`
	p := testProgram(t, "tsx", fstest.MapFS{"app.json": {Data: []byte(`{"name": "tsx"}`)}, "app.tsx": {Data: []byte(src)}})
	s := startHost(t, p)
	nodes := s.open("tsx").Body
	if len(nodes) != 4 {
		t.Fatalf("nodes: %v", nodes)
	}
	if nodes[0].Kind != ui.KindSection || nodes[0].Text != strings.Replace(cjk, "{2}", "2", 1) {
		t.Fatalf("section: %+v", nodes[0])
	}
	if nodes[2].Kind != ui.KindItem || nodes[2].ID != "b" {
		t.Fatalf("component: %+v", nodes[2])
	}
	if acts := nodes[3].Actions; nodes[3].Text != "Go" || len(acts) != 1 || acts[0].ID != "go" {
		t.Fatalf("button: %+v", nodes[3])
	}
}
