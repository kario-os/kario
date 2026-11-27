package shell

import "testing"

// The fdstore step tells an app's connection from the copy of its own
// connection, one app from another whose id it starts with, and the
// app's process from an earlier one.
func TestFdStoreHas(t *testing.T) {
	names := []string{connName("com.example.keepalive", 40) + ".v1", keepName("com.example.keepalivets", 41), "keep-com.example.calc-x"}
	for _, c := range []struct {
		kind, id string
		pid      int
		want     bool
	}{
		{"app", "com.example.keepalive", 40, true},
		{"app", "com.example.keepalive", 0, true},
		{"app", "com.example.keepalive", 39, false},
		{"keep", "com.example.keepalive", 0, false},
		{"keep", "com.example.keepalivets", 41, true},
		{"app", "com.example.keepalivets", 0, false},
		{"keep", "com.example.calc", 0, false},
	} {
		if got := fdStoreHas(names, c.kind, c.id, c.pid); got != c.want {
			t.Errorf("%s %s pid %d: %v, want %v", c.kind, c.id, c.pid, got, c.want)
		}
	}
}
