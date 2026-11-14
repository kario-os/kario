package sdk

import (
	"encoding/json"
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestDecodeReplaces(t *testing.T) {
	log := []ui.TelLogEntry{{Number: "13800000002", Name: "Li Si"}}
	if err := decode(json.RawMessage(`[{"number":"112","dir":"out"}]`), nil, &log); err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].Name != "" || log[0].Number != "112" {
		t.Fatalf("old entry leaked into the result: %+v", log)
	}
}
