package shell

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// The developer documentation lists every capability the broker serves, so
// adding one without documenting it fails here.
func TestAPIDocCoversCapabilities(t *testing.T) {
	raw, err := os.ReadFile("../docs/22-developer-api.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	var missing []string
	for cap := range capPerms {
		if !strings.Contains(doc, "`"+cap+"`") {
			missing = append(missing, cap)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("22-developer-api.md does not document: %s", strings.Join(missing, ", "))
	}
}
