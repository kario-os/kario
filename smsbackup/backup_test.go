package smsbackup

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// sample is shaped as SMS Backup & Restore 10.x writes a backup: emoji as
// UTF-16 surrogate pairs in references, line breaks as &#10;, "null" for
// what a message lacks, a received group MMS with its picture's base64, a
// sent MMS, a picture alone, a draft.
const sample = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<!--File Created By SMS Backup & Restore v10.20.002 on 19/11/2026 10:12:33-->
<?xml-stylesheet type="text/xsl" href="sms.xsl"?>
<smses count="6" backup_set="0f1c2d3e" backup_date="1763518353000" type="full">
  <sms protocol="0" address="+8613800138000" date="1763500000000" type="1" subject="null" body="Lunch at 12? &#55357;&#56832;&#10;Bring the &quot;docs&quot; &amp; keys" toa="null" sc_toa="null" service_center="+8613800100500" read="1" status="-1" locked="0" date_sent="1763499999000" sub_id="1" readable_date="Nov 19, 2026 05:06:40" contact_name="Zhang San" />
  <sms protocol="0" address="13800138000" date="1763500060000" type="2" subject="null" body="OK" toa="null" sc_toa="null" service_center="null" read="1" status="-1" locked="0" date_sent="0" sub_id="1" readable_date="Nov 19, 2026 05:07:40" contact_name="Zhang San" />
  <sms protocol="0" address="10086" date="1763500120000" type="3" subject="null" body="a draft" read="1" />
  <sms protocol="0" address="10086" date="1763500180000" type="5" body="balance" read="1" />
  <mms date="1763500200000" rr="null" sub="Trip" ct_t="application/vnd.wap.multipart.related" read_status="null" seen="1" msg_box="1" address="+8613900139000~+8613700137000~+8613600136000" sub_cs="106" read="0" m_type="132" text_only="0" readable_date="Nov 19, 2026 05:10:00" contact_name="(Unknown)">
    <parts>
      <part seq="-1" ct="application/smil" name="null" chset="null" cd="null" fn="null" cid="&lt;smil&gt;" cl="smil.xml" ctt_s="null" ctt_t="null" text='&lt;smil&gt;&lt;body&gt;&lt;/body&gt;&lt;/smil&gt;' />
      <part seq="0" ct="image/jpeg" name="IMG_1.jpg" chset="null" cd="null" fn="null" cid="&lt;img&gt;" cl="IMG_1.jpg" ctt_s="null" ctt_t="null" text="null" data="/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/" />
      <part seq="0" ct="text/plain" name="null" chset="106" cd="null" fn="null" cid="&lt;text&gt;" cl="text.txt" ctt_s="null" ctt_t="null" text="Who is in?" />
    </parts>
    <addrs>
      <addr address="+8613900139000" type="137" charset="106" />
      <addr address="+8613700137000" type="151" charset="106" />
      <addr address="+8613600136000" type="151" charset="106" />
      <addr address="+8613500135000" type="151" charset="106" />
    </addrs>
  </mms>
  <mms date="1763500300000" sub="null" msg_box="2" address="+8613700137000" read="1" m_type="128" text_only="1">
    <parts>
      <part seq="0" ct="text/plain" chset="106" text="Me!" />
    </parts>
    <addrs>
      <addr address="insert-address-token" type="137" charset="106" />
      <addr address="+8613700137000" type="151" charset="106" />
    </addrs>
  </mms>
  <mms date="1763500400000" sub="null" msg_box="1" address="+8613900139000" read="1" m_type="132">
    <parts><part seq="0" ct="image/png" name="a.png" data="iVBORw0KGgo=" /></parts>
    <addrs><addr address="+8613900139000" type="137" charset="106" /></addrs>
  </mms>
