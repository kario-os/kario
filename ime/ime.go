package ime

import (
	"strings"
	"time"
	"unicode"
)

type Keypad string

const (
	QWERTY Keypad = "qwerty"
	T9Pad  Keypad = "t9"
)

// Mode is the input mode. Keys follow Nokia's conventions (docs/18): on the
// 9-key pad # cycles pinyin → strokes → smart English → Abc → abc → ABC (as
// on Nokia's Chinese phones) and a long # toggles 123; on a keyboard
// Ctrl+Space toggles Chinese / English.
type Mode string

const (
	Zh     Mode = "zh"     // pinyin
	Stroke Mode = "stroke" // 9-key: 1 horizontal, 2 vertical, 3 left-falling, 4 dot, 5 turning, 6 any stroke
	En     Mode = "en"     // English: predictive on the 9-key pad, completion on a keyboard
	Abc    Mode = "Abc"    // multi-tap, sentence case
	Lower  Mode = "abc"    // multi-tap
	Upper  Mode = "ABC"    // multi-tap
	Num    Mode = "num"
)

var t9Cycle = []Mode{Zh, Stroke, En, Abc, Lower, Upper}

// Key is what the shell forwards while a text input has focus.
type Key struct {
	Name string // rune, backspace, enter, esc, left, right, up, down, ime, sym
	Rune rune
	// Long marks a held key: a digit types itself, # toggles 123, * opens
	// the symbol table.
	Long bool
	// Before is the text before the cursor: punctuation after a digit stays
	// half-width ("6.5"), and Abc capitalises the start of a sentence.
	Before string
}

func (k Key) prev() rune {
	r := []rune(k.Before)
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1]
}

const pageSize = 5

// multiTap is the ITU-T E.161 letter assignment with Nokia's key-1
// punctuation; repeated presses within the timeout step through a key.
var multiTap = map[rune]string{
	'1': ".,?!'\"-()@/:_;1", '2': "abc2", '3': "def3", '4': "ghi4", '5': "jkl5",
	'6': "mno6", '7': "pqrs7", '8': "tuv8", '9': "wxyz9",
}

const multiTapTimeout = time.Second

type Engine struct {
	Keypad Keypad
	Mode   Mode
	// WantSymbols is set when a key asks for the symbol table (* on the
	// 9-key pad, Chr/Sym on a keyboard); the shell opens it and clears it.
	WantSymbols bool

	buf      string
	punct    bool
	cands    []Candidate
	sel      int
	spells   []string
	spellSel int
	onSpell  bool
	spelling string
	assoc    bool
	assocNav bool
	numbered bool // 9-key: digits pick candidates (after Down), as on S60
	before   Mode // the mode a long # returns to from 123
	tap      rune // pending multi-tap key
	tapIdx   int
	tapAt    time.Time
	tapUpper bool
	now      func() time.Time
	// Fuzzy enables fuzzy pinyin (full keyboard).
	Fuzzy bool
	user  *userDict
	// phrase: what was committed in pieces of one input so far, to learn
	// the whole as a word.
	phraseText, phrasePy string
	lastEn               string
}

// SetUserDict keeps the user dictionary (picks, learnt phrases, English
// next words) in path.
func (e *Engine) SetUserDict(path string) { e.user = openUser(path) }

func New(k Keypad) *Engine { return &Engine{Keypad: k, Mode: Zh, now: time.Now} }

func (e *Engine) Composing() bool { return e.buf != "" || e.punct || e.tap != 0 }

// Active reports whether a candidate bar should be shown.
func (e *Engine) Active() bool {
	return e.Composing() && e.tap == 0 || e.assoc && len(e.cands) > 0
}

func (e *Engine) Cancel() {
	e.buf, e.punct, e.cands, e.sel, e.assoc, e.assocNav = "", false, nil, 0, false, false
	e.spells, e.spellSel, e.onSpell, e.spelling, e.numbered = nil, 0, false, "", false
	e.tap, e.tapIdx = 0, 0
}

func (e *Engine) SetKeypad(k Keypad) {
	e.Cancel()
	e.Keypad, e.Mode = k, Zh
}

func (e *Engine) SetMode(m Mode) {
	e.Cancel()
	e.Mode = m
}

func (e *Engine) multiTapMode() bool {
	return e.Keypad == T9Pad && (e.Mode == Abc || e.Mode == Lower || e.Mode == Upper)
}

