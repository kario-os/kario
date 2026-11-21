package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/ota/delta"
	"github.com/kario-os/kario/ota/verity"
)

// The hook runs this test binary as phone-ota-delta.
func TestMain(m *testing.M) {
	if os.Getenv("PHONE_OTA_DELTA_AS_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const (
	hook       = "../../yocto/meta-phone/dynamic-layers/rauc/recipes-phone/phone-bundle/files/delta-hook"
	verityHook = "../../yocto/meta-phone/dynamic-layers/rauc/recipes-phone/phone-bundle/files/verity-hook"
	slotChoose = "../../yocto/meta-phone/recipes-phone/phone-ota/files/slot-choose"
)

// busybox is a PATH of only the commands the hooks may use: the phone's
// busybox has them with the options they use (its od, for one, has no -A,
// -t, -j, -N).
func busybox(t *testing.T) string {
	bin := t.TempDir()
	for _, tool := range []string{"sed", "mv", "sync", "tee", "tail", "rm", "cp", "grep"} {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(bin, tool))
		}
	}
	return bin
}

// A full bundle's hook: before RAUC writes the slot, an incremental
// install's checkpoint for it goes, and so does its old descriptor (an
// install that stops leaves a slot nothing opens); after, its descriptor
// comes in.
func TestVerityHook(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	slots, bundle := t.TempDir(), t.TempDir()
	progress := filepath.Join(slots, "rootfs_b.progress")
	_ = os.WriteFile(progress, []byte("a checkpoint"), 0o600)
	_ = os.WriteFile(filepath.Join(slots, "rootfs_a.progress"), []byte("the other slot's"), 0o600)
	for _, f := range []string{"rootfs_a.verity", "rootfs_a.verity.sig", "rootfs_b.verity", "rootfs_b.verity.sig"} {
		_ = os.WriteFile(filepath.Join(slots, f), []byte("old "+f), 0o644)
	}
	_ = os.WriteFile(filepath.Join(bundle, "phone-image.ext4.verity.desc"), []byte("desc"), 0o644)
	_ = os.WriteFile(filepath.Join(bundle, "phone-image.ext4.verity.desc.sig"), []byte("sig"), 0o644)
	env := append(os.Environ(), "PATH="+busybox(t), "RAUC_SLOT_CLASS=rootfs", "RAUC_SLOT_BOOTNAME=b", "RAUC_BUNDLE_MOUNT_POINT="+bundle, "PHONE_SLOTS="+slots)
	for _, step := range []string{"slot-pre-install", "slot-post-install"} {
		cmd := exec.Command("sh", verityHook, step)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", step, err, out)
		}
		if step != "slot-pre-install" {
			continue
		}
		for _, f := range []string{"rootfs_b.verity", "rootfs_b.verity.sig"} {
			if _, err := os.Stat(filepath.Join(slots, f)); !os.IsNotExist(err) {
				t.Fatalf("%s stayed while the slot is written", f)
			}
		}
		if b, _ := os.ReadFile(filepath.Join(slots, "rootfs_a.verity")); string(b) != "old rootfs_a.verity" {
			t.Fatal("the running slot's descriptor changed")
		}
	}
	if _, err := os.Stat(progress); !os.IsNotExist(err) {
		t.Fatal("the checkpoint of the slot written stayed")
	}
	if _, err := os.Stat(filepath.Join(slots, "rootfs_a.progress")); err != nil {
		t.Fatal("the other slot's checkpoint went")
	}
	if b, _ := os.ReadFile(filepath.Join(slots, "rootfs_b.verity")); string(b) != "desc" {
		t.Fatalf("descriptor %q", b)
	}
}

