package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// cjk is Chinese JSX text: it must come through the transform as it is.
const cjk = "\u5171 {2} \u6761"

func TestTSXBuildsUiNodes(t *testing.T) {
	code, err := transform(`
function Row(p: { n: string; children?: unknown[] }) {
  return <item id={p.n} title={p.n} />;
}
const out = (
  <>
    <section>`+cjk+`</section>
    {["a", "b"].map((n) => <Row n={n} />)}
    {false}
    <button id="go">Go</button>
  </>
);`, "t/app.tsx", api.LoaderTSX)
	if err != nil {
		t.Fatal(err)
	}
	rt := goja.New()
	if _, err := rt.RunString(prelude + code + "\nvar json = JSON.stringify(out);"); err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	if err := json.Unmarshal([]byte(rt.Get("json").String()), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("nodes: %v", nodes)
	}
	if nodes[0]["kind"] != "section" || nodes[0]["text"] != strings.Replace(cjk, "{2}", "2", 1) {
		t.Fatalf("section: %v", nodes[0])
	}
	if nodes[2]["kind"] != "item" || nodes[2]["id"] != "b" {
		t.Fatalf("component: %v", nodes[2])
	}
	acts, _ := nodes[3]["actions"].([]any)
	if nodes[3]["text"] != "Go" || len(acts) != 1 || acts[0].(map[string]any)["id"] != "go" {
		t.Fatalf("button: %v", nodes[3])
	}
}
