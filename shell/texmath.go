package shell

import (
	"strings"
	"unicode"
)

// Formulas ($…$, $$…$$, ```math) are shown as Unicode text, not typeset: a
// small screen has room for "x² + y² = r²" and "∑ᵢ aᵢ/n", not for stacked
// fractions. What has no Unicode form degrades to readable ASCII:
// x^{abc} becomes x^(abc), \frac{a+b}{2} becomes (a+b)/2.

var texSymbols = map[string]string{
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ", "epsilon": "ε", "varepsilon": "ε", "zeta": "ζ",
	"eta": "η", "theta": "θ", "vartheta": "θ", "iota": "ι", "kappa": "κ", "lambda": "λ", "mu": "μ", "nu": "ν",
	"xi": "ξ", "pi": "π", "rho": "ρ", "sigma": "σ", "tau": "τ", "upsilon": "υ", "phi": "φ", "varphi": "φ",
	"chi": "χ", "psi": "ψ", "omega": "ω",
	"Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ", "Xi": "Ξ", "Pi": "Π", "Sigma": "Σ", "Phi": "Φ",
	"Psi": "Ψ", "Omega": "Ω",
	"infty": "∞", "sum": "∑", "prod": "∏", "int": "∫", "oint": "∮", "partial": "∂", "nabla": "∇",
	"pm": "±", "mp": "∓", "times": "×", "cdot": "·", "div": "÷", "ast": "∗", "circ": "∘", "bullet": "•",
	"le": "≤", "leq": "≤", "ge": "≥", "geq": "≥", "ne": "≠", "neq": "≠", "approx": "≈", "equiv": "≡",
	"sim": "∼", "simeq": "≃", "propto": "∝", "ll": "≪", "gg": "≫",
	"in": "∈", "notin": "∉", "ni": "∋", "subset": "⊂", "subseteq": "⊆", "supset": "⊃", "supseteq": "⊇",
	"cup": "∪", "cap": "∩", "emptyset": "∅", "varnothing": "∅", "setminus": "∖",
	"forall": "∀", "exists": "∃", "neg": "¬", "lnot": "¬", "land": "∧", "wedge": "∧", "lor": "∨", "vee": "∨",
	"oplus": "⊕", "otimes": "⊗", "perp": "⊥", "parallel": "∥", "angle": "∠", "degree": "°",
	"to": "→", "rightarrow": "→", "leftarrow": "←", "leftrightarrow": "↔", "mapsto": "↦",
	"Rightarrow": "⇒", "implies": "⇒", "Leftarrow": "⇐", "Leftrightarrow": "⇔", "iff": "⇔",
	"uparrow": "↑", "downarrow": "↓",
	"ldots": "…", "cdots": "⋯", "dots": "…", "vdots": "⋮", "prime": "′", "ell": "ℓ", "hbar": "ħ",
	"langle": "⟨", "rangle": "⟩", "lfloor": "⌊", "rfloor": "⌋", "lceil": "⌈", "rceil": "⌉",
	"{": "{", "}": "}", "|": "‖", "%": "%", "$": "$", "&": "&", "#": "#", "_": "_",
	",": " ", ";": " ", ":": " ", " ": " ", "quad": "  ", "qquad": "    ", "!": "",
}

var texDropped = map[string]bool{
	"left": true, "right": true, "big": true, "Big": true, "bigg": true, "Bigg": true,
	"bigl": true, "bigr": true, "Bigl": true, "Bigr": true, "displaystyle": true, "limits": true,
	"nolimits": true, "textstyle": true,
}

var texText = map[string]bool{
	"text": true, "textrm": true, "textit": true, "textbf": true, "mbox": true,
	"mathrm": true, "mathit": true, "mathbf": true, "mathsf": true, "mathtt": true, "operatorname": true,
	"boldsymbol": true, "hat": true, "widehat": true, "bar": true, "overline": true, "vec": true,
	"dot": true, "ddot": true, "tilde": true, "widetilde": true, "underline": true,
}

