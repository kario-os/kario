package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/programs/devcmd"
)

// The test binary is phone-dev on the fake phone: the fake ssh runs it so.
func init() {
	if os.Getenv("KARIO_TEST_PHONE_DEV") == "1" {
		devcmd.Main()
		os.Exit(0)
	}
}

// fakeSSH runs the remote command the way sshd does (the words joined, to
// sh -c) with phone-dev on the PATH and the session's connection on the
// cable; it writes its arguments, one per line, to $FAKE_SSH_ARGS.
const fakeSSH = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done >> "$FAKE_SSH_ARGS"
while [ $# -gt 0 ] && [ "${1#-}" != "$1" ]; do shift; done
shift
export SSH_CONNECTION="10.71.0.10 40000 10.71.0.1 22" KARIO_TEST_PHONE_DEV=1 PATH="$FAKE_PHONE_BIN:$PATH"
exec sh -c "$*"
`

// fakePhone is the Shell's side of the developer link: it answers as a
// phone in developer mode with a trusted computer (or, refuse set, as one
// that is not) and keeps what it was asked.
type fakePhone struct {
	mu      sync.Mutex
	refuse  string
	reqs    []devctl.Request
	pkgs    [][]byte
	trusted string
	// debug answers a debug request (dap_test.go).
	debug func(conn net.Conn, r *bufio.Reader, req devctl.Request)
}

func (f *fakePhone) serve(t *testing.T, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			r := bufio.NewReader(conn)
			req, err := devctl.ReadRequest(r)
			if err != nil {
				_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
				return
			}
			f.mu.Lock()
			f.reqs = append(f.reqs, req)
			refuse := f.refuse
			f.mu.Unlock()
			if refuse != "" {
				_ = devctl.WriteReply(conn, devctl.Reply{Error: refuse})
				return
			}
			switch req.Op {
			case devctl.OpInstall:
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true})
				raw := make([]byte, req.Size)
				if _, err := io.ReadFull(r, raw); err != nil {
					t.Error(err)
					return
				}
				f.mu.Lock()
				f.pkgs = append(f.pkgs, raw)
				f.mu.Unlock()
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: "installed com.example.my_app 1.0 (developer D, store dev)"})
			case devctl.OpTrust:
				f.mu.Lock()
				f.trusted = req.Key
				f.mu.Unlock()
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: "trusting the development store"})
			case devctl.OpLogs:
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true})
				_, _ = io.WriteString(conn, "first line\nsecond line\n")
			case devctl.OpDebug:
				if f.debug == nil {
					_ = devctl.WriteReply(conn, devctl.Reply{Error: "no debugger here"})
					return
				}
				f.debug(conn, r, req)
			case devctl.OpPush, devctl.OpUpdate:
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true})
				raw := make([]byte, req.Size)
				if _, err := io.ReadFull(r, raw); err != nil {
					t.Error(err)
					return
				}
				f.mu.Lock()
				f.pkgs = append(f.pkgs, raw)
				f.mu.Unlock()
				if req.Op == devctl.OpUpdate {
					_ = devctl.WriteReply(conn, devctl.Reply{OK: true, More: true, Text: " 50% installing"})
				}
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: fmt.Sprintf("%s %s %d %o", req.Op, req.Path, req.Size, req.Mode)})
			case devctl.OpPull, devctl.OpScreenshot, devctl.OpBugreport:
				body := map[string]string{devctl.OpPull: "file " + req.Path, devctl.OpScreenshot: "\x89PNG picture", devctl.OpBugreport: "PK zip"}[req.Op]
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Size: int64(len(body)), Note: "said beside " + req.Op})
				_, _ = io.WriteString(conn, body)
			case devctl.OpForward:
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: "forwarding"})
				_, _ = io.WriteString(conn, "phone-dev: developer mode is off\n")
			default:
				_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: req.Op + " " + req.App})
			}
		}()
	}
}

func (f *fakePhone) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Op)
	}
	return out
}

// startFakePhone serves the fake Shell's socket and points kario at it
// through the fake ssh ($KARIO_SSH): the phone, a scratch directory, and
// the file the ssh arguments go to.
func startFakePhone(t *testing.T) (*fakePhone, string, string) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	_ = os.MkdirAll(bin, 0o755)
	if err := os.Symlink(self, filepath.Join(bin, devctl.Program)); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(tmp, "ssh")
	if err := os.WriteFile(ssh, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := os.MkdirTemp("", "kario-device-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	ln, err := net.Listen("unix", filepath.Join(run, devctl.Sock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	phone := &fakePhone{}
	go phone.serve(t, ln)
	args := filepath.Join(tmp, "ssh-args")
	t.Setenv("FAKE_SSH_ARGS", args)
	t.Setenv("FAKE_PHONE_BIN", bin)
	t.Setenv(devctl.RunDirEnv, run)
	t.Setenv("KARIO_SSH", ssh)
	t.Setenv("KARIO_DEVICE", "")
	return phone, tmp, args
}

// kario device runs phone-dev over ssh, which reaches the (fake) Shell:
// a project is signed with a development store whose root goes first,
// logs come back as the app wrote them, the phone's refusal is the error.
func TestDevice(t *testing.T) {
	phone, tmp, args := startFakePhone(t)
	kario := func(a ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := deviceCommand(a, strings.NewReader(""), &out, &errOut)
		return out.String() + errOut.String(), err
	}

	dir := newTestProject(t)
	store := filepath.Join(tmp, "devstore")
	out, err := kario("install", "-devstore", store, "-open", dir)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if got := strings.Join(phone.ops(), " "); got != "trust install restart" {
		t.Fatalf("the phone was asked: %s", got)
	}
	key, _ := os.ReadFile(filepath.Join(store, "trust", "dev.pub"))
	if phone.trusted != strings.TrimSpace(string(key)) {
		t.Fatalf("trusted %q, the store's root is %q", phone.trusted, key)
	}
	p, err := pkg.Open(phone.pkgs[0], pkg.LoadRoots(filepath.Join(store, "trust")))
	if err != nil || !strings.Contains(string(p.Manifest), "com.example.my_app") {
		t.Fatalf("the package the phone got: %v", err)
	}
	if !strings.Contains(out, "installed com.example.my_app 1.0") || !strings.Contains(out, "restart com.example.my_app") {
		t.Fatalf("output:\n%s", out)
	}
	sshArgs, _ := os.ReadFile(args)
	if !strings.Contains(string(sshArgs), "-T\nroot@"+devctl.USBAddr+"\n"+devctl.Program+"\n") {
		t.Fatalf("ssh ran as:\n%s", sshArgs)
	}

	pkgFile := filepath.Join(tmp, "app.phoneapp")
	_ = os.WriteFile(pkgFile, phone.pkgs[0], 0o644)
	if out, err := kario("-to", "dev@phone", "install", pkgFile); err != nil || len(phone.pkgs) != 2 || !bytes.Equal(phone.pkgs[1], phone.pkgs[0]) {
		t.Fatalf("install a package file: %v\n%s", err, out)
	}
	if ops := phone.ops(); ops[len(ops)-1] != devctl.OpInstall || ops[len(ops)-2] != devctl.OpRestart {
		t.Fatalf("a package file without -devstore sent no root first: %v", ops)
	}
	if sshArgs, _ = os.ReadFile(args); !strings.Contains(string(sshArgs), "dev@phone") {
		t.Fatal("-to was not the ssh target")
	}

	if out, err := kario("logs", "-f", "com.example.my_app"); err != nil || out != "first line\nsecond line\n" {
		t.Fatalf("logs: %v %q", err, out)
	}
	if last := phone.reqs[len(phone.reqs)-1]; !last.Follow || last.App != "com.example.my_app" || devctl.ViaAddr(last.Via) != devctl.USBAddr {
		t.Fatalf("logs asked %+v", last)
	}
	for _, op := range []string{"reload", "restart", "uninstall"} {
		if out, err := kario(op, "com.example.my_app"); err != nil || !strings.Contains(out, op+" com.example.my_app") {
			t.Fatalf("%s: %v %q", op, err, out)
		}
	}
	if out, err := kario("shell", "echo", "on the phone"); err != nil || out != "on the phone\n" {
		t.Fatalf("shell: %v %q", err, out)
	}

	phone.mu.Lock()
	phone.refuse = "developer mode is off on the phone (Settings › Developer mode)"
	phone.mu.Unlock()
	n := len(phone.ops())
	if _, err := kario("install", "-devstore", store, dir); err == nil || !strings.Contains(err.Error(), "developer mode is off") {
		t.Fatalf("a refusal: %v", err)
	}
	if ops := phone.ops()[n:]; strings.Join(ops, " ") != "trust" {
		t.Fatalf("went on after the phone refused: %v", ops)
	}
	if _, err := kario("reload", "com.example.my_app; reboot"); err == nil || !strings.Contains(err.Error(), "not an app id") {
		t.Fatalf("a command line in an app id: %v", err)
	}
	if _, err := kario("bogus", "x"); err == nil {
		t.Fatal("an unknown command")
	}
}

// The tools beside apps reach the phone with every argument as it was
// typed, whatever the remote shell makes of the command line; files come
// back whole into the file named, with what the phone says beside them on
// standard error.
func TestDeviceTools(t *testing.T) {
	phone, tmp, args := startFakePhone(t)
	kario := func(a ...string) (string, string, error) {
		var out, errOut bytes.Buffer
		err := deviceCommand(a, strings.NewReader(""), &out, &errOut)
		return out.String(), errOut.String(), err
	}
	last := func() devctl.Request {
		phone.mu.Lock()
		defer phone.mu.Unlock()
		return phone.reqs[len(phone.reqs)-1]
	}

	local := filepath.Join(tmp, "a file.sh")
	_ = os.WriteFile(local, []byte("#!/bin/sh\necho hi\n"), 0o755)
	if out, _, err := kario("push", local, "com.example.my_app:files/it's here.sh"); err != nil || out != "push com.example.my_app:files/it's here.sh 18 755\n" {
		t.Fatalf("push: %v %q", err, out)
	}
	if string(phone.pkgs[len(phone.pkgs)-1]) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("pushed %q", phone.pkgs[len(phone.pkgs)-1])
	}
	if _, _, err := kario("push", tmp, "x"); err == nil {
		t.Fatal("pushed a directory")
	}

	if out, _, err := kario("pull", "com.example.my_app:/files/notes.db", tmp); err != nil || !strings.Contains(out, filepath.Join(tmp, "notes.db")) {
		t.Fatalf("pull into a directory: %v %q", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(tmp, "notes.db")); string(got) != "file com.example.my_app:/files/notes.db" {
		t.Fatalf("pulled %q", got)
	}
	shot := filepath.Join(tmp, "shot.png")
	if out, errOut, err := kario("screenshot", "-o", shot); err != nil || !strings.Contains(out, shot+" (12 bytes)") || !strings.Contains(errOut, "said beside screenshot") {
		t.Fatalf("screenshot: %v %q %q", err, out, errOut)
	}
	if got, _ := os.ReadFile(shot); string(got) != "\x89PNG picture" {
		t.Fatalf("screenshot file %q", got)
	}
	wd, _ := os.Getwd()
	_ = os.Chdir(tmp)
	_, _, err := kario("bugreport")
	_ = os.Chdir(wd)
	if reports, _ := filepath.Glob(filepath.Join(tmp, "bugreport-*.zip")); err != nil || len(reports) != 1 {
		t.Fatalf("bugreport: %v %v", err, reports)
	}

	if _, _, err := kario("key", "down", "shift+right", "enter"); err != nil || !slices.Equal(last().Keys, []string{"down", "shift+right", "enter"}) {
		t.Fatalf("key: %v %+v", err, last())
	}
	if _, _, err := kario("type", "it's", "$HOME; reboot"); err != nil || last().Text != "it's $HOME; reboot" {
		t.Fatalf("type: %v %q", err, last().Text)
	}
	if _, _, err := kario("type", "two\nlines"); err == nil {
		t.Fatal("a control character went into the command line")
	}
	if _, _, err := kario("syslog", "-f", "-u", "phone-pushd.service", "-n", "20"); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.Op != devctl.OpSyslog || !r.Follow || r.Unit != "phone-pushd.service" || r.Lines != 20 {
		t.Fatalf("syslog asked %+v", r)
	}
	if _, _, err := kario("reboot", "bootloader"); err != nil || last().Target != devctl.RebootBootloader {
		t.Fatalf("reboot: %v %+v", err, last())
	}

	bundle := filepath.Join(tmp, "update.raucb")
	_ = os.WriteFile(bundle, []byte("a signed bundle"), 0o644)
	if out, _, err := kario("update", bundle); err != nil || out != " 50% installing\nupdate  15 0\n" || string(phone.pkgs[len(phone.pkgs)-1]) != "a signed bundle" {
		t.Fatalf("update: %v %q", err, out)
	}

	if out, errOut, err := kario("forward", "8080", "80"); err != nil || !strings.Contains(out, "forwarding\nphone-dev: developer mode is off") || !strings.Contains(errOut, "localhost:8080 reaches the phone's port 80") {
		t.Fatalf("forward: %v %q %q", err, out, errOut)
	}
	if r := last(); r.Op != devctl.OpForward || r.Port != 80 || r.Reverse {
		t.Fatalf("forward asked %+v", r)
	}
	sshArgs, _ := os.ReadFile(args)
	if !strings.Contains(string(sshArgs), "-oExitOnForwardFailure=yes\n-L8080:127.0.0.1:80\n-T\n") {
		t.Fatalf("ssh ran as:\n%s", sshArgs)
	}
	if _, _, err := kario("reverse", "9000", "3000"); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.Port != 9000 || !r.Reverse {
		t.Fatalf("reverse asked %+v", r)
	}
	if sshArgs, _ = os.ReadFile(args); !strings.Contains(string(sshArgs), "-R127.0.0.1:9000:127.0.0.1:3000\n") {
		t.Fatalf("ssh ran as:\n%s", sshArgs)
	}
	if _, _, err := kario("reverse", "80", "3000"); err == nil {
		t.Fatal("a reverse forward onto a system port")
	}
	if _, _, err := kario("forward", "8080", "http"); err == nil {
		t.Fatal("a port that is not one")
	}

	phone.mu.Lock()
	phone.refuse = "only the developer directory (/var/lib/phone/dev) and the data of apps installed in developer mode (APP-ID:PATH) are the computer's to read and write"
	phone.mu.Unlock()
	if _, _, err := kario("pull", "/etc/shadow", filepath.Join(tmp, "shadow")); err == nil || !strings.Contains(err.Error(), "the phone: only the developer directory") {
		t.Fatalf("a refused pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "shadow")); !os.IsNotExist(err) {
		t.Fatal("a refused pull left a file")
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, ".kario-device-*")); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestRemoteWord(t *testing.T) {
	for in, want := range map[string]string{
		"com.example.app":  "com.example.app",
		"-f":               "-f",
		"a b":              "'a b'",
		"it's":             `'it'\''s'`,
		"$HOME":            "'$HOME'",
		"":                 "''",
		"files/x.db":       "files/x.db",
		"key+/=AAAA":       "key+/=AAAA",
		"com.a.b:files/x":  "com.a.b:files/x",
		"a;reboot":         "'a;reboot'",
		"`id`":             "'`id`'",
		"name with \"q\"":  `'name with "q"'`,
		"100%":             "100%",
		"user@host":        "user@host",
		"a,b":              "a,b",
		"tab\there":        "'tab\there'",
		"\u4e2d\u6587.txt": "'\u4e2d\u6587.txt'",
	} {
		if got := remoteWord(in); got != want {
			t.Errorf("remoteWord(%q) = %q, want %q", in, got, want)
		}
	}
}
