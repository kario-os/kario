package mms

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzPush is the user data of an 8-bit SMS from anyone: a WAP push is
// read (and an MMS PDU or service indication in it) or refused, never a
// panic, and what comes out lies within the input.
func FuzzPush(f *testing.F) {
	f.Add(unhex(f, notificationHex))
	f.Add(cat(unhex(f, "06 05 04 0b84 23f0"), unhex(f, notificationHex)))
	f.Add(unhex(f, shortNotificationHex))
	f.Add(unhex(f, deliveryReportHex))
	f.Add(NotificationPush("t1", "+8613800138000", "Hi", "http://127.0.0.1/mms/m1", 1000, 3600))
	f.Add(DeliveryPush("id9", "+8613900139000", StatusRetrieved, 1700000000))
	f.Add(EncodePush(3, TypeSIC, cat(unhex(f, "02 05 6a 00 45 c6 0c 03"), []byte("example.com/x\x00"), unhex(f, "01 03"), []byte("Update\x00"), unhex(f, "01 01"))))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4<<10 {
			return
		}
		p, err := ParsePush(data)
		if err != nil {
			return
		}
		if len(p.Body) > len(data) {
			t.Fatalf("body of %d from %d bytes", len(p.Body), len(data))
		}
		if p.IsMMS() {
			if m, err := Decode(p.Body); err == nil && len(m.Parts) > MaxParts {
				t.Fatalf("%d parts", len(m.Parts))
			}
		}
		if si, err := ParseSI(p.Body); err == nil {
			if len(si.Text) > maxSIText+len(data) || len(si.Href) > maxSIText+64 {
				t.Fatalf("service indication of %d / %d bytes", len(si.Text), len(si.Href))
			}
		}
	})
}

// FuzzPDU is an MMS PDU from an MMSC (or anyone answering at a content
// location): it decodes or is refused; a decoded one is written again and
// reads back with the same type, recipients, subject and parts.
func FuzzPDU(f *testing.F) {
	f.Add(retrieveConf(f, []byte("\xff\xd8\xff\xe0jpeg")))
	f.Add(unhex(f, notificationHex)[37:])
	for _, p := range []*PDU{
		NewSendReq("tx1", []string{"+8613900139000", "13700137000"}, "Trip", "See you", []Attachment{{ContentType: TypeJPEG, Data: []byte("jpeg")}}),
		{Type: SendConf, TransactionID: "tx1", ResponseStatus: ResponseOK, MessageID: "id9"},
		{Type: DeliveryInd, MessageID: "id9", To: []string{"+8613900139000"}, Date: 1700000000, Status: StatusRetrieved},
	} {
		raw, _ := Encode(p)
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8<<10 {
			return
		}
		p, err := Decode(data)
		if err != nil {
			return
		}
		if len(p.Parts) > MaxParts || len(p.To) > maxRecipients {
			t.Fatalf("%d parts, %d recipients", len(p.Parts), len(p.To))
		}
		for _, part := range Order(p) {
			_ = part.Text()
		}
		if p.ContentType == "" && p.Body != nil {
			return
		}
		for _, part := range p.Parts {
			if part.ContentType == "" {
				return
			}
		}
		raw, err := Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		q, err := Decode(raw)
		if err != nil {
			t.Fatalf("%+v written as %x does not read back: %v", p, raw, err)
		}
		if q.Type != p.Type || !reflect.DeepEqual(q.To, p.To) || !reflect.DeepEqual(q.Cc, p.Cc) || len(q.Parts) != len(p.Parts) {
			t.Fatalf("\n got %+v\nwant %+v", q, p)
		}
		if utf8.ValidString(p.Subject) && !strings.ContainsRune(p.Subject, 0) && q.Subject != p.Subject {
			t.Fatalf("subject %q read back as %q", p.Subject, q.Subject)
		}
		for i := range p.Parts {
			if !bytes.Equal(q.Parts[i].Data, p.Parts[i].Data) {
				t.Fatalf("part %d changed", i)
			}
		}
	})
}
