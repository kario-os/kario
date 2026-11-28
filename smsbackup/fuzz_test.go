package smsbackup

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/ui"
)

// FuzzParse is a backup file from anywhere (Files): it reads or is
// refused, what it gives is within the limits, and written out again it
// reads back the same.
func FuzzParse(f *testing.F) {
	f.Add([]byte(sample))
	f.Add([]byte(`<smses><mms msg_box="1" date="1763500000"><parts><part ct="text/plain" text="x"/></parts><addrs><addr address="a" type="137"/><addr address="b" type="151"/></addrs></mms>`))
	f.Add([]byte(`<!DOCTYPE smses [<!ENTITY x "y">]><smses><![CDATA[<sms>]]><sms address='1' date='1763500000000' type='2' body='&#x1F600;&lt;'/></smses>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		msgs, st, err := Parse(bytes.NewReader(data), "")
		if err != nil {
			return
		}
		if len(msgs) != st.Messages || len(msgs) > MaxMessages || st.Skipped < 0 {
			t.Fatalf("%d messages, stats %+v", len(msgs), st)
		}
		for _, m := range msgs {
			if m.Peer == "" || (m.Dir != "in" && m.Dir != "out") || m.At < minAt || m.At > maxAt || !utf8.ValidString(m.Text) ||
				utf8.RuneCountInString(m.Text) > MaxText || utf8.RuneCountInString(m.Subject) > MaxSubject || len(m.Members) > ui.MaxGroup ||
				len(m.Members) == 1 || (strings.TrimSpace(m.Text) == "" && m.Subject == "") {
				t.Fatalf("outside the limits: %+v", m)
			}
		}
		var b bytes.Buffer
		if n, err := Write(&b, msgs, func(string) string { return "" }, "", time.Unix(1763500000, 0)); err != nil || n != len(msgs) {
			t.Fatal(err)
		}
		again, st2, err := Parse(&b, "")
		if len(msgs) == 0 {
			return
		}
		if err != nil || st2.Skipped != 0 || !reflect.DeepEqual(again, msgs) {
			t.Fatalf("written and read again:\n%+v\n%+v (%+v, %v)\n%s", msgs, again, st2, err, b.String())
		}
	})
}
