package ime

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// The user dictionary (adaptive ranking and learnt words): words the user
// picks rise in the candidates, phrases picked in pieces (zhongguo then
// yinhang for zhongguoyinhang) are learnt as one, and English words learn
// what usually follows them (next-word prediction from the user's own
// typing). It lives in a file the shell keeps in its state; nothing leaves
// the device.

const (
	maxUserWords = 5000
	maxNext      = 2000
)

type userWord struct {
	Word   string `json:"w"`
	Pinyin string `json:"p,omitempty"` // syllables separated by spaces; "" for English
	Count  int    `json:"n"`
	Last   int64  `json:"t"`
}

type userDict struct {
	mu    sync.Mutex
	path  string
	Words []userWord          `json:"words"`
	Next  map[string][]string `json:"next"` // English word -> recent followers, newest first
	dirty bool
}

func openUser(path string) *userDict {
	u := &userDict{path: path, Next: map[string][]string{}}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, u)
		if u.Next == nil {
			u.Next = map[string][]string{}
		}
	}
	return u
}

func (u *userDict) save() {
	if u.path == "" || !u.dirty {
		return
	}
	raw, _ := json.Marshal(u)
	tmp := u.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, u.path)
		u.dirty = false
	}
}

// picked records a committed candidate.
func (u *userDict) picked(word, pinyin string) {
	if word == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now().Unix()
	for i := range u.Words {
		if u.Words[i].Word == word && u.Words[i].Pinyin == pinyin {
			u.Words[i].Count++
			u.Words[i].Last = now
			u.dirty = true
			u.save()
			return
		}
	}
	u.Words = append(u.Words, userWord{Word: word, Pinyin: pinyin, Count: 1, Last: now})
	if len(u.Words) > maxUserWords {
		// Forget the least used, oldest first.
		sort.Slice(u.Words, func(a, b int) bool {
			if u.Words[a].Count != u.Words[b].Count {
				return u.Words[a].Count > u.Words[b].Count
			}
			return u.Words[a].Last > u.Words[b].Last
		})
		u.Words = u.Words[:maxUserWords]
	}
	u.dirty = true
	u.save()
}

// followed records that next was typed right after prev (English).
func (u *userDict) followed(prev, next string) {
	prev, next = strings.ToLower(prev), strings.ToLower(next)
	if prev == "" || next == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	list := []string{next}
	for _, w := range u.Next[prev] {
		if w != next && len(list) < 5 {
			list = append(list, w)
		}
	}
	u.Next[prev] = list
	if len(u.Next) > maxNext {
		for k := range u.Next {
			if k != prev {
				delete(u.Next, k)
				break
			}
		}
	}
	u.dirty = true
	u.save()
}

func (u *userDict) count(word, pinyin string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, w := range u.Words {
		if w.Word == word && w.Pinyin == pinyin {
			return w.Count
		}
	}
	return 0
}

// phrases are learnt Chinese words whose whole reading the input spells:
// the letters of the syllables (full keyboard) or their digits (9-key).
func (u *userDict) phrases(input string, t9 bool) []userWord {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []userWord
	for _, w := range u.Words {
		if w.Pinyin == "" || !strings.Contains(w.Pinyin, " ") {
			continue
		}
		key := strings.ReplaceAll(w.Pinyin, " ", "")
		if t9 {
			key = T9(key)
		}
		if key == strings.ReplaceAll(input, "'", "") {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Count > out[b].Count })
	return out
}

func (u *userDict) next(prev string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.Next[strings.ToLower(prev)]...)
}

// rank puts candidates the user picked before first, most picked first,
// keeping the dictionary's order otherwise.
func (u *userDict) rank(cs []Candidate) {
	n := make([]int, len(cs))
	for i, c := range cs {
		n[i] = u.count(c.Text, strings.ReplaceAll(c.Pinyin, "'", " "))
	}
	idx := make([]int, len(cs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return n[idx[a]] > n[idx[b]] })
	out := make([]Candidate, len(cs))
	for i, k := range idx {
		out[i] = cs[k]
	}
	copy(cs, out)
}
