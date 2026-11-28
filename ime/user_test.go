package ime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func pick(e *Engine, word string) string {
	for i, c := range e.cands {
		if c.Text == word {
			return e.commit(i)
		}
	}
	return ""
}

func TestUserDictRanksPicks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.json")
	e := New(QWERTY)
	e.SetUserDict(path)
	typeAll(e, "shi")
	if len(e.cands) < 6 {
		t.Fatal("no candidates for shi")
	}
	rare := e.cands[5].Text
	for i := 0; i < 3; i++ {
		e.Cancel()
		typeAll(e, "shi")
		if pick(e, rare) == "" {
			t.Fatalf("%s not offered", rare)
		}
		e.Cancel()
	}
	again := New(QWERTY)
	again.SetUserDict(path)
	typeAll(again, "shi")
	if again.cands[0].Text != rare {
		t.Fatalf("after three picks %s is not first: %v", rare, texts(again)[:6])
	}
}

// The user dictionary cut short (the power going while it was written) is
// read from its copy, the picks up to its last synced write kept; with
// neither whole the engine starts with an empty one.
func TestUserDictFromItsCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.json")
	e := New(QWERTY)
	e.SetUserDict(path)
	typeAll(e, "shi")
	rare := e.cands[5].Text
	pick(e, rare)
	e.Cancel()
	typeAll(e, "shi")
	pick(e, e.cands[4].Text)
	e.Cancel()
	words := func(p string) int {
		var d userDict
		raw, _ := os.ReadFile(p)
		_ = json.Unmarshal(raw, &d)
		return len(d.Words)
	}
	if words(path) != 2 || words(path+".bak") != 1 {
		t.Fatalf("the second pick within a minute: %d words, %d in the copy", words(path), words(path+".bak"))
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, st.Size()/2); err != nil {
		t.Fatal(err)
	}
	again := New(QWERTY)
	again.SetUserDict(path)
	typeAll(again, "shi")
	if again.cands[0].Text != rare {
		t.Fatalf("the pick lost with the file cut short: %v", texts(again)[:6])
	}
	for _, p := range []string{path, path + ".bak"} {
		if err := os.WriteFile(p, []byte(`{"words":[{"w":`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fresh := New(QWERTY)
	fresh.SetUserDict(path)
	typeAll(fresh, "shi")
	if fresh.cands[0].Text == rare || len(fresh.user.Words) != 0 {
		t.Fatalf("neither whole: %v, %d words", texts(fresh)[:6], len(fresh.user.Words))
	}
}

func TestUserDictLearnsPhrases(t *testing.T) {
	e := New(QWERTY)
	e.SetUserDict(filepath.Join(t.TempDir(), "user.json"))
	typeAll(e, "wodemingzi")
	for _, part := range []string{zh("wode"), zh("mingzi")} {
		if pick(e, part) == "" {
			t.Fatalf("%s not offered: %v", part, texts(e))
		}
	}
	e.Cancel()
	typeAll(e, "wodemingzi")
	if e.cands[0].Text != zh("wodemingzi") {
		t.Fatalf("the phrase picked in pieces is not learnt: %v", texts(e)[:4])
	}
	t9 := New(T9Pad)
	t9.user = e.user
	typeAll(t9, "9633646494")
	if t9.cands[0].Text != zh("wodemingzi") {
		t.Fatalf("9-key does not find the learnt phrase: %v", texts(t9)[:4])
	}
}

func TestEnglishNextWord(t *testing.T) {
	e := New(QWERTY)
	e.SetUserDict(filepath.Join(t.TempDir(), "user.json"))
	e.SetMode(En)
	for _, w := range []string{"good", "morning"} {
		typeAll(e, w)
		pick(e, w)
		e.Cancel()
	}
	typeAll(e, "good")
	pick(e, "good")
	if !e.assoc || len(e.cands) == 0 || e.cands[0].Text != " morning" {
		t.Fatalf("after good: %v", texts(e))
	}
}

func TestFuzzyPinyin(t *testing.T) {
	e := New(QWERTY)
	typeAll(e, "zongguo")
	if has(texts(e), zh("zhongguo"), 5) {
		t.Fatal("zhongguo's word for zongguo without fuzzy pinyin")
	}
	e.Cancel()
	e.Fuzzy = true
	typeAll(e, "zongguo")
	if !has(texts(e), zh("zhongguo"), 5) {
		t.Fatalf("fuzzy z=zh: %v", texts(e)[:5])
	}
	e.Cancel()
	typeAll(e, "henglan")
	if !has(texts(e), zh("hennan"), 8) {
		t.Fatalf("fuzzy eng=en: %v", texts(e)[:8])
	}
	d := load()
	if v := d.variants("zhang"); len(v) < 3 {
		t.Fatalf("variants of zhang: %v", v)
	}
}
