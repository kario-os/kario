package shell

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestConnName(t *testing.T) {
	name := connName("com.phone.calc-pro", 4242)
	id, pid, ok := parseConnName(name)
	if !ok || id != "com.phone.calc-pro" || pid != 4242 {
		t.Fatalf("%s -> %q %d %v", name, id, pid, ok)
	}
	for _, bad := range []string{"", "app-", "app-x", "app-x-y", "renderer", "app--12x"} {
		if _, _, ok := parseConnName(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSdNotifyStoresDescriptor(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "notify")
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("NOTIFY_SOCKET", sock)
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	mine, peer := os.NewFile(uintptr(fds[0]), "mine"), os.NewFile(uintptr(fds[1]), "peer")
	defer peer.Close()
	if err := sdNotify("FDSTORE=1\nFDNAME="+connName("com.phone.calc", 7), mine); err != nil {
		t.Fatal(err)
	}
	mine.Close()
	buf, oob := make([]byte, 256), make([]byte, 256)
	n, oobn, _, _, err := ln.ReadMsgUnix(buf, oob)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "FDSTORE=1\nFDNAME=app-com.phone.calc-7" {
		t.Fatalf("message %q", got)
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		t.Fatalf("control messages %v %v", msgs, err)
	}
	got, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil || len(got) != 1 {
		t.Fatalf("rights %v %v", got, err)
	}
	stored := os.NewFile(uintptr(got[0]), "stored")
	defer stored.Close()
	if _, err := peer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := stored.Read(b); err != nil || b[0] != 'x' {
		t.Fatalf("stored descriptor is not the connection: %v %q", err, b)
	}
}

func TestInheritedFromHandoff(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fds[1])
	t.Setenv(handoffEnv, strconv.Itoa(fds[0])+"="+connName("com.phone.clock", 99)+",1=junk")
	got := InheritedApps()
	if len(got) != 1 || got[0].ID != "com.phone.clock" || got[0].Pid != 99 {
		t.Fatalf("inherited %+v", got)
	}
	got[0].File.Close()
	if os.Getenv(handoffEnv) != "" {
		t.Fatal("PHONE_HANDOFF left for the apps to see")
	}
}