func (e *Engine) cycleMode() {
	if e.Keypad != T9Pad {
		if e.Mode == Zh {
			e.SetMode(En)
		} else {
			e.SetMode(Zh)
		}
		return
	}
	for i, m := range t9Cycle {
		if m == e.Mode {
			e.SetMode(t9Cycle[(i+1)%len(t9Cycle)])
			return
		}
	}
	e.SetMode(Zh)
}

func (e *Engine) toggleNumbers() {
	if e.Mode == Num {
		m := e.before
		if m == "" {
			m = Zh
		}
		e.SetMode(m)
		return
	}
	prev := e.Mode
	e.SetMode(Num)
	e.before = prev
}

func (e *Engine) Indicator() string {
	if e.Keypad == T9Pad {
		return map[Mode]string{Zh: tr.T("Pinyin"), Stroke: tr.T("Strokes"), En: tr.T("Smart English"), Abc: "Abc", Lower: "abc", Upper: "ABC", Num: "123"}[e.Mode]
	}
	if e.Mode == Zh {
		return tr.T("Zh")
	}
	if e.Mode == Num {
		return "123"
	}
	return tr.T("En")
}

// Preedit is the composition shown inline in the input field.
func (e *Engine) Preedit() string {
	switch {
	case e.tap != 0:
		return e.tapChar()
	case e.punct:
		return ""
	case e.Keypad == T9Pad && e.Mode == Zh:
		if e.spelling != "" {
			return e.spelling
		}
		if len(e.spells) > 0 {
			return e.spells[0]
		}
	case e.Keypad == T9Pad && e.Mode == En && len(e.cands) > 0:
		return e.cands[e.sel].Text
	case e.Mode == Stroke:
		return StrokeGlyphs(e.buf)
	}
	return e.buf
}

type Row struct {
	Items    []string `json:"items"`
	Sel      int      `json:"sel,omitempty"`
	Active   bool     `json:"active,omitempty"`
	Numbered bool     `json:"numbered,omitempty"`
	More     bool     `json:"more,omitempty"`
}

// State is everything the shell draws from; phone-imed returns it after
// every call so drawing never waits on the service.
type State struct {
	Keypad      Keypad `json:"keypad"`
	Mode        Mode   `json:"mode"`
	Composing   bool   `json:"composing,omitempty"`
	Active      bool   `json:"active,omitempty"`
	Preedit     string `json:"preedit,omitempty"`
	Indicator   string `json:"indicator"`
	Rows        []Row  `json:"rows,omitempty"`
	WantSymbols bool   `json:"want_symbols,omitempty"`
	// Tapping: a multi-tap letter is pending, so Tick may commit it.
	Tapping bool `json:"tapping,omitempty"`
}

func (e *Engine) State() State {
	return State{Keypad: e.Keypad, Mode: e.Mode, Composing: e.Composing(), Active: e.Active(), Preedit: e.Preedit(),
		Indicator: e.Indicator(), Rows: e.Rows(), WantSymbols: e.WantSymbols, Tapping: e.tap != 0}
}

// Rows returns the candidate bar: one page of candidates, plus the pinyin
// spelling row on a 9-key pad.
func (e *Engine) Rows() []Row {
	if !e.Active() {
		return nil
	}
	t9 := e.Keypad == T9Pad
	var rows []Row
	if t9 && e.Mode == Zh && len(e.spells) > 1 && !e.punct && !e.assoc {
		rows = append(rows, Row{Items: e.spells, Sel: e.spellSel, Active: e.onSpell})
	}
	start := e.sel / pageSize * pageSize
	end := min(len(e.cands), start+pageSize)
	page := make([]string, 0, pageSize)
	for _, c := range e.cands[start:end] {
		page = append(page, c.Text)
	}
	active := !e.onSpell && (!e.assoc || e.assocNav || t9)
	rows = append(rows, Row{Items: page, Sel: e.sel - start, Active: active,
		Numbered: !t9 || e.numbered, More: end < len(e.cands)})
	return rows
}

func (e *Engine) refresh() {
	d := load()
	t9 := e.Keypad == T9Pad
	if e.Mode == Stroke {
		e.cands = d.strokeCandidates(e.buf, 60)
		if e.user != nil {
			e.user.rank(e.cands)
		}
		if e.sel >= len(e.cands) {
			e.sel = 0
		}
		return
	}
	if e.Mode == En {
		e.cands = d.enCandidates(e.buf, t9)
	} else {
		e.cands = d.zhCandidates(e.buf, t9, e.spelling, e.Fuzzy)
		if e.user != nil && e.spelling == "" {
			var learnt []Candidate
			for _, w := range e.user.phrases(e.buf, t9) {
				learnt = append(learnt, Candidate{Text: w.Word, Pinyin: strings.ReplaceAll(w.Pinyin, " ", "'"), Consumed: len(e.buf)})
			}
			e.cands = append(learnt, without(e.cands, learnt)...)
		}
		if t9 && e.spelling == "" {
			e.spells = spellings(e.cands, 6)
			e.spellSel = 0
		}
	}
	if e.user != nil {
		e.user.rank(e.cands)
	}
	if e.sel >= len(e.cands) {
		e.sel = 0
	}
}

