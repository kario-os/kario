package shell

import (
	"strings"
	"unicode"

	cv "github.com/kario-os/kario/canvas"
)

// Code blocks are coloured by a small built-in lexer per language: keywords,
// strings, comments and numbers. It is line based, with block comments
// carried across lines; anything finer (types, nested template strings) is
// left plain. A full highlighter (chroma) would add megabytes to the Shell.

var (
	colCodeText = cv.RGB{220, 223, 230}
	colKeyword  = cv.RGB{198, 146, 234}
	colString   = cv.RGB{152, 195, 121}
	colComment  = cv.RGB{120, 128, 150}
	colNumber   = cv.RGB{209, 154, 102}
)

type lexer struct {
	keywords   map[string]bool
	line       []string
	blockOpen  string
	blockClose string
	quotes     string
	keys       bool
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var (
	cKeywords  = "auto break case char const continue default do double else enum extern float for goto if inline int long register return short signed sizeof static struct switch typedef union unsigned void volatile while bool true false NULL nullptr"
	jsKeywords = "async await break case catch class const continue debugger default delete do else export extends false finally for from function if import in instanceof interface let new null of return static super switch this throw true try type typeof undefined var void while yield enum implements private protected public readonly as any number string boolean"
	lexers     = map[string]*lexer{
		"go": {keywords: words("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false iota"),
			line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`"},
		"js": {keywords: words(jsKeywords), line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`"},
		"python": {keywords: words("and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield self"),
			line: []string{"#"}, quotes: "\"'"},
		"rust": {keywords: words("as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while Some None Ok Err"),
			line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\""},
		"c": {keywords: words(cKeywords), line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'"},
		"cpp": {keywords: words(cKeywords + " class namespace template typename public private protected virtual override new delete this using std auto constexpr noexcept"),
			line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'"},
		"java": {keywords: words("abstract boolean break byte case catch char class const continue default do double else enum extends final finally float for if implements import instanceof int interface long new null package private protected public return short static super switch this throw throws true false try void while var"),
			line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'"},
		"sh":   {keywords: words("if then else elif fi for while until do done case esac in function return local export set echo exit"), line: []string{"#"}, quotes: "\"'"},
		"json": {keywords: words("true false null"), quotes: "\""},
		"toml": {keywords: words("true false"), line: []string{"#"}, quotes: "\"'", keys: true},
		"yaml": {keywords: words("true false null yes no"), line: []string{"#"}, quotes: "\"'", keys: true},
		"sql": {keywords: words("select from where insert into values update set delete create table index drop alter and or not null primary key join left right inner outer on group by order having limit as distinct count SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE INDEX DROP ALTER AND OR NOT NULL PRIMARY KEY JOIN LEFT RIGHT INNER OUTER ON GROUP BY ORDER HAVING LIMIT AS DISTINCT COUNT"),
			line: []string{"--"}, blockOpen: "/*", blockClose: "*/", quotes: "'\""},
	}
	langAliases = map[string]string{
		"golang": "go", "javascript": "js", "jsx": "js", "ts": "js", "typescript": "js", "tsx": "js",
		"py": "python", "rs": "rust", "h": "c", "c++": "cpp", "cc": "cpp", "hpp": "cpp", "cxx": "cpp",
		"kotlin": "java", "kt": "java", "bash": "sh", "shell": "sh", "zsh": "sh", "console": "sh",
		"ini": "toml", "yml": "yaml",
	}
)

func lexerFor(lang string) *lexer {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if a, ok := langAliases[lang]; ok {
		lang = a
	}
	return lexers[lang]
}

func identRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// highlight colours one line; inBlock carries an open block comment to the
// next line.
func (lx *lexer) highlight(s string, inBlock *bool, bg cv.RGB) []srune {
	rs := []rune(s)
	out := make([]srune, 0, len(rs))
	emit := func(part []rune, fg cv.RGB, italic bool) {
		sty := cv.Style{FG: fg, BG: bg, Italic: italic}
		for _, r := range part {
			out = append(out, srune{r, sty})
		}
	}
	has := func(i int, p string) bool { return p != "" && strings.HasPrefix(string(rs[i:]), p) }
	keyEnd := -1
	if lx.keys {
		t := strings.TrimLeft(s, " ")
		if k := strings.IndexAny(t, ":="); k > 0 && !strings.ContainsAny(t[:k], "\"'#[ ") {
			lead := len([]rune(s)) - len([]rune(t))
			keyEnd = lead + len([]rune(t[:k]))
		}
	}
	for i := 0; i < len(rs); {
		if *inBlock {
			j := strings.Index(string(rs[i:]), lx.blockClose)
			end := len(rs)
			if j >= 0 {
				end = i + len([]rune(string(rs[i:])[:j])) + len([]rune(lx.blockClose))
				*inBlock = false
			}
			emit(rs[i:end], colComment, true)
			i = end
			continue
		}
		if i < keyEnd {
			emit(rs[i:keyEnd], colKeyword, false)
			i = keyEnd
			continue
		}
		comment := false
		for _, p := range lx.line {
			if has(i, p) {
				comment = true
			}
		}
		if comment {
			emit(rs[i:], colComment, true)
			break
		}
		if has(i, lx.blockOpen) {
			*inBlock = true
			emit([]rune(lx.blockOpen), colComment, true)
			i += len([]rune(lx.blockOpen))
			continue
		}
		r := rs[i]
		if strings.ContainsRune(lx.quotes, r) {
			j := i + 1
			for j < len(rs) && rs[j] != r {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(rs))
			emit(rs[i:j], colString, false)
			i = j
			continue
		}
		if unicode.IsDigit(r) && (i == 0 || !identRune(rs[i-1])) {
			j := i
			for j < len(rs) && (identRune(rs[j]) || rs[j] == '.') {
				j++
			}
			emit(rs[i:j], colNumber, false)
			i = j
			continue
		}
		if identRune(r) {
			j := i
			for j < len(rs) && identRune(rs[j]) {
				j++
			}
			fg := colCodeText
			if lx.keywords[string(rs[i:j])] {
				fg = colKeyword
			}
			emit(rs[i:j], fg, false)
			i = j
			continue
		}
		emit(rs[i:i+1], colCodeText, false)
		i++
	}
	return out
}
