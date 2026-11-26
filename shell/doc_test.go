package shell

import (
	"strings"
	"testing"
)

func docText(res *docResult) []string {
	var out []string
	for _, l := range res.lines {
		out = append(out, strings.TrimRight(string(plain(l)), " "))
	}
	return out
}

func TestTexToUnicode(t *testing.T) {
	for src, want := range map[string]string{
		`x^2 + y^2 = r^2`:                                "x² + y² = r²",
		`\sum_{i=1}^{n} a_i`:                             "∑ᵢ₌₁ⁿ aᵢ",
		`\frac{a+b}{2} \le \sqrt{x+1}`:                   "(a+b)/2 ≤ √(x+1)",
		`\alpha \to \infty`:                              "α → ∞",
		`x \in \mathbb{R}`:                               "x ∈ ℝ",
		`e^{i\pi} + 1 = 0`:                               "e^(iπ) + 1 = 0",
		`\text{price} = 3 \times q`:                      "price = 3 × q",
		`\left( \frac{1}{n} \right)^{2}`:                 "( 1/n )²",
		`\log_{10} x \neq \sqrt[3]{8}`:                   "log₁₀ x ≠ ∛8",
		`f'(x) = \lim_{h \to 0} \frac{f(x+h) - f(x)}{h}`: "f′(x) = lim_(h → 0) (f(x+h) - f(x))/h",
	} {
		if got := texToUnicode(src); got != want {
			t.Errorf("%s: got %q, want %q", src, got, want)
		}
	}
}

func TestDocMath(t *testing.T) {
	res := renderDoc("Area $\\pi r^2$, price $5 to $10.\n\n$$E = mc^2$$\n\n```math\na^2 + b^2 = c^2\n```", 40)
	lines := docText(res)
	if !strings.Contains(lines[0], "Area π r²") || !strings.Contains(lines[0], "price $5 to $10") {
		t.Fatalf("inline: %q", lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "  E = mc²") || !strings.Contains(joined, "  a² + b² = c²") {
		t.Fatalf("display: %q", lines)
	}
	for _, l := range res.lines {
		for _, r := range l {
			if r.r == 'π' && (r.st.FG != colMath || !r.st.Italic) {
				t.Fatalf("math style: %+v", r.st)
			}
		}
	}
}

func TestDocHighlight(t *testing.T) {
	res := renderDoc("```go\n// add two\nfunc add(a int) int { return a + 1 } /* x\ny */ s := \"go\"\n```", 80)
	colour := map[string]any{}
	for _, l := range res.lines {
		text := string(plain(l))
		for _, w := range []string{"func", "return", "add(", "1", "\"go\"", "// add two", "y */"} {
			if i := strings.Index(text, w); i >= 0 && colour[w] == nil {
				colour[w] = l[len([]rune(text[:i]))].st.FG
			}
		}
	}
	want := map[string]any{
		"func": colKeyword, "return": colKeyword, "add(": colCodeText, "1": colNumber,
		"\"go\"": colString, "// add two": colComment, "y */": colComment,
	}
	for w, c := range want {
		if colour[w] != c {
			t.Errorf("%s: got %v, want %v", w, colour[w], c)
		}
	}
	plainRes := renderDoc("```\nfunc x\n```", 40)
	if plainRes.lines[0][1].st.FG != colCode {
		t.Fatalf("unknown language should stay plain")
	}
}
