package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// sandbox-read on an app that never started (the case alone, as the whole
// phone's first selftest run had it) finds the app's data directory there,
// as the app's first start makes it: as root the sandbox gives the
// directory to the app's uid, which failed with "chown …: no such file or
// directory" (2026-11-24). The launcher here says whether the directory of
// the policy it is handed is there.
func TestSandboxReadBeforeTheAppStarted(t *testing.T) {
	root := t.TempDir()
	apps, bin := filepath.Join(root, "apps"), filepath.Join(root, "bin")
	for p, s := range map[string]string{
		filepath.Join(apps, "chat", "manifest.json"): `{"id":"com.phone.chat","name":"Chat","exec":"none","permissions":["net"]}`,
		filepath.Join(bin, "phone-sandbox"): "#!/bin/sh\ndir=$(printf %s \"$1\" | sed -n 's/.*\"data_dir\":\"\\([^\"]*\\)\".*/\\1/p')\n" +
			"if [ -d \"$dir\" ]; then echo no-landlock; else echo \"no $dir\"; fi\n",
	} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run, err := os.MkdirTemp("", "sandboxread-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, AppBinDir: bin, RunDir: run, StateDir: filepath.Join(root, "state"), FreezeAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	var out bytes.Buffer
	r := &Runner{C: c, Out: &out}
	if err := r.sandboxRead("com.phone.chat /etc/resolv.conf allowed"); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(root, "state", "apps", "com.phone.chat")); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the app's directory: %v %v", st, err)
	}
	if !bytes.Contains(out.Bytes(), []byte("no Landlock in com.phone.chat's sandbox")) {
		t.Errorf("the step said %q", out.String())
	}
}
