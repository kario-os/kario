package appstore

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
)

// A company's credit code is public and kept whole; a person's ID number
// only masked.
func TestCompanyCreditIsPublic(t *testing.T) {
	_, ts := newServer(t)
	pub := b64.EncodeToString(make([]byte, ed25519.PublicKeySize))
	var d Developer
	if code := call(t, "POST", ts.URL+"/v1/developers", "", map[string]string{"name": "Example Co.", "pub": pub, "kind": "company",
		"number": "91110108MA01ABCD2H"}, &d); code != 200 || d.Credit != "91110108MA01ABCD2H" {
		t.Fatalf("company: %d %+v", code, d)
	}
	pub2 := b64.EncodeToString(bytes.Repeat([]byte{2}, ed25519.PublicKeySize))
	var p Developer
	if code := call(t, "POST", ts.URL+"/v1/developers", "", map[string]string{"name": "Zhang San", "pub": pub2, "kind": "person",
		"number": "11010519491231002X"}, &p); code != 200 || p.Credit != "" || strings.Contains(p.Number, "110105") {
		t.Fatalf("person: %d %+v", code, p)
	}
}

// Anyone reports a published app, a bounded number of times an hour; the
// staff see the open reports in their queue, and once one is handled
// (HandleReport) it leaves the queue.
func TestReports(t *testing.T) {
	s, ts := newServer(t)
	s.catalog["com.example.notes"] = Entry{ID: "com.example.notes", Name: "Notes", Version: "1.0"}
	var got map[string]string
	if code := call(t, "POST", ts.URL+"/v1/reports", "", map[string]string{"app": "com.example.notes", "reason": "Pushes users to share it"}, &got); code != 200 || got["id"] == "" {
		t.Fatalf("report: %d %v", code, got)
	}
	for body, want := range map[string]int{`{"app":"com.example.nope","reason":"x"}`: 404, `{"app":"com.example.notes","reason":" "}`: 400} {
		if code := call(t, "POST", ts.URL+"/v1/reports", "", []byte(body), nil); code != want {
			t.Errorf("%s: %d, want %d", body, code, want)
		}
	}
	var q struct {
		Reports []Report `json:"reports"`
	}
	if code := call(t, "GET", ts.URL+"/v1/admin/queue", token, nil, &q); code != 200 || len(q.Reports) != 1 {
		t.Fatalf("queue: %d %+v", code, q)
	}
	if rep, err := s.HandleReport(got["id"], "Warned the developer", "Li Si"); err != nil || rep.By != "Li Si" {
		t.Fatalf("handle: %v %+v", err, rep)
	}
	if call(t, "GET", ts.URL+"/v1/admin/queue", token, nil, &q); len(q.Reports) != 0 {
		t.Fatalf("handled reports stay open: %+v", q.Reports)
	}
	for i := 0; i < reportsPerHour; i++ {
		call(t, "POST", ts.URL+"/v1/reports", "", map[string]string{"app": "com.example.notes", "reason": "spam"}, nil)
	}
	if code := call(t, "POST", ts.URL+"/v1/reports", "", map[string]string{"app": "com.example.notes", "reason": "spam"}, nil); code != 429 {
		t.Fatalf("report flood: %d", code)
	}
}
