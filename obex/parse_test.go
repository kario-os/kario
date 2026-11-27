package obex

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// The simulated old phones' fixtures, written as Android and an iPhone
// write their objects.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("btphone/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var shanghai = time.FixedZone("CST", 8*3600)

func TestSplitVCards(t *testing.T) {
	cs := SplitVCards(fixture(t, "android/pb.vcf"))
	if len(cs) != 4 || !strings.Contains(cs[0], "Redmi Note 9") || !strings.HasSuffix(cs[2], "END:VCARD\r\n") {
		t.Fatalf("android: %d cards: %q", len(cs), cs)
	}
	// The quoted-printable note's soft breaks stay inside its card.
	if !strings.Contains(cs[2], "=\r\n=E5=B7=9E") {
		t.Errorf("soft break: %q", cs[2])
	}
	if cs := SplitVCards(fixture(t, "iphone/pb.vcf")); len(cs) != 2 || !strings.Contains(cs[1], "VERSION:3.0") {
		t.Errorf("iphone: %q", cs)
	}
	odd := "junk\nbegin:vcard\nN:a\nAGENT:\nBEGIN:VCARD\nN:b\nEND:VCARD\nend:vcard\nBEGIN:VCARD\nN:cut"
	if cs := SplitVCards([]byte(odd)); len(cs) != 1 || strings.Count(cs[0], "\r\n") != 7 {
		t.Errorf("nested, lower case, cut off: %q", cs)
	}
}

