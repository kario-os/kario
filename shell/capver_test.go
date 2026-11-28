package shell

import (
	"encoding/json"
	"io"
	"log"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// capCore is a Shell with only its broker, the music library empty (the
// demo tracks alone), and app a.
func capCore(t *testing.T, a *App) (*Core, func(cap string, args any) ui.Msg) {
	t.Helper()
	c := &Core{cfg: Config{StateDir: t.TempDir()}, log: log.New(io.Discard, "", 0), ev: make(chan func(), 16), apps: []*App{a},
		sys: &App{system: true, M: &Manifest{ID: "system"}}}
	c.broker = &broker{c: c, perms: map[string]map[string]string{}, stats: map[string]*CapStat{}, usage: map[string]map[string]*permUse{}}
	call := func(cap string, args any) ui.Msg {
		t.Helper()
		raw, _ := json.Marshal(args)
		var got *ui.Msg
		c.broker.handleFor(a, ui.Msg{T: ui.MsgReq, Req: 1, Cap: cap, Args: raw}, func(m ui.Msg, _ []*os.File) { got = &m })
		for end := time.After(10 * time.Second); got == nil; {
			select {
			case f := <-c.ev:
				f()
			case <-end:
				t.Fatalf("%s: no answer", cap)
			}
		}
		return *got
	}
	return c, call
}

// An app names a capability bare or with its version: the bare name and @1
// are version 1, its answer and its count; @2 answers in its own shape; a
// version the system lacks is refused saying which it serves, and counts
// for nothing (the compatibility report counts versions served).
func TestCapVersionDispatch(t *testing.T) {
	c, call := capCore(t, &App{M: &Manifest{ID: "com.example.music", Name: "Music"}})
	bare, one := call(ui.CapMediaList, nil), call(ui.CapMediaList+"@1", nil)
	var all []ui.MediaItem
	if !bare.OK || json.Unmarshal(bare.Data, &all) != nil || len(all) < 3 {
		t.Fatalf("media.list: %+v", bare)
	}
	if !one.OK || string(one.Data) != string(bare.Data) {
		t.Fatalf("media.list@1 answered %s, media.list %s", one.Data, bare.Data)
	}
	page := func(args any) ui.MediaPage {
		t.Helper()
		r := call(ui.CapMediaList2, args)
		var p ui.MediaPage
		if !r.OK || json.Unmarshal(r.Data, &p) != nil || !strings.HasPrefix(string(r.Data), `{"items":[`) {
			t.Fatalf("media.list@2 %v: %+v", args, r)
		}
		return p
	}
	if p := page(nil); len(p.Items) != len(all) || p.Total != len(all) || p.Next != 0 || p.Items[0].ID != all[0].ID {
		t.Errorf("a whole page: %+v", p)
	}
	if p := page(ui.MediaListArgs{Limit: 2}); len(p.Items) != 2 || p.Total != len(all) || p.Next != 2 || p.Items[1].ID != all[1].ID {
		t.Errorf("the first two: %+v", p)
	}
	if p := page(ui.MediaListArgs{Kind: ui.MediaAudio, Offset: 2, Limit: 2}); len(p.Items) != min(2, len(all)-2) || p.Items[0].ID != all[2].ID || (p.Next != 0) != (len(all) > 4) {
		t.Errorf("from the third: %+v", p)
	}
	if p := page(ui.MediaListArgs{Offset: 99}); len(p.Items) != 0 || p.Total != len(all) {
		t.Errorf("past the end: %+v", p)
	}
	for _, bad := range []any{ui.MediaListArgs{Limit: ui.MediaPageMax + 1}, ui.MediaListArgs{Offset: -1}, ui.MediaListArgs{Kind: "doc"}, map[string]string{"limit": "2"}} {
		if r := call(ui.CapMediaList2, bad); r.OK {
			t.Errorf("media.list@2 %v answered %s", bad, r.Data)
		}
	}
	r := call("media.list@3", nil)
	if r.OK || r.Err != `unknown capability "media.list@3" (served: media.list@1, media.list@2)` {
		t.Errorf("media.list@3: %+v", r)
	}
	if st := c.broker.stats[ui.CapMediaList]; st == nil || st.Calls != 2 || st.OK != 2 {
		t.Errorf("media.list's count %+v", st)
	}
	if st := c.broker.stats[ui.CapMediaList2]; st == nil || st.Calls != 8 || st.Errors != 4 {
		t.Errorf("media.list@2's count %+v", st)
	}
	if st := c.broker.stats["media.list@3"]; st != nil {
		t.Errorf("an unknown version counted: %+v", st)
	}
}

// A version is the same capability to every check: @1 needs the
// permission the bare name does, @2 the one @1 needs for the same kind.
func TestCapVersionChecks(t *testing.T) {
	_, call := capCore(t, &App{M: &Manifest{ID: "com.example.notes", Name: "Notes"}})
	for cap, args := range map[string]any{
		ui.CapSMSSend + "@1":   ui.SMSSendArgs{To: "10086", Text: "x"},
		ui.CapContacts + "@1":  nil,
		ui.CapMediaList2:       ui.MediaListArgs{Kind: ui.MediaImage},
		ui.CapMediaList + "@1": ui.MediaArgs{Kind: ui.MediaVideo},
	} {
		if r := call(cap, args); r.OK || !strings.HasPrefix(r.Err, "permission not declared in manifest") {
			t.Errorf("%s %v without the permission: %+v", cap, args, r)
		}
	}
}

// Once version 1 is removed (CapRemoved) the bare name and @1 are refused
// with the level and what is served, @2 still answers; the removal policy
// itself is ui's TestCapTables.
func TestCapVersionRemoved(t *testing.T) {
	c, call := capCore(t, &App{M: &Manifest{ID: "com.example.music", Name: "Music"}})
	levels := maps.Clone(ui.CapLevels)
	delete(levels, ui.CapMediaList)
	removed := map[string]int{ui.CapMediaList: ui.CapLevels[ui.CapMediaList2] + ui.CapKeepLevels}
	if err := ui.CheckCapTables(levels, removed, removed[ui.CapMediaList]); err != nil {
		t.Fatal(err)
	}
	c.broker.resolve = func(cap string) (string, error) { return ui.ResolveCapIn(levels, removed, cap) }
	for _, cap := range []string{ui.CapMediaList, ui.CapMediaList + "@1"} {
		if r := call(cap, nil); r.OK || r.Err != `unknown capability "`+cap+`": removed at platform level 5 (served: media.list@2)` {
			t.Errorf("%s once removed: %+v", cap, r)
		}
	}
	if r := call(ui.CapMediaList2, ui.MediaListArgs{Limit: 1}); !r.OK {
		t.Errorf("media.list@2: %+v", r)
	}
	if st := c.broker.stats[ui.CapMediaList]; st != nil {
		t.Errorf("a removed version counted: %+v", st)
	}
}

// media.list@2's page of any list: within it, never null.
func TestMediaPage(t *testing.T) {
	if raw, _ := json.Marshal(mediaPage(nil, ui.MediaListArgs{})); string(raw) != `{"items":[],"total":0}` {
		t.Errorf("an empty library: %s", raw)
	}
	many := make([]ui.MediaItem, ui.MediaPageDefault+1)
	if p := mediaPage(many, ui.MediaListArgs{}); len(p.Items) != ui.MediaPageDefault || p.Next != ui.MediaPageDefault {
		t.Errorf("the default page: %d items, next %d", len(p.Items), p.Next)
	}
	if p := mediaPage(many, ui.MediaListArgs{Offset: ui.MediaPageDefault}); len(p.Items) != 1 || p.Next != 0 {
		t.Errorf("the last page: %d items, next %d", len(p.Items), p.Next)
	}
}
