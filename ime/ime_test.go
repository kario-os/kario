package ime

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// words are the Chinese the tests expect (testdata/words.json): words by
// their pinyin, stroke codes as glyphs.
var words = func() (w struct {
	Pinyin  map[string]string `json:"pinyin"`
	Strokes map[string]string `json:"strokes"`
}) {
	raw, err := os.ReadFile("testdata/words.json")
	if err == nil {
		err = json.Unmarshal(raw, &w)
	}
	if err != nil {
		panic(err)
	}
	return w
}()

// zh is the word spelt py in testdata/words.json.
func zh(py string) string {
	w, ok := words.Pinyin[py]
	if !ok {
		panic("testdata/words.json has no " + py)
	}
	return w
}

func typeAll(e *Engine, s string) string {
	var out strings.Builder
	for _, r := range s {
		_, c := e.Handle(Key{Name: "rune", Rune: r})
		out.WriteString(c)
	}
	return out.String()
}

func texts(e *Engine) []string {
	var out []string
	for _, c := range e.cands {
		out = append(out, c.Text)
	}
	return out
}

func has(list []string, w string, within int) bool {
	for i, x := range list {
		if i >= within {
			break
		}
		if x == w {
			return true
		}
	}
	return false
}

func TestLoadTime(t *testing.T) {
	t0 := time.Now()
	d := load()
	t.Logf("dictionaries: %d zh, %d en, %d syllables in %v (%s)", d.zhCount(), d.enCount(), len(d.syllable), time.Since(t0), DictSource)
}

func TestQwertyPinyin(t *testing.T) {
	e := New(QWERTY)
	typeAll(e, "zhongguo")
	if !has(texts(e), zh("zhongguo"), 1) {
		t.Fatalf("zhongguo -> %v", texts(e))
	}
	_, c := e.Handle(Key{Name: "rune", Rune: ' '})
	if c != zh("zhongguo") {
		t.Fatalf("space committed %q", c)
	}
	if !e.Active() || !has(texts(e), zh("ren"), 5) {
		t.Fatalf("association after zhongguo: %v", texts(e))
	}
}

func TestPartialSyllableAndAbbreviation(t *testing.T) {
	e := New(QWERTY)
	typeAll(e, "nihaom")
	if !has(texts(e), zh("nihaoma"), 5) && !has(texts(e), zh("nihao"), 8) {
		t.Fatalf("nihaom -> %v", texts(e))
	}
	e.Cancel()
	typeAll(e, "zg")
	if !has(texts(e), zh("zhongguo"), 3) {
		t.Fatalf("abbreviation zg -> %v", texts(e))
	}
}

func TestPartialCommitKeepsRest(t *testing.T) {
	e := New(QWERTY)
	typeAll(e, "womenzhongguo")
	// pick the candidate covering only the first syllables, rest keeps composing
	for i, c := range e.cands {
		if c.Text == zh("women") {
			_, got := e.Handle(Key{Name: "rune", Rune: rune('1' + i%pageSize)})
			if e.sel != 0 {
				e.sel = i
			}
			_ = got
			break
		}
	}
	e.Cancel()
	typeAll(e, "womenzhongguo")
	idx := -1
	for i, c := range e.cands {
		if c.Text == zh("women") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("women not offered: %v", texts(e))
	}
	got := e.commit(idx)
	if got != zh("women") || e.buf != "zhongguo" || !has(texts(e), zh("zhongguo"), 1) {
		t.Fatalf("partial commit: got %q, rest %q, cands %v", got, e.buf, texts(e))
	}
}

func TestT9Pinyin(t *testing.T) {
	e := New(T9Pad)
	typeAll(e, "94664486")
	if !has(texts(e), zh("zhongguo"), 3) {
		t.Fatalf("94664486 -> %v (spellings %v)", texts(e), e.spells)
	}
	if len(e.spells) == 0 || e.spells[0] != "zhong'guo" {
		t.Logf("spellings: %v", e.spells)
	}
	_, c := e.Handle(Key{Name: "enter"})
	if c != zh("zhongguo") && !has(texts(e), zh("zhongguo"), 1) {
		t.Fatalf("enter committed %q", c)
	}
}