var (
	supMap = map[rune]rune{
		'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴', '5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
		'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾', 'n': 'ⁿ', 'i': 'ⁱ',
		'a': 'ᵃ', 'b': 'ᵇ', 'c': 'ᶜ', 'd': 'ᵈ', 'e': 'ᵉ', 'f': 'ᶠ', 'g': 'ᵍ', 'h': 'ʰ', 'j': 'ʲ', 'k': 'ᵏ',
		'l': 'ˡ', 'm': 'ᵐ', 'o': 'ᵒ', 'p': 'ᵖ', 'r': 'ʳ', 's': 'ˢ', 't': 'ᵗ', 'u': 'ᵘ', 'v': 'ᵛ', 'w': 'ʷ',
		'x': 'ˣ', 'y': 'ʸ', 'z': 'ᶻ', 'T': 'ᵀ', '′': '′', '*': '*',
	}
	subMap = map[rune]rune{
		'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄', '5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
		'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎', 'a': 'ₐ', 'e': 'ₑ', 'h': 'ₕ', 'i': 'ᵢ', 'j': 'ⱼ',
		'k': 'ₖ', 'l': 'ₗ', 'm': 'ₘ', 'n': 'ₙ', 'o': 'ₒ', 'p': 'ₚ', 'r': 'ᵣ', 's': 'ₛ', 't': 'ₜ', 'u': 'ᵤ',
		'v': 'ᵥ', 'x': 'ₓ', 'β': 'ᵦ', 'γ': 'ᵧ', 'ρ': 'ᵨ', 'φ': 'ᵩ', 'χ': 'ᵪ',
	}
	blackboard = map[rune]string{'R': "ℝ", 'N': "ℕ", 'Z': "ℤ", 'Q': "ℚ", 'C': "ℂ", 'P': "ℙ", 'H': "ℍ"}
)

type texParser struct {
	s []rune
	i int
}

// texToUnicode converts a LaTeX math fragment.
func texToUnicode(src string) string {
	p := &texParser{s: []rune(src)}
	out := p.seq(0)
	return strings.Join(strings.Fields(out), " ")
}

func (p *texParser) seq(close rune) string {
	var b strings.Builder
	for p.i < len(p.s) {
		if close != 0 && p.s[p.i] == close {
			p.i++
			return b.String()
		}
		b.WriteString(p.atom())
	}
	return b.String()
}

// arg reads one argument: a {group}, a \command or a single character.
func (p *texParser) arg() string {
	p.skipSpace()
	if p.i >= len(p.s) {
		return ""
	}
	if p.s[p.i] == '{' {
		p.i++
		return p.seq('}')
	}
	return p.atom()
}

func (p *texParser) rawArg() string {
	p.skipSpace()
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return p.arg()
	}
	depth, start := 0, p.i+1
	for ; p.i < len(p.s); p.i++ {
		switch p.s[p.i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				out := string(p.s[start:p.i])
				p.i++
				return out
			}
		}
	}
	return string(p.s[start:])
}

func (p *texParser) skipSpace() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *texParser) atom() string {
	c := p.s[p.i]
	switch c {
	case '\\':
		return p.command()
	case '{':
		p.i++
		return p.seq('}')
	case '^':
		p.i++
		return script(p.arg(), supMap, "^")
	case '_':
		p.i++
		return script(p.arg(), subMap, "_")
	case '&':
		p.i++
		return " "
	case '~':
		p.i++
		return " "
	case '\'':
		p.i++
		return "′"
	}
	p.i++
	return string(c)
}

func (p *texParser) command() string {
	p.i++
	if p.i >= len(p.s) {
		return ""
	}
	start := p.i
	if unicode.IsLetter(p.s[p.i]) {
		for p.i < len(p.s) && unicode.IsLetter(p.s[p.i]) {
			p.i++
		}
	} else {
		p.i++
	}
	name := string(p.s[start:p.i])
	switch {
	case name == "\\":
		return "; "
	case name == "frac" || name == "dfrac" || name == "tfrac":
		num, den := p.arg(), p.arg()
		return group(num) + "/" + group(den)
	case name == "sqrt":
		root := ""
		if p.i < len(p.s) && p.s[p.i] == '[' {
			end := p.i + 1
			for end < len(p.s) && p.s[end] != ']' {
				end++
			}
			root = string(p.s[p.i+1 : min(end, len(p.s))])
			p.i = min(end+1, len(p.s))
		}
		body := group(p.arg())
		switch root {
		case "":
			return "√" + body
		case "3":
			return "∛" + body
		case "4":
			return "∜" + body
		}
		return script(root, supMap, "^") + "√" + body
	case name == "mathbb":
		var b strings.Builder
		for _, r := range p.arg() {
			if s, ok := blackboard[r]; ok {
				b.WriteString(s)
			} else {
				b.WriteRune(r)
			}
		}
		return b.String()
	case name == "text" || name == "textrm" || name == "mbox":
		return p.rawArg()
	case texText[name]:
		return p.arg()
	case texDropped[name]:
		return ""
	}
	if s, ok := texSymbols[name]; ok {
		return s
	}
	return name
}

// group parenthesises compound terms so a/b stays unambiguous.
func group(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= 1 || !strings.ContainsAny(s, " +-−±·×/=<>≤≥") {
		return s
	}
	return "(" + s + ")"
}

func script(s string, m map[rune]rune, mark string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		u, ok := m[r]
		if !ok {
			if len([]rune(s)) == 1 {
				return mark + s
			}
			return mark + "(" + s + ")"
		}
		b.WriteRune(u)
	}
	return b.String()
}
