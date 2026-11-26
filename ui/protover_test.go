package ui

import "testing"

// Versions are "phone.ui/<major>[.<minor>]"; the newest major both sides
// speak is taken, proto and protos alike; hello naming none is the
// system's own; nothing in common is refused.
func TestNegotiate(t *testing.T) {
	if ProtoName(ProtoMajor) != Proto {
		t.Fatalf("ProtoName(%d) = %q, Proto %q", ProtoMajor, ProtoName(ProtoMajor), Proto)
	}
	for p, want := range map[string]int{"phone.ui/0": 0, "phone.ui/7": 7, "phone.ui/1.12": 1, "phone.ui/12": 12} {
		if got, ok := ParseProto(p); !ok || got != want {
			t.Errorf("ParseProto(%q) = %d %v", p, got, ok)
		}
	}
	for _, p := range []string{"", "phone.ui/", "phone.ui/x", "phone.ui/-1", "phone.ui/01", "phone.ui/1.", "phone.ui/1.x", "phone.ui/1.-2", "other/1", "phone.ui/1.02"} {
		if _, ok := ParseProto(p); ok {
			t.Errorf("ParseProto(%q) took it", p)
		}
	}
	for _, tc := range []struct {
		majors []int
		proto  string
		protos []string
		want   int
		ok     bool
	}{
		{[]int{0}, "phone.ui/0", nil, 0, true},
		{[]int{0}, "phone.ui/0.4", nil, 0, true},
		{[]int{0}, "", nil, 0, true},
		{[]int{0}, "phone.ui/1", nil, 0, false},
		{[]int{1, 0}, "phone.ui/0", nil, 0, true},
		{[]int{1, 0}, "phone.ui/0", []string{"phone.ui/1", "phone.ui/0"}, 1, true},
		{[]int{1, 0}, "", []string{"phone.ui/1.3"}, 1, true},
		{[]int{1, 0}, "phone.ui/2", []string{"bogus", "phone.ui/3"}, 0, false},
		{[]int{2, 1}, "phone.ui/0", []string{"phone.ui/1"}, 1, true},
		{[]int{2, 1}, "phone.ui/0", nil, 0, false},
		{[]int{2, 1}, "", nil, 2, true},
	} {
		got, ok := Negotiate(tc.majors, tc.proto, tc.protos)
		if ok != tc.ok || ok && got != tc.want {
			t.Errorf("Negotiate(%v, %q, %q) = %d %v, want %d %v", tc.majors, tc.proto, tc.protos, got, ok, tc.want, tc.ok)
		}
	}
}