func TestParseCall(t *testing.T) {
	at := func(s string) time.Time { x, _ := time.ParseInLocation("20060102T150405", s, shanghai); return x }
	for _, tc := range []struct {
		file, folder string
		i            int
		want         Call
		err          error
	}{
		{"android/ich.vcf", "ich", 0, Call{"+8613655550201", "RECEIVED", at("20261116T201500")}, nil},
		{"android/ich.vcf", "ich", 1, Call{"Unknown", "RECEIVED", at("20261115T090000")}, nil},
		{"android/och.vcf", "och", 0, Call{"10086", "DIALED", at("20261116T183000")}, nil},
		{"android/mch.vcf", "cch", 0, Call{"136-5555-0202", "MISSED", at("20261116T120500")}, nil},
		{"iphone/ich.vcf", "cch", 0, Call{"+86 136 5555 0301", "RECEIVED", at("20261117T081000")}, nil},
		{"iphone/mch.vcf", "mch", 0, Call{"+86 21 5555 0300", "MISSED", at("20261117T073000")}, nil},
	} {
		c, err := ParseCall(SplitVCards(fixture(t, tc.file))[tc.i], tc.folder, shanghai)
		if err != tc.err || c.Number != tc.want.Number || c.Kind != tc.want.Kind || !c.At.Equal(tc.want.At) {
			t.Errorf("%s %d: %+v %v, want %+v", tc.file, tc.i, c, err, tc.want)
		}
	}
	for _, tc := range []struct {
		card, folder string
		err          error
	}{
		{"BEGIN:VCARD\r\nTEL:1\r\nX-IRMC-CALL-DATETIME:20261116T120500\r\nEND:VCARD", "cch", ErrNoKind},
		{"BEGIN:VCARD\r\nTEL:\r\nX-IRMC-CALL-DATETIME;MISSED:20261116T120500\r\nEND:VCARD", "mch", ErrNoNumber},
		{"BEGIN:VCARD\r\nTEL:10086\r\nEND:VCARD", "och", ErrNoTime},
		{"BEGIN:VCARD\r\nTEL:10086\r\nX-IRMC-CALL-DATETIME;DIALED:yesterday\r\nEND:VCARD", "och", ErrNoTime},
	} {
		if _, err := ParseCall(tc.card, tc.folder, shanghai); !errors.Is(err, tc.err) {
			t.Errorf("%q: %v, want %v", tc.card, err, tc.err)
		}
	}
	// A line folded many times took time quadratic in its pieces.
	long := "BEGIN:VCARD\r\nTEL:1" + strings.Repeat("\r\n 0", 200000) + "\r\nX-IRMC-CALL-DATETIME;MISSED:20261116T120500\r\nEND:VCARD\r\n"
	start := time.Now()
	if c, err := ParseCall(long, "mch", shanghai); err != nil || len(c.Number) != 200001 || time.Since(start) > time.Second {
		t.Errorf("folded %d times: %d digits, %v after %v", 200000, len(c.Number), err, time.Since(start))
	}
	c, err := ParseCall("BEGIN:VCARD\r\nitem1.TEL;TYPE=CELL:tel:+861\r\n 0086;ext=1\r\nX-IRMC-CALL-DATETIME;TYPE=dialed;VALUE=DATE-TIME:20261116T120500Z\r\nEND:VCARD", "ich", shanghai)
	if err != nil || c.Number != "+8610086" || c.Kind != "DIALED" || !c.At.Equal(time.Date(2026, 11, 16, 12, 5, 0, 0, time.UTC)) {
		t.Errorf("folded tel: URI, group, lower-case kind, UTC: %+v %v", c, err)
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 11, 16, 10, 15, 0, 0, time.UTC)
	for _, s := range []string{"20261116T181500", "20261116T101500Z", "20261116T181500+0800", "20261116T181500+08:00", " 2026-11-16T18:15:00 "} {
		if got, err := ParseTime(s, shanghai); err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	if _, err := ParseTime("20261316T181500", shanghai); err == nil {
		t.Error("month 13 read")
	}
}

func TestParseListing(t *testing.T) {
	es := ParseListing(fixture(t, "android/inbox.xml"), shanghai)
	if len(es) != 3 {
		t.Fatalf("%d entries: %+v", len(es), es)
	}
	first, second, mms := es[0], es[1], es[2]
	if first.Handle != "100000000000005" || first.Type != "SMS_GSM" || first.Sender != "+8613655550201" || !first.Read || first.Sent ||
		!first.At.Equal(time.Date(2026, 11, 16, 19, 30, 0, 0, shanghai)) || first.Subject != "\u5468\u65e5\u4e00\u8d77\u53bb\u897f\u6e56\u5417\uff1f" {
		t.Errorf("first: %+v", first)
	}
	if second.Read || !second.At.Equal(time.Date(2026, 11, 16, 2, 15, 0, 0, time.UTC)) {
		t.Errorf("with a UTC offset, unread: %+v", second)
	}
	if mms.Type != "MMS" || mms.Sender != "136-5555-0202" {
		t.Errorf("mms: %+v", mms)
	}
	sent := ParseListing(fixture(t, "android/sent.xml"), shanghai)
	if len(sent) != 1 || !sent[0].Sent || sent[0].Recipient != "+8613655550201" {
		t.Errorf("sent: %+v", sent)
	}
	if es := ParseListing(fixture(t, "iphone/inbox.xml"), shanghai); len(es) != 0 {
		t.Errorf("iphone: %+v", es)
	}
	lax := `<MAP-msg-listing><MSG HANDLE="20" Type="SMS_CDMA" datetime="bad" read="YES" subject="a &amp; b &nbsp;"><msg subject="no handle"/>`
	if es := ParseListing([]byte(lax), shanghai); len(es) != 1 || es[0].Handle != "20" || !es[0].Read || !es[0].At.IsZero() || es[0].Subject != "a & b \u00a0" {
		t.Errorf("lax: %+v", es)
	}
}

func TestParseBMessage(t *testing.T) {
	m, err := ParseBMessage(fixture(t, "android/msg/100000000000005.bmsg"))
	if err != nil || !m.Read || m.Type != "SMS_GSM" || m.Folder != "telecom/msg/inbox" || len(m.From) != 1 || m.From[0] != "+8613655550201" ||
		len(m.To) != 1 || m.To[0] != "+8613800138099" || m.Text != "\u5468\u65e5\u4e00\u8d77\u53bb\u897f\u6e56\u5417\uff1f\n\u65e9\u4e0a\u516b\u70b9\u51fa\u53d1" {
		t.Errorf("inbox sms: %+v %v", m, err)
	}
	m, err = ParseBMessage(fixture(t, "android/msg/100000000000006.bmsg"))
	if err != nil || len(m.To) != 1 || m.To[0] != "+8613655550201" || m.From[0] != "+8613800138099" {
		t.Errorf("sent sms: %+v %v", m, err)
	}
	m, err = ParseBMessage(fixture(t, "android/msg/200000000000002.bmsg"))
	if err != nil || m.Type != "MMS" || m.Text != "\u770b\u770b\u8fd9\u5f20\uff0c\u53bb\u5e74\u5728\u676d\u5dde\u62cd\u7684" {
		t.Errorf("mms: %q %v", m.Text, err)
	}
	// A wrong LENGTH, the text in two parts, an END:MSG inside the text,
	// a forwarded message's inner envelope, LF line ends.
	odd := "BEGIN:BMSG\nVERSION:1.0\nSTATUS:UNREAD\nTYPE:SMS_CDMA\nBEGIN:VCARD\nTEL:100\nEND:VCARD\nBEGIN:BENV\nBEGIN:VCARD\nTEL:200\nEND:VCARD\n" +
		"BEGIN:BENV\nBEGIN:VCARD\nTEL:300\nEND:VCARD\nBEGIN:BBODY\nLENGTH:3\nBEGIN:MSG\none\nEND:MSG\nBEGIN:MSG\ntwo\nEND:MSG\n\nEND:MSG\nEND:BBODY\nEND:BENV\nEND:BENV\nEND:BMSG\n"
	m, err = ParseBMessage([]byte(odd))
	if err != nil || m.Read || m.Text != "one\ntwo\nEND:MSG\n" || len(m.To) != 1 || m.To[0] != "200" || m.From[0] != "100" {
		t.Errorf("odd: %+v %v", m, err)
	}
	native := "BEGIN:BMSG\r\nTYPE:SMS_GSM\r\nBEGIN:BENV\r\nBEGIN:BBODY\r\nCHARSET:native\r\nBEGIN:MSG\r\n0011000B91\r\nEND:MSG\r\nEND:BBODY\r\nEND:BENV\r\nEND:BMSG\r\n"
	if _, err := ParseBMessage([]byte(native)); !errors.Is(err, ErrNative) {
		t.Errorf("native: %v", err)
	}
	for _, bad := range []string{"", "BEGIN:VCARD\r\nEND:VCARD", "BEGIN:BMSG\r\nBEGIN:BBODY\r\nEND:BBODY\r\nEND:BMSG", "BEGIN:BMSG\r\nBEGIN:BBODY\r\nBEGIN:MSG\r\nhi\r\nEND:BMSG"} {
		if _, err := ParseBMessage([]byte(bad)); !errors.Is(err, ErrNotBMessage) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// An old phone nesting components without end took time quadratic
	// in the lines: each line counted the envelopes of all of them.
	deep := "BEGIN:BMSG\r\n" + strings.Repeat("BEGIN:BENV\r\n", 40000) + strings.Repeat("TEL:1\r\n", 40000) + "END:BBODY\r\n"
	start := time.Now()
	if _, err := ParseBMessage([]byte(deep)); !errors.Is(err, ErrNotBMessage) || time.Since(start) > time.Second {
		t.Errorf("deep nesting: %v after %v", err, time.Since(start))
	}
	qp := "BEGIN:BMSG\r\nTYPE:MMS\r\nBEGIN:BENV\r\nBEGIN:BBODY\r\nBEGIN:MSG\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=C3=A9\r\n--b\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\naGVs\r\nbG8=\r\n" +
		"--b\r\nContent-Type: image/jpeg\r\n\r\n\xff\xd8\r\n--b--\r\nEND:MSG\r\nEND:BBODY\r\nEND:BENV\r\nEND:BMSG\r\n"
	if m, err := ParseBMessage([]byte(qp)); err != nil || m.Text != "caf\u00e9\nhello" {
		t.Errorf("mms parts: %q %v", m.Text, err)
	}
}
