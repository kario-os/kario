package ime

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestMappedDictMatchesBuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ime.dict")
	if err := WriteDict(path); err != nil {
		t.Fatal(err)
	}
	b, err := mapFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := openDict(b)
	if err != nil {
		t.Fatal(err)
	}
	built := load()
	for _, q := range []struct {
		s  string
		t9 bool
	}{{"zhongguo", false}, {"zgr", false}, {"94664486", true}, {"xian", false}, {"ni'hao", false}} {
		a, b := built.zhCandidates(q.s, q.t9, "", false), mapped.zhCandidates(q.s, q.t9, "", false)
		if !reflect.DeepEqual(a, b) || len(a) == 0 {
			t.Fatalf("%s: built %v, mapped %v", q.s, a, b)
		}
	}
	if a, b := built.enCandidates("4663", true), mapped.enCandidates("4663", true); !reflect.DeepEqual(a, b) || len(a) == 0 {
		t.Fatalf("en: %v vs %v", a, b)
	}
	if a, b := built.association(zh("zhongguo")), mapped.association(zh("zhongguo")); !reflect.DeepEqual(a, b) || len(a) == 0 {
		t.Fatalf("association: %v vs %v", a, b)
	}
}

func TestDamagedDictRejected(t *testing.T) {
	blob := BuildDict()
	for name, b := range map[string][]byte{
		"short":    blob[:20],
		"magic":    append([]byte("XXXXXXXX"), blob[8:]...),
		"truncate": blob[:len(blob)/2],
	} {
		if _, err := openDict(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	other := append([]byte{}, blob...)
	other[8] ^= 1
	if _, err := openDict(other); err == nil {
		t.Errorf("stale source checksum accepted")
	}
}
