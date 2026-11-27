package jsrt

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/ui"
)

// Requests aborted as their transfers complete, many times: the loop takes
// a request's news while its worker may still post more (its last, as it
// ends), and lets go of the request only once it is off the list of news.
// Each round aborts its requests from the first answer's callback, which
// the loop runs between settling one request of that news and the next.
func TestFetchAbortRace(t *testing.T) {
	srv := httptest.NewServer(fetchServer())
	defer srv.Close()
	s := jsrtEngine(t)(t, t.TempDir(), "en", filepath.Join(appCopy(t, "fetch"), "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	raw, _ := json.Marshal(map[string]any{"race": true, "url": srv.URL + "/hello", "rounds": 100})
	for i := 0; i < 10; i++ {
		s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "fetch", Value: string(raw)})
		s.screenText("race - next 200 hi")
	}
}
