package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cable's SSH counts as quiet by the send queues of its established
// connections to or from port 22 only (lines as QEMU's kernel wrote them).
func TestUnackedSSH(t *testing.T) {
	phone := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 5471 1 0000000000000000 100 0 0 10 0
   1: 0000000000000000FFFF00000100470A:0016 0000000000000000FFFF00000D00470A:E862 01 000000FC:00000000 01:00000016 00000003     0        0 9122 2 0000000000000000 20 4 30 10 -1
`
	computer := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0D00470A:9F2A 0100470A:0016 06 00000000:00000000 03:00000F3C 00000000     0        0 0 3 0000000000000000
   1: 0D00470A:E862 0100470A:0016 01 00000025:00000000 01:00000020 00000002     0        0 9120 2 0000000000000000 20 4 1 10 -1
   2: 0100007F:EBB0 0100007F:1B9E 01 00000010:00000000 00:00000000 00000000     0        0 9300 1 0000000000000000 20 4 1 10 -1
`
	if n := unackedSSH(phone); n != 0xfc {
		t.Errorf("the phone's side: %d bytes, want %d", n, 0xfc)
	}
	if n := unackedSSH(computer); n != 0x25 {
		t.Errorf("the computer's side: %d bytes, want %d", n, 0x25)
	}
	if n := unackedSSH(strings.ReplaceAll(phone, "000000FC", "00000000")); n != 0 {
		t.Errorf("acknowledged: %d bytes", n)
	}
}

// What a dev-forward failure quotes of the Shell's log: the developer
// link, developer mode and the cable's firewall since the step began.
func TestDevLinkLog(t *testing.T) {
	r := &Runner{C: &Core{}}
	r.C.cfg.RunDir = t.TempDir()
	log := filepath.Join(r.C.cfg.RunDir, "shell.log")
	if err := os.WriteFile(log, []byte("shell: 10:00:00 dev link: forwarding to 127.0.0.1:7071 on the phone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	from := r.shellLogEnd()
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("shell: 10:00:01 usb: the link reaches udp [67], tcp [80]\nshell: 10:00:01 script 6362: dev-forward 7071 7070\nshell: 10:00:01 developer mode false\n" +
		"shell: 10:00:02 dev link: forwarding to 127.0.0.1:7071 on the phone ended: developer mode is off\n")
	f.Close()
	got := r.devLinkLog(from)
	for _, want := range []string{"usb: the link reaches", "developer mode false", "ended: developer mode is off"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "10:00:00") || strings.Contains(got, "script") {
		t.Errorf("%q has lines from before the step or of the script", got)
	}
}