// commit takes the highlighted candidate; a partial match keeps the rest of
// the input composing.
func (e *Engine) commit(i int) string {
	if e.tap != 0 {
		return e.commitTap()
	}
	if e.punct {
		text := marks.punct[min(max(i, 0), len(marks.punct)-1)]
		e.Cancel()
		return text
	}
	if i < 0 || i >= len(e.cands) {
		text := e.buf
		e.Cancel()
		return text
	}
	c := e.cands[i]
	if e.assoc {
		e.Cancel()
		e.startAssoc(c.Text)
		return c.Text
	}
	rest := ""
	if c.Consumed < len(e.buf) {
		rest = strings.TrimLeft(e.buf[c.Consumed:], "'")
	}
	e.learn(c, rest != "")
	e.Cancel()
	if rest != "" {
		e.buf = rest
		e.refresh()
		return c.Text
	}
	e.startAssoc(c.Text)
	return c.Text
}

// learn records a pick: its count, the phrase being picked in pieces, and in
// English what followed the previous word.
func (e *Engine) learn(c Candidate, more bool) {
	if e.user == nil || e.Mode == Stroke {
		return
	}
	if e.Mode == En {
		e.user.picked(c.Text, "")
		e.user.followed(e.lastEn, c.Text)
		e.lastEn = c.Text
		return
	}
	py := strings.ReplaceAll(c.Pinyin, "'", " ")
	e.user.picked(c.Text, py)
	if more || e.phraseText != "" {
		e.phraseText += c.Text
		e.phrasePy = strings.TrimSpace(e.phrasePy + " " + py)
	}
	if !more && e.phraseText != "" {
		if e.phraseText != c.Text {
			e.user.picked(e.phraseText, e.phrasePy)
		}
		e.phraseText, e.phrasePy = "", ""
	}
}

