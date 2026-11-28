// Package statefiletest makes file writes fail midway, as a full disk or
// the power going does, for tests that a writer leaves its file as it was.
package statefiletest

import (
	"syscall"
	"testing"
)

// FailPast runs f with every write into a regular file past n bytes failing
// (EFBIG: Go ignores SIGXFSZ). The limit is the whole process's: a test
// using it must not run in parallel with others writing files.
func FailPast(t testing.TB, n uint64, f func()) {
	t.Helper()
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: n, Max: lim.Max}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
			t.Fatal(err)
		}
	}()
	f()
}
