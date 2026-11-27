package shellcmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
)

// fakeServices writes stand-ins for the services into a bin directory:
// each script gets the run directory as its second argument, as the real
// ones do (--run-dir DIR).
func fakeServices(t *testing.T, scripts map[string]string) (bin, run, state string) {
	t.Helper()
	root := t.TempDir()
	bin, run, state = filepath.Join(root, "bin"), filepath.Join(root, "run"), filepath.Join(root, "state")
	for _, d := range []string{bin, run, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	serve := func(socks ...string) string {
		var touch []string
		for _, s := range socks {
			touch = append(touch, `"$2/`+s+`"`)
		}
		return "touch " + strings.Join(touch, " ") + "\nexec sleep 60\n"
	}
	all := map[string]string{
		"phone-mediad":    serve(svc.MediadSock, svc.MediadFramesSock),
		"phone-keystored": serve(svc.KeystoredSock),
	}
	for name, body := range scripts {
		all[name] = body
	}
	for name, body := range all {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin, run, state
}

func runs(t *testing.T, run, name string) int {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(run, name+".runs"))
	return strings.Count(string(raw), "\n")
}

// A service that exits with status 0 before its socket appears has nothing
// to serve on this board (phone-nfcd without an NFC controller): it is not
// started again and the Shell does not wait for it.
func TestADeclinedServiceStaysDown(t *testing.T) {
	bin, run, state := fakeServices(t, map[string]string{
		"phone-nfcd": "echo run >> \"$2/phone-nfcd.runs\"\necho 'this board has no NFC controller' >&2\nexit 0\n",
	})
	t0 := time.Now()
	s, err := startServices(bin, run, state, "", []string{"--sink", "auto"}, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("the services took %v: waited for the declined one's socket", d)
	}
	time.Sleep(2500 * time.Millisecond)
	if n := runs(t, run, "phone-nfcd"); n != 1 || !s.hasDeclined("phone-nfcd") {
		t.Errorf("phone-nfcd ran %d times, declined %v", n, s.hasDeclined("phone-nfcd"))
	}
}

// A service slow to start under emulation (several emulators on a busy
// machine) is waited for by the run's timeout scale: phone-mediad's socket
// 3.5 seconds after its start failed QEMU's whole run at once (2026-11-25,
// two emulated processors).
func TestServiceSocketWaitScales(t *testing.T) {
	slow := "sleep 3.5\ntouch \"$2/" + svc.MediadSock + "\" \"$2/" + svc.MediadFramesSock + "\"\nexec sleep 60\n"
	bin, run, state := fakeServices(t, map[string]string{"phone-mediad": slow})
	if s, err := startServices(bin, run, state, "", []string{"--sink", "auto"}, false, 1); err == nil {
		s.stop()
		t.Fatal("a socket 3.5 s late was waited for at scale 1")
	}
	bin, run, state = fakeServices(t, map[string]string{"phone-mediad": slow})
	s, err := startServices(bin, run, state, "", []string{"--sink", "auto"}, false, 4)
	if err != nil {
		t.Fatalf("at scale 4: %v", err)
	}
	s.stop()
}

// The Shell's own audiod plays and sets the card up as it is told: the
// board's PCM and UCM set-up on a phone whose SKU names them (privateAudio).
func TestPrivateAudiodSink(t *testing.T) {
	bin, run, state := fakeServices(t, map[string]string{
		"phone-audiod": "echo \"$*\" > \"$2/phone-audiod.args\"\ntouch \"$2/" + svc.AudiodSock + "\"\nexec sleep 60\n",
	})
	s, err := startServices(bin, run, state, "", []string{"--sink", "alsa:plughw:0,2", "--ucm-card", "hw:0", "--ucm-verb", "HiFi"}, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	raw, _ := os.ReadFile(filepath.Join(run, "phone-audiod.args"))
	if got, want := strings.TrimSpace(string(raw)), "--run-dir "+run+" --sink alsa:plughw:0,2 --ucm-card hw:0 --ucm-verb HiFi"; got != want {
		t.Errorf("phone-audiod %q, want %q", got, want)
	}
}

// --keep-going after an in-place restart: the services an earlier image of
// the process started are its children, and the start over ends them.
func TestStopChildren(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopChildren()
	if err := syscall.Kill(cmd.Process.Pid, 0); err == nil {
		_ = cmd.Process.Kill()
		t.Fatal("the child still runs")
	}
}

// A non-zero exit is a crash, before the socket appears or after; so is a
// zero exit once the service has served.
func TestACrashedServiceIsRestarted(t *testing.T) {
	once := func(name, sock, first string) string {
		return "echo run >> \"$2/" + name + ".runs\"\n" +
			"if [ \"$(wc -l < \"$2/" + name + ".runs\")\" -eq 1 ]; then " + first + "; fi\n" +
			"touch \"$2/" + sock + "\"\nexec sleep 60\n"
	}
	bin, run, state := fakeServices(t, map[string]string{
		"phone-smsd": once("phone-smsd", svc.SmsdSock, "exit 1"),
		"phone-nfcd": once("phone-nfcd", svc.NfcdSock, "touch \"$2/"+svc.NfcdSock+"\"; exit 0"),
	})
	s, err := startServices(bin, run, state, "", []string{"--sink", "auto"}, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runs(t, run, "phone-nfcd") < 2 {
		time.Sleep(50 * time.Millisecond)
	}
	for _, name := range []string{"phone-smsd", "phone-nfcd"} {
		if n := runs(t, run, name); n != 2 || s.hasDeclined(name) {
			t.Errorf("%s ran %d times, declined %v", name, n, s.hasDeclined(name))
		}
	}
}
