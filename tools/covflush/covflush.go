// Package covflush writes a coverage build's counters while the program
// runs (make coverage, docs/40-testing.md §7). The Go runtime writes them
// into $GOCOVERDIR only when a program exits normally, and the system's
// processes seldom do: apps and services die by SIGTERM or SIGKILL, the
// sandbox launcher and the Shell exec another program. Only a build with
// the kariocover tag links it: packages every program links start it
// (their cover.go), the launcher and the Shell flush before they exec.
package covflush

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime/coverage"
	"strings"
	"sync"
	"time"
)

// Interval is how often Start writes: what a process ran in its last
// interval before it was killed is lost. A process that may no longer
// make files writes every ConfinedInterval: when it exits, the runtime
// cannot make its file either.
const (
	Interval         = time.Second
	ConfinedInterval = Interval / 10
)

var (
	once     sync.Once
	confined = make(chan struct{}, 1)
	mu       sync.Mutex
	// file is the counter file this process replaces at each write: one
	// file per process, which covdata reads as that process's run. It
	// stays open for a process that later may not make files any more
	// (phone-usbshared, confined to the descriptors it holds, flushes
	// just before).
	file *os.File
)

// Start writes the counters every Interval from now on, when GOCOVERDIR is
// set; calling it again does nothing. Not at once: a package's init runs
// before the runtime's coverage has its meta-data. A program built
// without -cover -covermode=atomic, or a test binary (the runtime writes
// its meta-data at exit), stops at the first write.
func Start() {
	if os.Getenv("GOCOVERDIR") == "" {
		return
	}
	once.Do(func() {
		go func() {
			t := time.NewTicker(Interval)
			defer t.Stop()
			for {
				select {
				case <-confined:
					t.Reset(ConfinedInterval)
					continue
				case <-t.C:
				}
				inPlace, err := write()
				if err != nil {
					return
				}
				if inPlace {
					t.Reset(ConfinedInterval)
				}
			}
		}()
	})
}

// Flush writes the counters now, when GOCOVERDIR is set.
func Flush() { _, _ = write() }

// Confine is Flush for a process about to stop making files: it makes
// its counter file while it still can, and writes over it every
// ConfinedInterval from then on.
func Confine() {
	Flush()
	select {
	case confined <- struct{}{}:
	default:
	}
}

// write writes the counters, in place when no new file can be made.
func write() (inPlace bool, err error) {
	dir := os.Getenv("GOCOVERDIR")
	if dir == "" {
		return false, nil
	}
	mu.Lock()
	defer mu.Unlock()
	err = replace(dir)
	if err == nil || file == nil || errors.Is(err, errNoCounters) {
		return false, err
	}
	// The open file is written over, which a kill in the middle of it
	// would leave torn.
	var buf bytes.Buffer
	if err = coverage.WriteCounters(&buf); err == nil {
		if _, err = file.WriteAt(buf.Bytes(), 0); err == nil {
			err = file.Truncate(int64(buf.Len()))
		}
	}
	return true, err
}

var errNoCounters = errors.New("covflush: the runtime wrote no counter file")

// replace has the runtime write the counters, named as it names them
// (covcounters.<meta hash>.<pid>.<time>), beside the directory's files,
// and moves them over this process's earlier file: a reader never sees
// half of one.
func replace(dir string) error {
	tmp, err := os.MkdirTemp(dir, ".flush-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := coverage.WriteCountersDir(tmp); err != nil {
		return errors.Join(errNoCounters, err)
	}
	ents, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "covcounters.") {
			continue
		}
		if file == nil {
			dst := filepath.Join(dir, e.Name())
			if err := os.Rename(filepath.Join(tmp, e.Name()), dst); err != nil {
				return err
			}
			file, err = os.OpenFile(dst, os.O_RDWR, 0)
			return err
		}
		return os.Rename(filepath.Join(tmp, e.Name()), file.Name())
	}
	return errNoCounters
}
