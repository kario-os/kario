package shell

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The renderer protocol (23-renderer-protocol.md) is what a renderer built
// outside this tree is written against, so every frame field and message
// must be documented there.
func TestRendererDoc(t *testing.T) {
	raw, err := os.ReadFile("../docs/23-renderer-protocol.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	names := map[string]bool{}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(ty reflect.Type) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Array {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || seen[ty] {
			return
		}
		seen[ty] = true
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
				names[tag] = true
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(Frame{}))
	walk(reflect.TypeOf(guiMsg{}))
	walk(reflect.TypeOf(RendererInfo{}))
	for _, m := range []string{"hello", "frame", "snap", "quit", "ready", "display", "key", "snapped", RendererProto} {
		names[m] = true
	}
	var missing []string
	for n := range names {
		if !strings.Contains(doc, "`"+n+"`") && !strings.Contains(doc, `"`+n+`"`) {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("23-renderer-protocol.md does not document: %s", strings.Join(missing, ", "))
	}
}
