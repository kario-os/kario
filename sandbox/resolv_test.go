package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The resolver's configuration links, as on the image, into
// systemd-resolved's directory: that directory is what the network
// permission adds, whether or not the file is there yet.
func TestNetReadOnly(t *testing.T) {
	root := t.TempDir()
	etc, run := filepath.Join(root, "etc"), filepath.Join(root, "run", "systemd", "resolve")
	for _, d := range []string{etc, run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	conf := filepath.Join(etc, "resolv.conf")
	if err := os.Symlink("resolv-conf.systemd", conf); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../run/systemd/resolve/resolv.conf", filepath.Join(etc, "resolv-conf.systemd")); err != nil {
		t.Fatal(err)
	}
	if got := NetReadOnly(conf); !slices.Equal(got, []string{run}) {
		t.Fatalf("a link not yet resolved: %v, want %s", got, run)
	}
	if err := os.WriteFile(filepath.Join(run, "resolv.conf"), []byte("nameserver 10.0.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NetReadOnly(conf); !slices.Equal(got, []string{run}) {
		t.Fatalf("%v, want %s", got, run)
	}
	plain := filepath.Join(root, "plain.conf")
	if err := os.WriteFile(plain, []byte("nameserver 10.0.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NetReadOnly(plain); got != nil {
		t.Fatalf("a plain file needs nothing more: %v", got)
	}
	if got := NetReadOnly("/etc/no-such-resolv.conf"); got != nil {
		t.Fatalf("no file: %v", got)
	}
}