// slot makes a slot image (ext4 and its hash tree) of one file, and its
// descriptor.
func slot(t *testing.T, content []byte) ([]byte, verity.Descriptor) {
	dir, img := t.TempDir(), filepath.Join(t.TempDir(), "slot.img")
	if err := os.MkdirAll(filepath.Join(dir, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "usr/bin/program"), content, 0o755)
	if b, err := exec.Command("mkfs.ext4", "-q", "-F", "-b", "4096", "-d", dir, img, "8M").CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, b)
	}
	data, _ := os.ReadFile(img)
	salt := sha256.Sum256(content)
	d := verity.Descriptor{DataBlocks: uint64(len(data)) / verity.BlockSize, HashOffset: uint64(len(data)), Salt: salt[:]}
	h := verity.NewHasher(d.Salt, d.DataBlocks)
	_, _ = h.Write(data)
	tree, root, _ := h.Tree()
	d.RootHash = root
	return append(data, tree...), d
}

// The RAUC install hook of a delta bundle, run as RAUC runs it, against
// files standing in for the slots and the state partition.
func TestDeltaHook(t *testing.T) {
	for _, tool := range []string{"mkfs.ext4", "xz", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	r := rand.New(rand.NewSource(1))
	old := make([]byte, 1<<20)
	r.Read(old)
	new := append(append([]byte{}, old[:5000]...), append([]byte("relinked"), old[5000:]...)...)
	srcImg, srcDesc := slot(t, old)
	dstImg, dstDesc := slot(t, new)
	var payload bytes.Buffer
	if _, err := delta.Generate(delta.GenerateConfig{
		Source: &delta.Slot{Data: srcImg[:srcDesc.HashOffset], Desc: &srcDesc},
		Target: &delta.Slot{Data: dstImg[:dstDesc.HashOffset], Desc: &dstDesc}}, &payload); err != nil {
		t.Fatal(err)
	}

	self, _ := os.Executable()
	// used logs which applier ran; own is the system's (PHONE_OTA_DELTA,
	// none by default); noApplier leaves the bundle's out.
	used := filepath.Join(t.TempDir(), "used")
	own, noApplier := filepath.Join(t.TempDir(), "none"), false
	applier := func(name, formats string) string {
		p := filepath.Join(t.TempDir(), name)
		// No formats: an applier from before the query, which knows no
		// such command. The test's payloads are format 1. Without "+" in
		// formats, one from before checkpoints: it knows no can-resume, and
		// refuses --checkpoint as its flags did.
		answer := "if [ \"$1\" = can-apply ]; then echo usage >&2; exit 2; fi\n"
		if formats != "" {
			answer = "if [ \"$1\" = can-apply ]; then printf '" + strings.TrimSuffix(formats, "+") + "' | grep -qx 1 && { echo 1; exit 0; }; exit 3; fi\n"
		}
		if strings.HasSuffix(formats, "+") {
			answer += "[ \"$1\" = can-resume ] && PHONE_OTA_DELTA_AS_MAIN=1 exec " + self + " can-resume\n" +
				"[ \"$1\" = apply ] && set -- \"$@\" --checkpoint-bytes 4096\n"
		} else {
			answer += "if [ \"$1\" = can-resume ]; then echo usage >&2; exit 2; fi\n" +
				"for a in \"$@\"; do [ \"$a\" = --checkpoint ] && { echo 'flag provided but not defined: -checkpoint' >&2; exit 2; }; done\n"
		}
		_ = os.WriteFile(p, []byte("#!/bin/sh\n"+answer+"echo "+name+" >> "+used+"\nPHONE_OTA_DELTA_AS_MAIN=1 exec "+self+" \"$@\"\n"), 0o755)
		return p
	}
	ran := func() string {
		b, _ := os.ReadFile(used)
		_ = os.Remove(used)
		return string(bytes.TrimSpace(b))
	}
	bundleApplier := applier("bundle", "+")
	bin := busybox(t)
	// bundlePayload is the payload the bundle carries; dir, if set, the
	// phone the hook runs on again (the slots and the state as left);
	// stopped: the shell has asked the install to stop (its stop file).
	bundlePayload, again, stopped := payload.Bytes(), "", false
	run := func(running []byte, runningDesc verity.Descriptor, booted, target string) (string, string, error) {
		dir := again
		if dir == "" {
			dir = t.TempDir()
		}
		slots, dev, bundle := filepath.Join(dir, "slots"), filepath.Join(dir, "dev"), filepath.Join(dir, "bundle")
		for _, d := range []string{slots, dev, bundle} {
			_ = os.MkdirAll(d, 0o755)
		}
		part := filepath.Join(dir, "rootfs_"+target)
		if again == "" {
			_ = os.WriteFile(filepath.Join(slots, "boot"), []byte(fmt.Sprintf("slot=%s\ngood=%s\ntries=0\nbooted=%s\n", booted, booted, booted)), 0o644)
			_ = os.WriteFile(filepath.Join(slots, "rootfs_"+booted+".verity"), runningDesc.Marshal(), 0o644)
			_ = os.WriteFile(filepath.Join(slots, "rootfs_b.verity"), []byte("the slot's old descriptor"), 0o644)
			_ = os.WriteFile(part, append(append([]byte{}, srcImg...), make([]byte, 1<<20)...), 0o644)
		}
		_ = os.WriteFile(filepath.Join(dev, "root_"+booted), running, 0o644)
		_ = os.WriteFile(filepath.Join(bundle, "rootfs.kdelta"), bundlePayload, 0o644)
		_ = os.WriteFile(filepath.Join(bundle, "phone-image.ext4.verity.desc"), dstDesc.Marshal(), 0o644)
		_ = os.WriteFile(filepath.Join(bundle, "phone-image.ext4.verity.desc.sig"), []byte("signature"), 0o644)
		if !noApplier {
			script, _ := os.ReadFile(bundleApplier)
			_ = os.WriteFile(filepath.Join(bundle, "phone-ota-delta"), script, 0o755)
		}
		stop := filepath.Join(dir, "ota-delta.stop")
		_ = os.Remove(stop)
		if stopped {
			_ = os.WriteFile(stop, nil, 0o644)
		}
		cmd := exec.Command("sh", hook, "slot-install")
		cmd.Env = append(os.Environ(), "PATH="+bin, "RAUC_SLOT_CLASS=rootfs", "RAUC_SLOT_BOOTNAME="+target, "RAUC_SLOT_DEVICE="+part,
			"RAUC_IMAGE_NAME="+filepath.Join(bundle, "rootfs.kdelta"), "RAUC_BUNDLE_MOUNT_POINT="+bundle,
			"PHONE_SLOTS="+slots, "PHONE_SLOT_CHOOSE="+slotChoose, "PHONE_SLOT_DEVICES="+dev, "PHONE_OTA_DELTA="+own,
			"PHONE_OTA_DELTA_LOG="+filepath.Join(dir, "ota-delta.log"), "PHONE_OTA_DELTA_STOP="+stop)
		out, err := cmd.CombinedOutput()
		return dir, string(out), err
	}

	installed := func(dir string) {
		t.Helper()
		got, _ := os.ReadFile(filepath.Join(dir, "rootfs_b"))
		if !bytes.Equal(got[:len(dstImg)], dstImg) {
			t.Fatal("slot b is not the target slot image")
		}
		desc, _ := os.ReadFile(filepath.Join(dir, "slots", "rootfs_b.verity"))
		sig, _ := os.ReadFile(filepath.Join(dir, "slots", "rootfs_b.verity.sig"))
		if !bytes.Equal(desc, dstDesc.Marshal()) || string(sig) != "signature" {
			t.Fatalf("the boot state has descriptor %q", desc)
		}
		if log, _ := os.ReadFile(filepath.Join(dir, "ota-delta.log")); !bytes.Contains(log, []byte("applying payload format")) || !bytes.Contains(log, []byte(", peak memory ")) {
			t.Fatalf("the hook's log: %q", log)
		}
	}
	dir, out, err := run(srcImg[:srcDesc.HashOffset], srcDesc, "a", "b")
	if err != nil {
		t.Fatalf("the hook failed: %v\n%s", err, out)
	}
	installed(dir)
	if got := ran(); got != "bundle" {
		t.Fatalf("a system without an applier ran %q", got)
	}

	// The system's applier when it applies the payload's format, the
	// bundle's when it applies others only; without the bundle's, the
	// system's must do.
	for _, c := range []struct {
		name, formats string
		noApplier     bool
		want          string
	}{
		{"own", "1\n+", false, "own"},
		{"before-checkpoints", "1\n", false, "before-checkpoints"},
		{"multi", "1\n2\n+", true, "multi"},
		{"older", "7\n", false, "bundle"},
		{"silent", "", false, "bundle"},
		{"older", "7\n", true, ""},
	} {
		own, noApplier = applier(c.name, c.formats), c.noApplier
		dir, out, err := run(srcImg[:srcDesc.HashOffset], srcDesc, "a", "b")
		got := ran()
		switch {
		case c.want == "" && (err == nil || !strings.Contains(out, "does not apply this payload's format")):
			t.Fatalf("%s formats %q without the bundle's applier: %v\n%s", c.name, c.formats, err, out)
		case c.want == "":
			if b, _ := os.ReadFile(filepath.Join(dir, "slots", "rootfs_b.verity")); string(b) != "the slot's old descriptor" {
				t.Fatal("the boot state changed although no applier could run")
			}
		case err != nil:
			t.Fatalf("%s formats %q: %v\n%s", c.name, c.formats, err, out)
		case got != c.want:
			t.Fatalf("%s formats %q: ran %q, want %q\n%s", c.name, c.formats, got, c.want, out)
		default:
			installed(dir)
		}
	}
	// This program's own answer to formats is what the hook reads.
	real := filepath.Join(t.TempDir(), "real")
	_ = os.WriteFile(real, []byte("#!/bin/sh\ncase \"$1\" in can-*) ;; *) echo real >> "+used+" ;; esac\nPHONE_OTA_DELTA_AS_MAIN=1 exec "+self+" \"$@\"\n"), 0o755)
	own, noApplier = real, true
	if dir, out, err := run(srcImg[:srcDesc.HashOffset], srcDesc, "a", "b"); err != nil || ran() != "real" {
		t.Fatalf("with this program as the system's applier: %v\n%s", err, out)
	} else {
		installed(dir)
	}
	own, noApplier = filepath.Join(t.TempDir(), "none"), false

	// Running another version: refused before anything is written.
	dir, out, err = run(dstImg[:dstDesc.HashOffset], dstDesc, "a", "b")
	if err == nil || !bytes.Contains([]byte(out), []byte("another system version")) {
		t.Fatalf("applied on another version: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "rootfs_b")); !bytes.Equal(got[:len(srcImg)], srcImg) {
		t.Fatal("slot b was written for another version")
	}
	// Data that is not what the running slot's descriptor says (dm-verity
	// would not let it be read): the operations' source hashes stop it,
	// and slot b has no descriptor left that the half-written slot could
	// be opened with.
	dir, out, err = run(dstImg[:dstDesc.HashOffset], srcDesc, "a", "b")
	if err == nil {
		t.Fatalf("applied from other data:\n%s", out)
	}
	if _, serr := os.Stat(filepath.Join(dir, "slots", "rootfs_b.verity")); !os.IsNotExist(serr) {
		t.Fatal("slot b kept a descriptor after a failed install")
	}
	if boot, _ := os.ReadFile(filepath.Join(dir, "slots", "boot")); string(boot) != "slot=a\ngood=a\ntries=0\nbooted=a\n" {
		t.Fatalf("the boot state changed: %q", boot)
	}
	t.Logf("refused: %s", bytes.TrimSpace([]byte(out)))

	// RAUC never targets the running slot; the hook refuses it anyway.
	if _, out, err := run(srcImg[:srcDesc.HashOffset], srcDesc, "b", "b"); err == nil {
		t.Fatalf("wrote the running slot:\n%s", out)
	}

	// The payload stops arriving in its last operation's data (the network
	// gone): the hook fails and the checkpoint stays in the state partition;
	// the same bundle installed again on that phone resumes, and the
	// checkpoint goes.
	full := payload.Bytes()
	m, err := delta.ReadManifest(bytes.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	last := m.Ops[len(m.Ops)-1]
	if last.DataLength == 0 {
		t.Fatalf("the last operation (%s) carries no data", last.Type)
	}
	bundlePayload = full[:len(full)-int(last.DataLength)/2]
	dir, out, err = run(srcImg[:srcDesc.HashOffset], srcDesc, "a", "b")
	progress := filepath.Join(dir, "slots", "rootfs_b.progress")
	if err == nil {
		t.Fatalf("applied a payload that stopped arriving:\n%s", out)
	}
	if _, serr := os.Stat(progress); serr != nil {
		t.Fatalf("no checkpoint kept: %v\n%s", serr, out)
	}
	if log, _ := os.ReadFile(filepath.Join(dir, "ota-delta.log")); !delta.ParseLog(log).Stalled {
		t.Fatalf("the hook's log does not say the payload stopped arriving:\n%s", log)
	}
	again, bundlePayload = dir, full
	dir, out, err = run(srcImg[:srcDesc.HashOffset], srcDesc, "a", "b")
	again = ""
	if err != nil {
		t.Fatalf("installing again: %v\n%s", err, out)
	}
	installed(dir)
	log, _ := os.ReadFile(filepath.Join(dir, "ota-delta.log"))
	if p := delta.ParseLog(log); p.Resumed == 0 || !strings.Contains(out, "resumed at operation") {
		t.Fatalf("did not resume (%+v):\n%s", p, out)
	}
	if _, serr := os.Stat(progress); !os.IsNotExist(serr) {
		t.Fatal("the checkpoint outlived the install")
	}
	t.Logf("resumed: %s", bytes.TrimSpace(bytes.Split(log, []byte("\n"))[2]))

	// The shell stops an automatic install (its stop file, --stop): the
	// applier stops before it writes on, the hook fails, the slot has no
	// descriptor and the boot state is as it was; installed again without
	// the stop file, it goes on.
	stopped = true
	dir, out, err = run(srcImg[:srcDesc.HashOffset], srcDesc, "a", "b")
	stopped = false
	if err == nil {
		t.Fatalf("an install asked to stop went on:\n%s", out)
	}
	if log, _ := os.ReadFile(filepath.Join(dir, "ota-delta.log")); !delta.ParseLog(log).Stopped {
		t.Fatalf("the hook's log does not say the install stopped:\n%s", log)
	}
	if _, serr := os.Stat(filepath.Join(dir, "slots", "rootfs_b.verity")); !os.IsNotExist(serr) {
		t.Fatal("slot b kept a descriptor after the stop")
	}
	if boot, _ := os.ReadFile(filepath.Join(dir, "slots", "boot")); string(boot) != "slot=a\ngood=a\ntries=0\nbooted=a\n" {
		t.Fatalf("the boot state changed: %q", boot)
	}
	again = dir
	dir, out, err = run(srcImg[:srcDesc.HashOffset], srcDesc, "a", "b")
	again = ""
	if err != nil {
		t.Fatalf("installing again after the stop: %v\n%s", err, out)
	}
	installed(dir)
}

// A resumed install seeks over the payload data it skips: within what is
// buffered, or on the file past it.
func TestSeekBuffer(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := &seekBuffer{f: f, r: bufio.NewReaderSize(f, 64)}
	at := 0
	read := func(n int) {
		t.Helper()
		got := make([]byte, n)
		if _, err := io.ReadFull(s, got); err != nil || !bytes.Equal(got, data[at:at+n]) {
			t.Fatalf("at %d: %v", at, err)
		}
		at += n
	}
	seek := func(off int64) {
		t.Helper()
		pos, err := s.Seek(off, io.SeekCurrent)
		at += int(off)
		if err != nil || pos != int64(at) {
			t.Fatalf("seek %d: %d %v, want %d", off, pos, err, at)
		}
	}
	read(10)
	seek(20)
	read(5)
	seek(int64(s.r.Buffered()))
	read(3)
	seek(500)
	read(100)
	seek(0)
	read(1)
	if _, err := s.Seek(-1, io.SeekCurrent); err == nil {
		t.Fatal("sought backwards")
	}
	if _, err := s.Seek(1, io.SeekStart); err == nil {
		t.Fatal("sought from the start")
	}
}
