package shell

import (
	"strings"
	"testing"

	cv "github.com/kario-os/kario/canvas"
)

func artText(a *mermaidArt) string {
	var b strings.Builder
	for _, l := range a.lines {
		b.WriteString(string(plain(l)) + "\n")
	}
	return b.String()
}

// The labels are Chinese, two columns a character: "user scans", "merchant
// trusted?", "confirm payment", "warn of the risk".
func TestMermaidFlowchartCJK(t *testing.T) {
	art, note := renderMermaid("flowchart TD\n  A[\u7528\u6237\u626b\u7801] --> B{\u5546\u6237\u53ef\u4fe1?}\n  B -->|\u662f| C[\u786e\u8ba4\u4ed8\u6b3e]\n  B -->|\u5426| D[\u63d0\u793a\u98ce\u9669]", 37)
	if art == nil {
		t.Fatalf("not rendered: %s", note)
	}
	text := artText(art)
	for _, want := range []string{"\u7528\u6237\u626b\u7801", "\u786e\u8ba4\u4ed8\u6b3e", "\u63d0\u793a\u98ce\u9669", "▼"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	for _, l := range art.lines {
		if w := cv.StringWidth(string(plain(l))); w > 37 {
			t.Fatalf("line wider than screen (%d): %q", w, string(plain(l)))
		}
	}
}

// Six two-character Chinese steps in a row are too wide for 37 columns.
func TestMermaidWideFallsBackToVertical(t *testing.T) {
	art, note := renderMermaid("flowchart LR\n A[\u5f00\u59cb] --> B[\u626b\u7801] --> C[\u786e\u8ba4] --> D[\u652f\u4ed8] --> E[\u5b8c\u6210] --> F[\u8bc4\u4ef7]", 37)
	if art == nil || note != tr.T("(The screen is narrow; laid out top to bottom)") {
		t.Fatalf("expected vertical fallback, got note %q", note)
	}
}

func TestMermaidDiagramKinds(t *testing.T) {
	for kind, src := range map[string]string{
		"state":    "stateDiagram-v2\n [*] --> Unpaid\n Unpaid --> Done: pay\n Done --> [*]",
		"class":    "classDiagram\n class Order {\n  +amount\n  +pay()\n }\n Order <|-- DeliveryOrder",
		"er":       "erDiagram\n User ||--o{ Order : places",
		"pie":      "pie title Spending\n \"Food\" : 40\n \"Transport\" : 20",
		"mindmap":  "mindmap\n  root((Phone))\n    Calls\n    Payments",
		"timeline": "timeline\n 2026 : Prototype\n 2027 : Production",
		"gitgraph": "gitGraph\n commit\n branch dev\n commit\n checkout main\n merge dev",
	} {
		if art, note := renderMermaid(src, 60); art == nil {
			t.Errorf("%s not rendered: %s", kind, note)
		}
	}
}

func TestMermaidInDoc(t *testing.T) {
	res := renderDoc("# Flow\n\n```mermaid\nsequenceDiagram\n User->>Wallet: scan\n Wallet-->>User: result\n```\n", 37)
	var all strings.Builder
	for _, l := range res.lines {
		all.WriteString(string(plain(l)) + "\n")
	}
	if !strings.Contains(all.String(), "Wallet") || strings.Contains(all.String(), "sequenceDiagram") {
		t.Fatalf("mermaid fence not drawn:\n%s", all.String())
	}
}
