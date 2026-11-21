package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/jsrt"
)

// TestE2EPhone is the phone of Kario Studio's end-to-end test
// (ide/vscode/scripts/e2e.mjs): the fake phone TestDAP debugs on, the
// packages signed by the development store $KARIO_E2E_DEVSTORE, run until
// $KARIO_E2E_PHONE.stop appears (or go test is gone, or 15 minutes). It
// writes the environment kario dap needs to reach it to $KARIO_E2E_PHONE
// as JSON, and each page the app shows, a line each, to
// $KARIO_E2E_PHONE.pages.
func TestE2EPhone(t *testing.T) {
	out := os.Getenv("KARIO_E2E_PHONE")
	if out == "" {
		t.Skip("the phone of Kario Studio's end-to-end test")
	}
	bin := os.Getenv("KARIO_JSRT")
	if bin == "" {
		bin, _ = filepath.Abs(filepath.Join("..", "..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("no kario-jsrt (make jsrt): %v", err)
	}
	phone, _, _ := startFakePhone(t)
	app := phone.debugInstalled(t, bin, os.Getenv("KARIO_E2E_DEVSTORE"))
	pages, err := os.Create(out + ".pages")
	if err != nil {
		t.Fatal(err)
	}
	defer pages.Close()
	go func() {
		for p := range app.pages {
			_, _ = io.WriteString(pages, p+"\n")
		}
	}()
	env := map[string]string{}
	for _, k := range []string{"KARIO_SSH", "KARIO_DEVICE", "FAKE_SSH_ARGS", "FAKE_PHONE_BIN", devctl.RunDirEnv} {
		env[k] = os.Getenv(k)
	}
	raw, _ := json.Marshal(env)
	if err := os.WriteFile(out+".tmp", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(out+".tmp", out); err != nil {
		t.Fatal(err)
	}
	parent := os.Getppid()
	for end := time.Now().Add(15 * time.Minute); time.Now().Before(end) && os.Getppid() == parent; time.Sleep(200 * time.Millisecond) {
		if _, err := os.Stat(out + ".stop"); err == nil {
			return
		}
	}
}