</smses>
`

func TestParseSample(t *testing.T) {
	msgs, st, err := Parse(strings.NewReader(sample), "+86 135 0013 5000")
	if err != nil {
		t.Fatal(err)
	}
	if st.Messages != 5 || st.Skipped != 2 || st.Truncated {
		t.Fatalf("stats %+v", st)
	}
	want := []ui.SMS{
		{Peer: "+8613800138000", Dir: "in", Text: "Lunch at 12? \U0001F600\nBring the \"docs\" & keys", At: 1763500000, Read: true},
		{Peer: "13800138000", Dir: "out", Text: "OK", At: 1763500060, Read: true},
		{Peer: "10086", Dir: "out", Text: "balance", At: 1763500180, Read: true, State: ui.SMSFailed},
		{Peer: "+8613900139000", Dir: "in", Kind: ui.SMSKindMMS, Subject: "Trip", Text: "Who is in?", At: 1763500200,
			Members: []string{"+8613900139000", "+8613700137000", "+8613600136000"}},
		{Peer: "+8613700137000", Dir: "out", Kind: ui.SMSKindMMS, Text: "Me!", At: 1763500300, Read: true},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", msgs, want)
	}
	// Without this phone's number, every other recipient is a member.
	msgs, _, _ = Parse(strings.NewReader(sample), "")
	if got := msgs[3].Members; len(got) != 4 {
		t.Fatalf("members without the own number: %q", got)
	}
}

// What the phone writes reads back the same, here and in the app's own
// reader's terms (one message an element, the attributes it reads).
func TestWriteReadsBack(t *testing.T) {
	msgs, _, err := Parse(strings.NewReader(sample), "+8613500135000")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	names := func(n string) string {
		if n == "+8613800138000" {
			return "Zhang <San>"
		}
		return ""
	}
	scheduled := ui.SMS{Peer: "10086", Dir: "out", Text: "later", At: 1763600000, State: ui.SMSScheduled, Due: 1763700000}
	if n, err := Write(&b, append(msgs, scheduled), names, "+8613500135000", time.Unix(1763518353, 0)); err != nil || n != len(msgs) {
		t.Fatal(n, err)
	}
	out := b.String()
	for _, s := range []string{`<smses count="5" backup_date="1763518353000"`, `contact_name="Zhang &lt;San&gt;"`, `body="Lunch at 12? ` + "\U0001F600" + `&#10;Bring`,
		`type="5"`, `msg_box="1" address="+8613900139000~+8613700137000~+8613600136000"`, `<addr address="+8613500135000" type="151"`} {
		if !strings.Contains(out, s) {
			t.Errorf("written backup lacks %q", s)
		}
	}
	again, st, err := Parse(strings.NewReader(out), "+8613500135000")
	if err != nil || st.Skipped != 0 || !reflect.DeepEqual(again, msgs) {
		t.Fatalf("read back (%+v, %v):\n%+v\nwant\n%+v", st, err, again, msgs)
	}
}

// The readable dates are on the phone's clock, which the Shell passes as
// now's zone; the process's own zone (time.Local) is not the phone's.
func TestWriteReadableDateInNowsZone(t *testing.T) {
	msgs := []ui.SMS{{ID: "m1", Peer: "10086", Dir: "in", Text: "hi", At: 1763500000, Read: true}}
	for zone, want := range map[string]string{"Asia/Shanghai": "Nov 19, 2025 5:06:40 AM", "America/Mexico_City": "Nov 18, 2025 3:06:40 PM"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if _, err := Write(&b, msgs, func(string) string { return "" }, "", time.Unix(1763518353, 0).In(loc)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), `readable_date="`+want+`"`) {
			t.Errorf("%s: %s", zone, b.String())
		}
	}
}

func TestParseRefusesAndLimits(t *testing.T) {
	for _, s := range []string{"", "hello", "<html><body>no</body></html>", `<?xml version="1.0"?><!-- <smses> -->`} {
		if _, _, err := Parse(strings.NewReader(s), ""); !errors.Is(err, ErrNotBackup) {
			t.Errorf("%q: %v", s, err)
		}
	}
	// Cut short, a file keeps the messages it gave; a lone surrogate, a
	// control character and an unknown reference do not stop it.
	cut := `<smses><sms address="1" date="1763500000" type="1" body="a&#55357;b&#1;c&foo;d &#xD83D;&#xDE00;" /><sms address="2" date="17635`
	msgs, st, err := Parse(strings.NewReader(cut), "")
	if err != nil || len(msgs) != 1 || msgs[0].Text != "a\uFFFDbc&foo;d \U0001F600" || msgs[0].At != 1763500000 {
		t.Fatalf("%+v %+v %v", msgs, st, err)
	}
	long := `<smses><sms address="10086" date="1763500000000" type="1" body="` + strings.Repeat("\u4e2d", 3*MaxText) + `" data="` + strings.Repeat("A", 1<<20) + `"/>`
	msgs, _, _ = Parse(strings.NewReader(long), "")
	if len(msgs) != 1 || len([]rune(msgs[0].Text)) != MaxText {
		t.Fatalf("a long body: %d characters", len([]rune(msgs[0].Text)))
	}
	old := `<smses><sms address="10086" date="5000" type="1" body="1970" /><sms address="` + strings.Repeat("1", MaxAddress+1) + `" date="1763500000000" type="1" body="x"/></smses>`
	if msgs, st, _ := Parse(strings.NewReader(old), ""); len(msgs) != 0 || st.Skipped != 2 {
		t.Fatalf("an old date, a long address: %+v %+v", msgs, st)
	}
}
