package shell

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
)

// applyMerge is RFC 7386 as a renderer applies it.
func applyMerge(target map[string]any, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(target, k)
		case map[string]any:
			if set, ok := pv["$set"].(map[string]any); ok {
				arr := target[k].([]any)
				for i, ev := range set {
					n, _ := strconv.Atoi(i)
					if em, ok := ev.(map[string]any); ok {
						old, _ := arr[n].(map[string]any)
						arr[n] = applyMerge(old, em)
					} else {
						arr[n] = ev
					}
				}
				continue
			}
			t, _ := target[k].(map[string]any)
			target[k] = applyMerge(t, pv)
		default:
			target[k] = v
		}
	}
	return target
}

func TestMergePatchRoundTrip(t *testing.T) {
	decode := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	old := decode(`{"t":"frame","status":{"time":"10:00","bat":80},"body":[{"text":"a"}],"toast":"x","overlay":{"kind":"menu","items":[1,2]}}`)
	cur := decode(`{"t":"frame","status":{"time":"10:01","bat":80},"body":[{"text":"a"}],"overlay":{"kind":"menu","items":[1,2,3]},"idle":{"time":"10:01"}}`)
	p := mergePatch(old, cur)
	want := decode(`{"status":{"time":"10:01"},"toast":null,"overlay":{"items":[1,2,3]},"idle":{"time":"10:01"}}`)
	if !reflect.DeepEqual(p, want) {
		raw, _ := json.Marshal(p)
		t.Fatalf("patch %s", raw)
	}
	again := decode(`{"t":"frame","status":{"time":"10:00","bat":80},"body":[{"text":"a"}],"toast":"x","overlay":{"kind":"menu","items":[1,2]}}`)
	if got := applyMerge(again, p); !reflect.DeepEqual(got, cur) {
		t.Fatal("applying the patch does not give the new frame")
	}
	if len(mergePatch(cur, cur)) != 0 {
		t.Fatal("a patch between equal frames")
	}
}

func TestMergePatchArrays(t *testing.T) {
	decode := func(s string) map[string]any {
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		return m
	}
	old := `{"body":[{"title":"a","pixels":"AAAA","focused":true},{"title":"b","pixels":"BBBB"},{"title":"c","pixels":"CCCC"}]}`
	cur := `{"body":[{"title":"a","pixels":"AAAA"},{"title":"b","pixels":"BBBB","focused":true},{"title":"c","pixels":"CCCC"}]}`
	p := mergePatch(decode(old), decode(cur))
	raw, _ := json.Marshal(p)
	if want := `{"body":{"$set":{"0":{"focused":null},"1":{"focused":true}}}}`; string(raw) != want {
		t.Fatalf("patch %s, want %s", raw, want)
	}
	if got := applyMerge(decode(old), p); !reflect.DeepEqual(got, decode(cur)) {
		t.Fatal("applying the array patch does not give the new frame")
	}
	longer := `{"body":[{"title":"a"},{"title":"b"},{"title":"c"},{"title":"d"}]}`
	if p := mergePatch(decode(old), decode(longer)); reflect.TypeOf(p["body"]).Kind() != reflect.Slice {
		t.Fatal("an array that changed length must be replaced whole")
	}
}
