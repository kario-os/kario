package ui

import (
	"encoding/json"
	"testing"
)

// A form's renames go both ways: a message turned into the major's form
// and back is the message it was; a type renamed away is not the major's,
// and a type kept with renamed fields still is.
func TestProtoForm(t *testing.T) {
	f := ProtoForm{Major: 1, Rules: []ProtoRule{
		{T: MsgToast, As: "notice", Fields: map[string]string{"text": "body"}},
		{T: MsgResync, As: "resend"},
		{T: MsgReq, Fields: map[string]string{"cap": "capability"}},
		{Fields: map[string]string{"screen_id": "page"}},
	}}
	fields := func(m Msg) map[string]json.RawMessage {
		raw, _ := json.Marshal(m)
		var out map[string]json.RawMessage
		_ = json.Unmarshal(raw, &out)
		return out
	}
	str := func(m map[string]json.RawMessage) string {
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	for _, tc := range []struct {
		m    Msg
		want string
	}{
		{Msg{T: MsgToast, Text: "hi"}, `{"body":"hi","t":"notice"}`},
		{Msg{T: MsgResync}, `{"t":"resend"}`},
		{Msg{T: MsgAction, ScreenID: "main", Action: "open"}, `{"action":"open","page":"main","t":"action"}`},
		{Msg{T: MsgReq, Req: 1, Cap: CapClipboardSet}, `{"capability":"clipboard.set","req":1,"t":"req"}`},
		{Msg{T: MsgPong, Value: "3"}, `{"t":"pong","value":"3"}`},
	} {
		m := fields(tc.m)
		f.ToMajor(m)
		if got := str(m); got != tc.want {
			t.Errorf("%s to phone.ui/1: %s, want %s", tc.m.T, got, tc.want)
		}
		if err := f.FromMajor(m); err != nil {
			t.Errorf("%s back: %v", tc.m.T, err)
		}
		if got, want := str(m), str(fields(tc.m)); got != want {
			t.Errorf("%s back: %s, want %s", tc.m.T, got, want)
		}
	}
	m := fields(Msg{T: MsgToast, Text: "own form"})
	if err := f.FromMajor(m); err == nil || err.Error() != "phone.ui/1 has no toast (it is notice)" {
		t.Errorf("a toast in phone.ui/1: %v", err)
	}
	m = fields(Msg{T: MsgReq, Req: 2, Cap: CapClipboardSet})
	if err := f.FromMajor(m); err != nil {
		t.Errorf("a req in phone.ui/1: %v", err)
	}
	var back []ProtoForm
	raw, _ := json.Marshal([]ProtoForm{f})
	if err := json.Unmarshal(raw, &back); err != nil || len(back) != 1 || len(back[0].Rules) != 4 {
		t.Fatalf("as JSON: %s %v", raw, err)
	}
}
