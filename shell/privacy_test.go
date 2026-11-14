package shell

import (
	"io"
	"log"
	"strings"
	"testing"

	"github.com/kario-os/kario/pkg"
)

// The installer applies the store's privacy label check (ui.CheckPrivacy):
// none passes, one that leaves out a permission's data does not.
func TestInstallPrivacyLabel(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	pack := func(privacy string) *pkg.Package {
		m := `{"id":"com.example.maps","name":"N","exec":"app","permissions":["net","location"],"purposes":{"location":"Nearby places"}` + privacy + `}`
		return &pkg.Package{Manifest: []byte(m), Files: []pkg.File{{Path: "app", Mode: 0o755}}}
	}
	for _, ok := range []string{"", `,"privacy":{"collects":[{"data":"location","use":["app"],"linked":true}]}`, `,"privacy":{"local":["location"]}`} {
		if _, err := c.checkPackage(pack(ok)); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	if _, err := c.checkPackage(pack(`,"privacy":{}`)); err == nil || !strings.Contains(err.Error(), `"location" permission opens location`) {
		t.Errorf("a label without location: %v", err)
	}
}