// without drops the candidates already in front.
func without(cs, front []Candidate) []Candidate {
	out := cs[:0:0]
	for _, c := range cs {
		dup := false
		for _, f := range front {
			if f.Text == c.Text {
				dup = true
			}
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

func (e *Engine) startAssoc(committed string) {
	if e.Mode == En && e.user != nil {
		// Next-word prediction from what the user typed before.
		var cs []Candidate
		for _, w := range e.user.next(committed) {
			cs = append(cs, Candidate{Text: " " + w})
		}
		if len(cs) > 0 {
			e.assoc, e.cands, e.sel = true, cs, 0
		}
		return
	}
	if e.Mode != Zh && e.Mode != Stroke {
		return
	}
	if a := load().association(committed); len(a) > 0 {
		e.assoc, e.cands, e.sel = true, a, 0
	}
}

func (e *Engine) startPunct() {
	e.Cancel()
	e.punct = true
	for _, p := range marks.punct {
		e.cands = append(e.cands, Candidate{Text: p})
	}
}

func (e *Engine) tapChar() string {
	s := []rune(multiTap[e.tap])
	r := s[e.tapIdx%len(s)]
	if e.tapUpper {
		r = unicode.ToUpper(r)
	}
	return string(r)
}

func (e *Engine) commitTap() string {
	if e.tap == 0 {
		return ""
	}
	t := e.tapChar()
	e.tap, e.tapIdx = 0, 0
	return t
}

// Tick commits a multi-tap letter once its timeout has passed; the shell
// calls it periodically and inserts what it returns.
func (e *Engine) Tick() string {
	if e.tap != 0 && e.now().Sub(e.tapAt) >= multiTapTimeout {
		return e.commitTap()
	}
	return ""
}

func sentenceStart(before string) bool {
	t := []rune(strings.TrimRight(before, " "))
	return len(t) == 0 || strings.ContainsRune(marks.sentenceEnd, t[len(t)-1])
}

func (e *Engine) tapKey(k Key) (bool, string) {
	r := k.Rune
	switch {
	case k.Long && r >= '0' && r <= '9':
		return true, e.commitTap() + string(r)
	case r == '0':
		return true, e.commitTap() + " "
	case multiTap[r] != "":
		out := ""
		if e.tap == r && e.now().Sub(e.tapAt) < multiTapTimeout {
			e.tapIdx++
		} else {
			out = e.commitTap()
			e.tap, e.tapIdx = r, 0
			e.tapUpper = e.Mode == Upper || e.Mode == Abc && sentenceStart(k.Before+out)
		}
		e.tapAt = e.now()
		return true, out
	}
	return false, e.commitTap()
}

func isLetter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

// Handle processes one key. It returns whether the key was consumed and any
// text to insert into the input.
func (e *Engine) Handle(k Key) (bool, string) {
	t9 := e.Keypad == T9Pad
	if k.Name == "ime" || k.Name == "sym" || t9 && k.Name == "rune" && (k.Rune == '#' || k.Rune == '*') {
		if ok, text, done := e.modeKey(k); done {
			return ok, text
		}
	}
	if e.assoc && !e.Composing() {
		switch k.Name {
		case "left", "right":
			if e.assocNav || t9 {
				e.move(k.Name)
			}
			e.assocNav = true
			return true, ""
		case "enter":
			if e.assocNav || t9 {
				return true, e.commit(e.sel)
			}
		case "down":
			if t9 {
				e.numbered, e.assocNav = true, true
				return true, ""
			}
		case "rune":
			if t9 && e.numbered && k.Rune >= '1' && k.Rune <= '5' {
				if i := e.sel/pageSize*pageSize + int(k.Rune-'1'); i < len(e.cands) {
					return true, e.commit(i)
				}
			}
		case "esc":
			e.Cancel()
			return true, ""
		}
		e.Cancel()
	}
	if e.Composing() {
		return e.composingKey(k)
	}
	if k.Name != "rune" || e.Mode == Num {
		return false, ""
	}
	r := k.Rune
	if t9 {
		switch {
		case k.Long && r >= '0' && r <= '9':
			return true, string(r)
		case e.multiTapMode():
			return e.tapKey(k)
		case e.Mode == Stroke && r >= '1' && r <= '6':
			e.buf = string(r)
			e.refresh()
			return true, ""
		case e.Mode == Stroke && r >= '7':
			return true, ""
		case r >= '2' && r <= '9':
			e.buf = string(r)
			e.refresh()
			return true, ""
		case r == '1':
			e.startPunct()
			return true, ""
		case r == '0':
			return true, " "
		}
		return false, ""
	}
	switch {
	case e.Mode == Zh && r >= 'a' && r <= 'z', e.Mode == En && isLetter(r):
		e.buf = string(r)
		e.refresh()
		return true, ""
	case e.Mode == Zh && marks.fullWidth[r] != "" && !(k.prev() >= '0' && k.prev() <= '9'):
		return true, marks.fullWidth[r]
	}
	return false, ""
}

// modeKey handles #, *, Ctrl+Space and Chr/Sym. done=false hands the key on
// (e.g. * steps through matches while composing).
func (e *Engine) modeKey(k Key) (ok bool, text string, done bool) {
	switch {
	case k.Name == "ime" || k.Rune == '#' && !k.Long:
		text = e.commitCurrent()
		e.cycleMode()
		return true, text, true
	case k.Rune == '#':
		text = e.commitCurrent()
		e.toggleNumbers()
		return true, text, true
	case e.Mode == Num:
		return false, "", true
	case k.Name == "sym" || k.Long || e.tap != 0 || !e.Composing():
		text = e.commitCurrent()
		e.Cancel()
		e.WantSymbols = true
		return true, text, true
	}
	return false, "", false
}

// commitCurrent ends any composition by taking what is highlighted.
func (e *Engine) commitCurrent() string {
	switch {
	case e.tap != 0:
		return e.commitTap()
	case e.buf != "" || e.punct:
		text := e.commit(e.sel)
		e.Cancel()
		return text
	}
	return ""
}

func (e *Engine) move(dir string) {
	if e.onSpell {
		if dir == "right" {
			e.spellSel = (e.spellSel + 1) % len(e.spells)
		} else {
			e.spellSel = (e.spellSel + len(e.spells) - 1) % len(e.spells)
		}
		return
	}
	if len(e.cands) == 0 {
		return
	}
	if dir == "right" {
		e.sel = (e.sel + 1) % len(e.cands)
	} else {
		e.sel = (e.sel + len(e.cands) - 1) % len(e.cands)
	}
}

func (e *Engine) lockSpelling() {
	e.spelling, e.onSpell, e.sel = e.spells[e.spellSel], false, 0
	e.refresh()
}

func (e *Engine) composingKey(k Key) (bool, string) {
	t9 := e.Keypad == T9Pad
	if e.tap != 0 {
		switch k.Name {
		case "rune":
			return e.tapKey(k)
		case "backspace", "esc":
			e.tap, e.tapIdx = 0, 0
			return true, ""
		}
		text := e.commitTap()
		return k.Name == "right" || k.Name == "enter", text
	}
	switch k.Name {
	case "backspace":
		if e.punct || len(e.buf) <= 1 {
			e.Cancel()
			return true, ""
		}
		e.buf, e.spelling = e.buf[:len(e.buf)-1], ""
		e.refresh()
		return true, ""
	case "esc":
		e.Cancel()
		return true, ""
	case "left", "right":
		// Nokia T9: Right accepts the word without a space.
		if t9 && e.Mode == En && !e.punct && k.Name == "right" {
			return true, e.commit(e.sel)
		}
		e.move(k.Name)
		return true, ""
	case "up":
		switch {
		case e.numbered:
			e.numbered = false
		case t9 && e.Mode == Zh && len(e.spells) > 1 && !e.punct && !e.onSpell:
			e.onSpell = true
		default:
			e.sel = max(0, (e.sel/pageSize-1)*pageSize)
		}
		return true, ""
	case "down":
		switch {
		case e.onSpell:
			e.lockSpelling()
		case t9 && !e.numbered:
			e.numbered = true
		default:
			e.sel = min(len(e.cands)-1, (e.sel/pageSize+1)*pageSize)
		}
		return true, ""
	case "enter":
		if e.onSpell {
			e.lockSpelling()
			return true, ""
		}
		return true, e.commit(e.sel)
	case "rune":
	default:
		return true, ""
	}
	r := k.Rune
	if t9 {
		return e.t9Composing(k)
	}
	switch {
	case e.Mode == Zh && (r >= 'a' && r <= 'z' || r == '\''), e.Mode == En && isLetter(r):
		e.buf += string(r)
		e.sel = 0
		e.refresh()
		return true, ""
	case r >= '1' && r <= '9':
		i := e.sel/pageSize*pageSize + int(r-'1')
		if i < len(e.cands) && int(r-'1') < pageSize {
			return true, e.commit(i)
		}
		return true, ""
	case r == ' ':
		text := e.commit(e.sel)
		if e.Mode == En {
			text += " "
		}
		return true, text
	}
	text := e.commit(e.sel)
	if e.Mode == Zh && marks.fullWidth[r] != "" {
		return true, text + marks.fullWidth[r]
	}
	return true, text + string(r)
}

// t9Composing handles digits, * and 0 while a 9-key composition is open.
func (e *Engine) t9Composing(k Key) (bool, string) {
	r := k.Rune
	if e.Mode == Stroke && !e.punct && !e.numbered {
		if r >= '1' && r <= '6' {
			e.buf += string(r)
			e.sel = 0
			e.refresh()
		}
		return true, ""
	}
	switch {
	case e.numbered && r >= '1' && r <= '5':
		if i := e.sel/pageSize*pageSize + int(r-'1'); i < len(e.cands) {
			return true, e.commit(i)
		}
		return true, ""
	case r == '*':
		// Next match: the next word in English, the next spelling in Chinese
		// (Series 40 6th edition), the next mark in the punctuation list.
		if e.Mode == Zh && !e.punct && len(e.spells) > 1 {
			e.spellSel = (e.spellSel + 1) % len(e.spells)
			e.spelling, e.sel = e.spells[e.spellSel], 0
			e.cands = load().zhCandidates(e.buf, true, e.spelling, false)
		} else {
			e.move("right")
		}
		return true, ""
	case e.punct:
		if r == '1' {
			e.move("right")
			return true, ""
		}
		text := e.commit(e.sel)
		ok, more := e.Handle(k)
		_ = ok
		return true, text + more
	case r >= '2' && r <= '9':
		if k.Long && e.Mode == En {
			return true, e.commit(e.sel) + string(r)
		}
		e.buf += string(r)
	case r == '0' || r == '1':
		if e.Mode == En {
			text := e.commit(e.sel)
			if r == '0' {
				return true, text + " "
			}
			e.startPunct()
			return true, text
		}
		// Chinese: 0 separates syllables (Series 40); 1 does the same here
		// because the dictionary carries no tones.
		e.buf += "'"
	default:
		return true, ""
	}
	e.spelling, e.sel, e.numbered = "", 0, false
	e.refresh()
	return true, ""
}
