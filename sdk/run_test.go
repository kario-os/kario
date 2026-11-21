package sdk

import (
	"os"
	"strconv"
	"sync"
	"syscall"
	"testing"
)

// Posting while the Shell goes away neither panics nor races with Run's
// shutdown (go test -race), and what was posted before still runs.
func TestPostDuringShutdown(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := os.NewFile(uintptr(fds[1]), "shell")
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{})
	a.Post(func() { close(ran) })
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					a.Post(func() {})
				}
			}
		}()
	}
	go func() {
		<-ran
		shell.Close()
	}()
	if err := a.Run(); err == nil {
		t.Fatal("Run returned no error when the Shell went away")
	}
	close(stop)
	wg.Wait()
	a.Post(func() {})
}