func TestT9SpellingFilter(t *testing.T) {
	e := New(T9Pad)
	typeAll(e, "64")
	if len(e.spells) < 2 {
		t.Fatalf("expected several readings for 64, got %v", e.spells)
	}
	e.Handle(Key{Name: "up"})
	e.Handle(Key{Name: "right"})
	want := e.spells[e.spellSel]
	e.Handle(Key{Name: "enter"})
	for _, c := range e.cands {
		if c.Pinyin != "" && !strings.HasPrefix(c.Pinyin, want) && !strings.HasPrefix(want, c.Pinyin) {
			t.Fatalf("spelling %q not applied: %v has %q", want, c.Text, c.Pinyin)
		}
	}
}

func TestEnglish(t *testing.T) {
	e := New(QWERTY)
	e.Handle(Key{Name: "ime"})
	typeAll(e, "hel")
	if texts(e)[0] != "hel" || !has(texts(e), "hello", 9) || !has(texts(e), "help", 9) {
		t.Fatalf("hel -> %v", texts(e))
	}
	e.Cancel()
	e.SetKeypad(T9Pad)
	e.SetMode(En)
	typeAll(e, "43556")
	if !has(texts(e), "hello", 2) {
		t.Fatalf("T9 43556 -> %v", texts(e))
	}
	_, c := e.Handle(Key{Name: "rune", Rune: '0'})
	if !strings.HasSuffix(c, " ") {
		t.Fatalf("0 should commit with a space, got %q", c)
	}
}

func TestPunctuationAndModes(t *testing.T) {
	e := New(QWERTY)
	if _, c := e.Handle(Key{Name: "rune", Rune: ','}); c != "\uff0c" {
		t.Fatalf("zh comma -> %q", c)
	}
	e.SetKeypad(T9Pad)
	e.Handle(Key{Name: "rune", Rune: '1'})
	if !e.Composing() || len(e.cands) == 0 {
		t.Fatal("1 should open the punctuation picker")
	}
	if _, c := e.Handle(Key{Name: "enter"}); c != "\uff0c" {
		t.Fatalf("punctuation picker committed %q", c)
	}
	for _, want := range []Mode{Stroke, En, Abc, Lower, Upper, Zh} {
		e.Handle(Key{Name: "rune", Rune: '#'})
		if e.Mode != want {
			t.Fatalf("# cycled to %s, want %s", e.Mode, want)
		}
	}
	e.Handle(Key{Name: "rune", Rune: '#', Long: true})
	if e.Mode != Num {
		t.Fatalf("long # -> %s, want 123", e.Mode)
	}
	e.Handle(Key{Name: "rune", Rune: '#', Long: true})
	if e.Mode != Zh {
		t.Fatalf("long # again -> %s, want back to pinyin", e.Mode)
	}
}

// Nokia multi-tap: repeated presses step through a key, a different key or
// the timeout commits, Abc capitalises the start of a sentence.
func TestT9MultiTap(t *testing.T) {
	clock := time.Unix(0, 0)
	e := New(T9Pad)
	e.now = func() time.Time { return clock }
	e.SetMode(Abc)
	var out strings.Builder
	press := func(s string) {
		for _, r := range s {
			_, c := e.Handle(Key{Name: "rune", Rune: r, Before: out.String()})
			out.WriteString(c)
			clock = clock.Add(200 * time.Millisecond)
		}
	}
	press("4433555")
	_, c := e.Handle(Key{Name: "right"})
	out.WriteString(c)
	press("555666")
	clock = clock.Add(2 * time.Second)
	out.WriteString(e.Tick())
	if out.String() != "Hello" {
		t.Fatalf("multi-tap gave %q", out.String())
	}
	_, c = e.Handle(Key{Name: "rune", Rune: '5', Long: true})
	if c != "5" {
		t.Fatalf("long 5 gave %q", c)
	}
}

