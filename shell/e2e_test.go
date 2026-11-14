package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// running lists the processes still running a program under dir.
func running(dir string) []int {
	var pids []int
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil && strings.HasPrefix(exe, dir+string(filepath.Separator)) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// noneLeft fails the test if a program under dir outlives it (a frozen app
// the Shell left behind stays stopped forever), and kills what is left.
func noneLeft(t *testing.T, dir string) {
	t.Cleanup(func() {
		left := running(dir)
		for end := time.Now().Add(3 * time.Second); len(left) > 0 && time.Now().Before(end); left = running(dir) {
			time.Sleep(50 * time.Millisecond)
		}
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		if len(left) > 0 {
			t.Errorf("%d processes outlived the test: %v", len(left), left)
		}
	})
}

// TestSelftestScenario builds every binary, starts private services and runs
// the built-in end-to-end scenario through the real process boundaries.
func TestSelftestScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end scenario skipped in -short mode")
	}
	tmp := t.TempDir()
	noneLeft(t, tmp)
	bin, prefix := filepath.Join(tmp, "bin"), filepath.Join(tmp, "usr")
	build := exec.Command("go", "build", "-o", bin+"/", "github.com/kario-os/kario/cmd/...", "github.com/kario-os/kario/apps/...", "github.com/kario-os/kario/cts/...", "github.com/kario-os/kario/examples/...")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if out, err := exec.Command("sh", "../scripts/stage.sh", bin, prefix).CombinedOutput(); err != nil {
		t.Fatalf("stage: %v\n%s", err, out)
	}
	run, state := filepath.Join(tmp, "run"), filepath.Join(tmp, "state")
	for _, d := range []string{run, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	services := [][]string{
		{"phone-mediad", "--run-dir", run},
		{"phone-keystored", "--run-dir", run, "--state-dir", state},
		{"phone-audiod", "--run-dir", run, "--sink", "null"},
		{"phone-imed", "--run-dir", run, "--state-dir", state},
		{"phone-nfcd", "--run-dir", run, "--state-dir", state},
		{"phone-smsd", "--run-dir", run, "--state-dir", state},
		{"phone-pushgw", "--listen", "127.0.0.1:0", "--addr-file", filepath.Join(run, "pushgw.addr")},
		{"phone-pushd", "--run-dir", run, "--state-dir", state, "--gateway-file", filepath.Join(run, "pushgw.addr")},
		{"phone-chatsrv", "--listen", "127.0.0.1:0", "--addr-file", filepath.Join(run, "chatsrv.addr"), "--gateway-file", filepath.Join(run, "pushgw.addr")},
		{"phone-storesrv", "--listen", "127.0.0.1:0", "--addr-file", filepath.Join(run, "storesrv.addr"), "--data", filepath.Join(tmp, "storesrv"),
			"--root-out", filepath.Join(state, "factory", "stores", "store.pub"), "--admin-token-file", filepath.Join(run, "storesrv-admin.token")},
	}
	socks := []string{"mediad.sock", "mediad-frames.sock", "keystored.sock", "audiod.sock", "imed.sock", "nfcd.sock", "smsd.sock", "pushd.sock", "chatsrv.addr", "storesrv.addr"}
	if _, err := os.Stat(filepath.Join(prefix, "bin", "phone-telephonyd")); err == nil {
		services = append(services, []string{"phone-telephonyd", "--run-dir", run, "--state-dir", state})
		socks = append(socks, "telephonyd.sock")
	}
	// Restarted when they exit, as systemd does on the device (the input
	// method case kills phone-imed, a messages case phone-smsd).
	var mu sync.Mutex
	stopping := false
	for _, svc := range services {
		start := func() (*exec.Cmd, error) {
			cmd := exec.Command(filepath.Join(prefix, "bin", svc[0]), svc[1:]...)
			return cmd, cmd.Start()
		}
		cmd, err := start()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_ = cmd.Wait()
				mu.Lock()
				stop := stopping
				mu.Unlock()
				if stop {
					return
				}
				time.Sleep(500 * time.Millisecond)
				next, err := start()
				if err != nil {
					return
				}
				mu.Lock()
				cmd = next
				mu.Unlock()
			}
		}()
		t.Cleanup(func() {
			mu.Lock()
			stopping = true
			_ = cmd.Process.Kill()
			mu.Unlock()
			<-done
		})
	}
	for _, s := range socks {
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(run, s)); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not appear", s)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	chatURL, _ := os.ReadFile(filepath.Join(run, "chatsrv.addr"))
	storeURL, _ := os.ReadFile(filepath.Join(run, "storesrv.addr"))
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(prefix, "share", "phone", "apps"),
		AppBinDir: filepath.Join(prefix, "lib", "phone", "bin"), RunDir: run, StateDir: state,
		AppEnv: []string{"PHONE_CHAT_SERVER=" + strings.TrimSpace(string(chatURL)), "PHONE_STORE_SERVER=" + strings.TrimSpace(string(storeURL))}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown()
	var out strings.Builder
	if !(&Runner{C: c, Out: &out}).Run(SelftestScript) {
		t.Fatalf("scenario failed:\n%s", out.String())
	}
	t.Log("\n" + out.String())
}
