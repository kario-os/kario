package wipe

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/keystore"
)

// deviceScript is the phone's wipe (yocto), which must do what Boot does.
const deviceScript = "../yocto/meta-phone/recipes-phone/phone-wipe/files/data-wipe"

// phoneState fills dir as a phone's state dir: a keystore with its identity
// and a PIN, the Shell's files, an app's data, a photo, a hidden file.
func phoneState(t *testing.T, dir string) (id, cert string) {
	t.Helper()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ks.SetLockPIN("", "2468"); err != nil || !r.OK {
		t.Fatalf("PIN: %+v %v", r, err)
	}
	if _, err := ks.Sign("com.phone.wallet", "paycode", "", "x"); err != nil {
		t.Fatal(err)
	}
	for rel, text := range map[string]string{
		"shell/contacts.json":       `[{"name":"Li"}]`,
		"shell/accounts.json":       `{"accounts":[]}`,
		"apps/com.phone.chat/x.db":  "chat",
		"media/photos/1.jpg":        "jpeg",
		"files/notes.txt":           "notes",
		".hidden":                   "dot",
		"factory/stores/store.pub":  "store root",
		"settings.json":             "{}",
		"keystore/push/com.app.key": "push",
	} {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	att, err := ks.Attest("system.test", "n", "")
	if err != nil {
		t.Fatal(err)
	}
	return att.DeviceID, att.DeviceCert
}

// files lists the regular files under dir, relative, sorted.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// kept checks that the state dir holds the identity and nothing else, and
// that its keystore is the same device without the PIN.
func kept(t *testing.T, state, id, cert string, saltBefore []byte) {
	t.Helper()
	want := []string{"factory/root-sm2.key", "factory/root.key", "factory/root.pub", "factory/stores/store.pub",
		"keystore/data.salt", "keystore/device-sm2.key", "keystore/device.key"}
	if got := files(t, state); !slices.Equal(got, want) {
		t.Fatalf("left after the wipe:\n%q\nwant\n%q", got, want)
	}
	if st, err := os.Stat(filepath.Join(state, "factory", "root.pub")); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("root.pub: %v %v", st.Mode(), err)
	}
	if st, err := os.Stat(filepath.Join(state, "keystore", "device.key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("device.key: %v %v", st.Mode(), err)
	}
	salt, _ := os.ReadFile(keystore.DataSaltPath(state))
	if len(salt) != 32 || bytes.Equal(salt, saltBefore) {
		t.Fatalf("data salt %x, before %x", salt, saltBefore)
	}
	ks, err := keystore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	att, err := ks.Attest("system.test", "n", "")
	if err != nil {
		t.Fatal(err)
	}
	if att.DeviceID != id || att.DeviceCert != cert {
		t.Fatalf("device %s %s, was %s %s", att.DeviceID, att.DeviceCert, id, cert)
	}
	if ks.LockStatus().PIN {
		t.Fatal("the lock-screen PIN outlived the wipe")
	}
}

// A requested wipe empties the state dir but for the device's identity, with
// a new data salt; the request goes with it, and the next boot does nothing.
func TestBootWipes(t *testing.T) {
	state, persist := t.TempDir(), t.TempDir()
	id, cert := phoneState(t, state)
	salt, _ := os.ReadFile(keystore.DataSaltPath(state))
	if did, err := Boot(state, persist); did || err != nil {
		t.Fatalf("no request: %v %v", did, err)
	}
	if err := os.WriteFile(filepath.Join(persist, "antitheft.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Request(persist, FromLockScreen); err != nil {
		t.Fatal(err)
	}
	if !Requested(persist) || Why(persist) != FromLockScreen {
		t.Fatalf("requested %v, why %q", Requested(persist), Why(persist))
	}
	if did, err := Boot(state, persist); !did || err != nil {
		t.Fatalf("%v %v", did, err)
	}
	kept(t, state, id, cert, salt)
	if Requested(persist) {
		t.Fatal("the request outlived the wipe")
	}
	if got := files(t, persist); !slices.Equal(got, []string{"antitheft.json"}) {
		t.Fatalf("persistent area %q", got)
	}
	before := files(t, state)
	if did, err := Boot(state, persist); did || err != nil || !slices.Equal(files(t, state), before) {
		t.Fatalf("a second boot: %v %v", did, err)
	}
}

// A wipe cut short after its copies were made (power lost while emptying)
// starts over at the next boot from the copies: the identity is not lost.
func TestBootStartsOver(t *testing.T) {
	state, persist := t.TempDir(), t.TempDir()
	id, cert := phoneState(t, state)
	salt, _ := os.ReadFile(keystore.DataSaltPath(state))
	if err := Request(persist, FromSettings); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(persist, dirName, keepName)
	for _, rel := range Keep {
		if err := copyTree(filepath.Join(state, rel), filepath.Join(keep, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(persist, dirName, keptMark), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"keystore", "factory", "shell"} {
		if err := os.RemoveAll(filepath.Join(state, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if did, err := Boot(state, persist); !did || err != nil {
		t.Fatalf("%v %v", did, err)
	}
	kept(t, state, id, cert, salt)
}

func script(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(deviceScript)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	return p
}

// The phone's phone-wipe.service runs data-wipe empty on /var/lib: the same
// identity stays as Boot keeps, Wi-Fi networks and Bluetooth pairings go,
// the rest of /var/lib is not the phone's to empty. Without a request it
// does nothing.
func TestDeviceScriptEmpties(t *testing.T) {
	sh := script(t)
	varlib, persist := t.TempDir(), t.TempDir()
	state := filepath.Join(varlib, "phone")
	id, cert := phoneState(t, state)
	salt, _ := os.ReadFile(keystore.DataSaltPath(state))
	for rel, text := range map[string]string{"iwd/Home.psk": "[Security]", "bluetooth/AA:BB/info": "paired", "systemd/timesync/clock": ""} {
		p := filepath.Join(varlib, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func() {
		t.Helper()
		if out, err := exec.Command("sh", sh, "empty", varlib, persist).CombinedOutput(); err != nil {
			t.Fatalf("data-wipe: %v\n%s", err, out)
		}
	}
	run()
	if _, err := os.Stat(filepath.Join(state, "shell", "contacts.json")); err != nil {
		t.Fatal("data-wipe emptied without a request")
	}
	if err := Request(persist, FromSettings); err != nil {
		t.Fatal(err)
	}
	run()
	kept(t, state, id, cert, salt)
	if got := files(t, varlib); !slices.Contains(got, "systemd/timesync/clock") || slices.ContainsFunc(got, func(f string) bool {
		return strings.HasPrefix(f, "iwd/") || strings.HasPrefix(f, "bluetooth/")
	}) {
		t.Fatalf("/var/lib after the wipe: %q", got)
	}
	if Requested(persist) {
		t.Fatal("the request outlived the wipe")
	}
	if st, err := os.Stat(state); err != nil || st.Mode().Perm() != 0o711 {
		t.Fatalf("/var/lib/phone: %v %v", st.Mode(), err)
	}
}

// The initramfs makes the data partition anew with data-wipe's wipe_format;
// here mount, umount and mkfs.ext4 are stand-ins (a directory is the
// partition, a link the mount): the identity is kept across the new file
// system, and everything else the partition held is gone.
func TestDeviceScriptFormats(t *testing.T) {
	sh := script(t)
	part, mnt, persist, bin := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	state := filepath.Join(part, "phone")
	id, cert := phoneState(t, state)
	salt, _ := os.ReadFile(keystore.DataSaltPath(state))
	if err := os.WriteFile(filepath.Join(part, "dropbear-host-key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"mount":     `[ "$1" = -t ] && shift 2; rmdir "$2" && ln -s "$1" "$2"`,
		"umount":    `rm "$1" && mkdir "$1"`,
		"mkfs.ext4": `for d; do :; done; find "$d" -mindepth 1 -maxdepth 1 -exec rm -rf {} +; mkdir "$d/lost+found"; echo "$*" > "$MKFS_LOG"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Request(persist, FromLockScreen); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "mkfs")
	cmd := exec.Command("sh", "-c", `. "$1"; wipe_requested "$4" && wipe_format "$2" "$3" "$4"`, "sh", sh, part, mnt, persist)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "MKFS_LOG="+log)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wipe_format: %v\n%s", err, out)
	}
	if args, _ := os.ReadFile(log); !strings.Contains(string(args), "-L data "+part) {
		t.Fatalf("mkfs.ext4 %q", args)
	}
	if got := files(t, part); slices.Contains(got, "dropbear-host-key") {
		t.Fatalf("the partition after the wipe: %q", got)
	}
	kept(t, state, id, cert, salt)
	if Requested(persist) {
		t.Fatal("the request outlived the wipe")
	}
	if st, err := os.Lstat(mnt); err != nil || !st.IsDir() {
		t.Fatalf("the partition is still mounted: %v %v", st.Mode(), err)
	}
}