func TestT9StarNextMatchAndSymbols(t *testing.T) {
	e := New(T9Pad)
	e.SetMode(En)
	typeAll(e, "4663")
	first := e.Preedit()
	e.Handle(Key{Name: "rune", Rune: '*'})
	if e.Preedit() == first {
		t.Fatalf("* did not move to the next match (%v)", texts(e))
	}
	_, c := e.Handle(Key{Name: "right"})
	if c == "" || strings.HasSuffix(c, " ") {
		t.Fatalf("right should accept the word without a space, got %q", c)
	}
	e.SetMode(Zh)
	typeAll(e, "64")
	e.Handle(Key{Name: "rune", Rune: '*'})
	if e.spelling == "" || e.spelling != e.spells[1] {
		t.Fatalf("* should select the next spelling, got %q of %v", e.spelling, e.spells)
	}
	e.Cancel()
	e.Handle(Key{Name: "rune", Rune: '*'})
	if !e.WantSymbols {
		t.Fatal("* with nothing composing should open the symbol table")
	}
}

func TestT9NumberedPickAndSeparator(t *testing.T) {
	e := New(T9Pad)
	typeAll(e, "94664")
	e.Handle(Key{Name: "rune", Rune: '0'})
	typeAll(e, "486")
	if !strings.Contains(e.buf, "'") || !has(texts(e), zh("zhongguo"), 3) {
		t.Fatalf("0 should separate syllables: buf %q, %v", e.buf, texts(e))
	}
	e.Handle(Key{Name: "down"})
	idx := -1
	for i, c := range e.cands[:min(len(e.cands), pageSize)] {
		if c.Text == zh("zhongguo") {
			idx = i
		}
	}
	_, c := e.Handle(Key{Name: "rune", Rune: rune('1' + idx)})
	if c != zh("zhongguo") {
		t.Fatalf("numbered pick %d gave %q", idx+1, c)
	}
	// The association list is active on the 9-key pad: OK takes its first item.
	if !e.Active() {
		t.Fatal("association should follow a commit")
	}
	if _, c := e.Handle(Key{Name: "enter"}); c == "" {
		t.Fatal("OK should commit the highlighted association")
	}
}

// Stroke input: five keys for the five stroke classes in writing order, 6
// for a stroke not remembered; the most frequent matches first.
func TestStrokes(t *testing.T) {
	e := New(T9Pad)
	e.Handle(Key{Name: "rune", Rune: '#'})
	if e.Mode != Stroke || e.Indicator() != tr.T("Strokes") {
		t.Fatalf("# from pinyin -> %s", e.Mode)
	}
	typeAll(e, "2512")
	if e.Preedit() != words.Strokes["2512"] || !has(texts(e), zh("zhong"), 3) {
		t.Fatalf("2512 (%s) -> %v", e.Preedit(), texts(e))
	}
	e.Cancel()
	typeAll(e, "1")
	if !has(texts(e), zh("yi"), 5) {
		t.Fatalf("1 -> %v", texts(e))
	}
	e.Cancel()
	typeAll(e, "6334")
	if !has(texts(e), zh("huo"), 5) {
		t.Fatalf("6334 (6 = any) -> %v", texts(e))
	}
	e.Cancel()
	typeAll(e, "25112141")
	_, c := e.Handle(Key{Name: "enter"})
	if c != zh("guo") {
		t.Fatalf("25112141 committed %q, want %s", c, zh("guo"))
	}
	// Digits beyond the five classes and the wildcard are not strokes.
	typeAll(e, "3")
	before := e.buf
	e.Handle(Key{Name: "rune", Rune: '8'})
	if e.buf != before {
		t.Fatalf("8 changed the stroke code to %q", e.buf)
	}
}
