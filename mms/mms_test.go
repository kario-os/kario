package mms

import (
	"bytes"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func uv(n int) []byte {
	var w writer
	w.uintvar(n)
	return w.b
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// A notification as operators send it: the content type spelt out in the
// push headers (not the well-known number), X-Wap-Application-Id 4, MMS
// 1.2, a relative expiry and the content location.
const notificationHex = `
00 06 22 6170706c69636174696f6e2f766e642e7761702e6d6d732d6d65737361676500 af 84
8c 82
98 543230323331313131303030303100
8d 92
89 1a 80 2b383631333830303133383030302f545950453d504c4d4e00
8a 80
8e 03 01 86 a0
88 05 81 03 09 3a 80
83 687474703a2f2f6d6d73632e6d6f6e7465726e65742e636f6d2f3f6d6573736167652d69643d61626300`

func TestNotificationPush(t *testing.T) {
	for name, data := range map[string][]byte{
		"plain": unhex(t, notificationHex),
		// The user data header (destination port 2948, source 9200) still in front.
		"with header": cat(unhex(t, "06 05 04 0b84 23f0"), unhex(t, notificationHex)),
	} {
		push, err := ParsePush(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !push.IsMMS() || push.ContentType != TypeMMS || push.AppID != "4" {
			t.Fatalf("%s: %+v", name, push)
		}
		p, err := Decode(push.Body)
		if err != nil {
			t.Fatal(err)
		}
		want := &PDU{Type: NotificationInd, TransactionID: "T2023111100001", Version: 0x12, From: "+8613800138000", Class: "personal",
			Size: 100000, ExpiryAfter: 604800, ContentLocation: "http://mmsc.monternet.com/?message-id=abc"}
		if !reflect.DeepEqual(p, want) {
			t.Fatalf("%s:\n got %+v\nwant %+v", name, p, want)
		}
	}
	if _, err := ParsePush(unhex(t, "00 05 01 be")); err == nil {
		t.Error("a WSP reply is not a push")
	}
}

// The short forms other gateways use: the content type as its WSP number
// (0x3E), Push-Flag after the application ID; an MMS 1.0 notification
// with an encoded subject and an absolute expiry, and a delivery report.
const (
	shortNotificationHex = `01 06 05 be af 84 b4 84
		8c 82 98 31323334353637383930 00 8d 90
		89 17 80 31333830303133383030302f545950453d504c4d4e 00
		96 08 ea e4bda0e5a5bd 00 8a 80 8e 02 4e20
		88 06 80 04 654f1a80
		83 687474703a2f2f3231312e3133362e3131322e38342f6d6d733f69643d78797a 00`
	deliveryReportHex = `02 06 03 be af 84
		8c 86 8d 90 8b 6d73672d69642d31 00
		97 2b38363133393030313339303030 2f545950453d504c4d4e 00
		85 04 654f1a80 95 81`
)

func TestShortFormPushes(t *testing.T) {
	push, err := ParsePush(unhex(t, shortNotificationHex))
	if err != nil || !push.IsMMS() || push.ContentType != TypeMMS || push.AppID != "4" {
		t.Fatalf("%+v %v", push, err)
	}
	p, err := Decode(push.Body)
	want := &PDU{Type: NotificationInd, TransactionID: "1234567890", Version: 0x10, From: "13800138000", Subject: "\u4f60\u597d",
		Class: "personal", Size: 20000, Expiry: 1699682944, ContentLocation: "http://211.136.112.84/mms?id=xyz"}
	if err != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("%v\n got %+v\nwant %+v", err, p, want)
	}
	push, err = ParsePush(unhex(t, deliveryReportHex))
	if err != nil {
		t.Fatal(err)
	}
	p, err = Decode(push.Body)
	want = &PDU{Type: DeliveryInd, Version: 0x10, MessageID: "msg-id-1", To: []string{"+8613900139000"}, Date: 1699682944, Status: StatusRetrieved}
	if err != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("%v\n got %+v\nwant %+v", err, p, want)
	}
}

// retrieveConf is an m-retrieve.conf put together octet by octet: a
// presentation, a picture and UTF-8 text in a multipart/related body, the
// subject in UTF-8 with its character set.
func retrieveConf(t testing.TB, picture []byte) []byte {
	smil := []byte(`<smil><body><par><text src="text.txt"/><img src="cid:pic"/></par></body></smil>`)
	smilHead := cat([]byte("application/smil\x00"), unhex(t, "c0 22"), []byte("<smil>\x00"), unhex(t, "8e"), []byte("smil.xml\x00"))
	picHead := cat(unhex(t, "0a 9e 85"), []byte("pic.jpg\x00"), unhex(t, "c0 22"), []byte("<pic>\x00"))
	text := unhex(t, "48656c6c6f20 e4b896 e7958c")
	textHead := cat(unhex(t, "03 83 81 ea 8e"), []byte("text.txt\x00"))
	return cat(
		unhex(t, "8c 84 98"), []byte("T1\x00"), unhex(t, "8d 92 8b"), []byte("msg-1\x00"),
		unhex(t, "85 04 654f1a80"),
		unhex(t, "89 1a 80"), []byte("+8613800138000/TYPE=PLMN\x00"),
		unhex(t, "97"), []byte("+8613900139000/TYPE=PLMN\x00"),
		unhex(t, "82"), []byte("+8613700137000/TYPE=PLMN\x00"),
		unhex(t, "96 0b ea 486920 e4bda0 e5a5bd 00"),
		unhex(t, "84 1b b3 8a"), []byte("<smil>\x00"), unhex(t, "89"), []byte("application/smil\x00"),
		unhex(t, "03"),
		uv(len(smilHead)), uv(len(smil)), smilHead, smil,
		uv(len(picHead)), uv(len(picture)), picHead, picture,
		uv(len(textHead)), uv(len(text)), textHead, text,
	)
}

func TestRetrieveConf(t *testing.T) {
	pic := []byte("\xff\xd8\xff\xe0 not really a jpeg")
	p, err := Decode(retrieveConf(t, pic))
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != RetrieveConf || p.From != "+8613800138000" || !reflect.DeepEqual(p.To, []string{"+8613900139000"}) ||
		!reflect.DeepEqual(p.Cc, []string{"+8613700137000"}) || p.Subject != "Hi \u4f60\u597d" || p.Date != 1699682944 ||
		p.ContentType != TypeRelated || p.Start != "<smil>" || p.Related != TypeSMIL || len(p.Parts) != 3 {
		t.Fatalf("%+v", p)
	}
	if pt := p.Parts[1]; pt.ContentType != TypeJPEG || pt.Name != "pic.jpg" || pt.ContentID != "pic" || !bytes.Equal(pt.Data, pic) {
		t.Errorf("picture %+v", pt)
	}
	ordered := Order(p)
	if len(ordered) != 2 || ordered[0].Text() != "Hello \u4e16\u754c" || ordered[1].ContentID != "pic" {
		t.Errorf("SMIL order: %+v", ordered)
	}
	// Truncated anywhere, it is an error, never a panic.
	raw := retrieveConf(t, pic)
	for i := range raw {
		_, _ = Decode(raw[:i])
	}
}

func TestCharsets(t *testing.T) {
	gbk := Part{ContentType: TypeText, Charset: "gbk", Data: unhex(t, "c4e3 bac3")}
	if got := gbk.Text(); got != "\u4f60\u597d" {
		t.Errorf("GBK %q", got)
	}
	r := reader{b: cat(unhex(t, "08 02 03 e8"), unhex(t, "4f60 597d 00"))}
	if got := r.encodedString(); got != "\u4f60\u597d" || r.err != nil {
		t.Errorf("UCS-2 subject %q %v", got, r.err)
	}
	if got := (Part{Data: []byte{0xe9, 't', 0xe9}}).Text(); got != "\u00e9t\u00e9" {
		t.Errorf("not UTF-8, read as Latin-1: %q", got)
	}
}

func TestRoundTrip(t *testing.T) {
	req := NewSendReq("tx1", []string{"+8613900139000", "13700137000"}, "Trip", "See you \u4f60\u597d",
		[]Attachment{{ContentType: TypeJPEG, Data: []byte("jpegdata")}})
	raw, err := Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != SendReq || got.TransactionID != "tx1" || got.Version != Version13 || got.From != "" ||
		!reflect.DeepEqual(got.To, []string{"+8613900139000", "13700137000"}) || got.Subject != "Trip" || !got.DeliveryReport ||
		got.Start != "<smil>" || got.Related != TypeSMIL || len(got.Parts) != 3 {
		t.Fatalf("%+v", got)
	}
	ordered := Order(got)
	if len(ordered) != 2 || ordered[0].ContentType != TypeJPEG || string(ordered[0].Data) != "jpegdata" || ordered[1].Text() != "See you \u4f60\u597d" {
		t.Fatalf("%+v", ordered)
	}
	for _, p := range []*PDU{
		{Type: NotifyRespInd, TransactionID: "tx1", Status: StatusRetrieved},
		{Type: DeliveryInd, MessageID: "id9", To: []string{"+8613900139000"}, Date: 1700000000, Status: StatusRetrieved},
		{Type: SendConf, TransactionID: "tx1", ResponseStatus: ResponseOK, MessageID: "id9"},
		{Type: NotificationInd, TransactionID: "t", From: "+12015550123", Subject: "\u00a1Hola!", Size: 300, Expiry: 1800000000, ContentLocation: "http://x/1"},
	} {
		raw, err := Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(raw)
		p.Version = Version13
		if err != nil || !reflect.DeepEqual(got, p) {
			t.Errorf("%d: %v\n got %+v\nwant %+v", p.Type, err, got, p)
		}
	}
}

func TestSI(t *testing.T) {
	si, err := ParseSI(cat(unhex(t, "02 05 6a 00 45 c6 0c 03"), []byte("example.com/x\x00"), unhex(t, "11 03"), []byte("id1\x00"),
		unhex(t, "01 03"), []byte("Update available\x00"), unhex(t, "01 01")))
	if err != nil {
		t.Fatal(err)
	}
	if si.Href != "http://example.com/x" || si.Text != "Update available" || si.ID != "id1" {
		t.Errorf("%+v", si)
	}
	push, err := ParsePush(EncodePush(3, TypeSIC, []byte{2, 5, 0x6a, 0}))
	if err != nil || push.ContentType != TypeSIC || push.IsMMS() {
		t.Errorf("%+v %v", push, err)
	}
	if _, err := ParseSI(unhex(t, "02 05 6a 00 45 c6 ff")); err == nil {
		t.Error("an unknown attribute is refused")
	}
}

func TestFitImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1500))
	for y := 0; y < 1500; y++ {
		for x := 0; x < 2000; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 13), uint8(x ^ y), 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	out, mt, err := FitImage(b.Bytes(), 300<<10)
	if err != nil || mt != TypeJPEG || len(out) > 300<<10 {
		t.Fatalf("%s %d %v", mt, len(out), err)
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(out))
	if max(cfg.Width, cfg.Height) > maxSendSide {
		t.Errorf("%dx%d", cfg.Width, cfg.Height)
	}
	small := image.NewRGBA(image.Rect(0, 0, 20, 10))
	b.Reset()
	_ = png.Encode(&b, small)
	if out, mt, err := FitImage(b.Bytes(), 300<<10); err != nil || mt != TypePNG || !bytes.Equal(out, b.Bytes()) {
		t.Errorf("a small picture goes as it is: %s %v", mt, err)
	}
	if _, _, err := FitImage([]byte("text"), 1000); err == nil {
		t.Error("not a picture")
	}
}

func TestFakeMMSC(t *testing.T) {
	f := &FakeMMSC{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	loc, err := f.Offer(srv.URL, RetrieveConfOf("+8613800138000", []string{"+8613900139000"}, "s", 1, []Part{{ContentType: TypeText, Data: []byte("x")}}, ""))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if p, err := Decode(raw); err != nil || p.Type != RetrieveConf || p.From != "+8613800138000" {
		t.Fatalf("%+v %v", p, err)
	}
	req, _ := Encode(NewSendReq("t9", []string{"10086"}, "", "hi", nil))
	resp, err = http.Post(srv.URL, TypeMMS, bytes.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if p, err := Decode(raw); err != nil || p.Type != SendConf || p.ResponseStatus != ResponseOK || p.MessageID == "" || len(f.Sent()) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
}
