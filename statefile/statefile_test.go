package statefile

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

type state struct {
	Limit int    `json:"limit"`
	Name  string `json:"name"`
}

// partWriter writes the first half of what it is given to the temporary
// file, then stop decides what happens.
type partWriter struct {
	*os.File
	stop func() error
}

func (w partWriter) Write(p []byte) (int, error) {
	n, err := w.File.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	return n, w.stop()
}

func withPartWriter(t *testing.T, stop func() error) {
	saved := createTemp
	createTemp = func(dir, pattern string) (tempFile, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return partWriter{File: f, stop: stop}, nil
	}
	t.Cleanup(func() { createTemp = saved })
}

func readState(t *testing.T, path string) (state, bool) {
	t.Helper()
	var st state
	bak, err := ReadJSON(path, &st)
	if err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	return st, bak
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteGivesTheModeAndReplacesALink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	if err := Write(path, []byte(`{"limit":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, %v", st.Mode(), err)
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(other); string(raw) != "other" {
		t.Fatalf("written through the link: %q", raw)
	}
	if st, _ := os.Lstat(link); !st.Mode().IsRegular() {
		t.Fatalf("link not replaced: %v", st.Mode())
	}
}

// A write that fails half way (a full disk) leaves the old file and its
// copy whole and no temporary file behind.
func TestFailedWriteLeavesTheOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := WriteKeep(path, []byte(`{"limit":30,"name":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withPartWriter(t, func() error { return syscall.ENOSPC })
	if err := Write(path, []byte(`{"limit":60,"name":"new"}`), 0o600); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Write: %v", err)
	}
	if err := WriteKeep(path, []byte(`{"limit":60,"name":"new"}`), 0o600); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("WriteKeep: %v", err)
	}
	if st, bak := readState(t, path); st != (state{30, "old"}) || bak {
		t.Fatalf("after the failed writes: %+v (from the copy %v)", st, bak)
	}
	if got := strings.Join(entries(t, dir), " "); got != "state.json state.json.bak" {
		t.Fatalf("left in the directory: %s", got)
	}
}

// A writer killed in mid-write (as by a power cut or SIGKILL) leaves the
// old file whole: the child writes half the new file and waits on its
// standard input, then is killed.
func TestKilledWriterLeavesTheOldFile(t *testing.T) {
	if path := os.Getenv("STATEFILE_KILLED_CHILD"); path != "" {
		withPartWriter(t, func() error {
			os.Stdout.WriteString("half\n")
			_, _ = io.Copy(io.Discard, os.Stdin)
			return errors.New("not killed")
		})
		_ = WriteKeep(path, []byte(`{"limit":60,"name":"new"}`), 0o600)
		os.Exit(3)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := WriteKeep(path, []byte(`{"limit":30,"name":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestKilledWriterLeavesTheOldFile$")
	cmd.Env = append(os.Environ(), "STATEFILE_KILLED_CHILD="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if line != "half\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("the child said %q, %v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the child ended with %v", err)
	}
	tmps := 0
	for _, name := range entries(t, dir) {
		if strings.HasSuffix(name, ".tmp") {
			tmps++
		}
	}
	if tmps != 1 {
		t.Fatalf("the killed write left %d temporary files: %v", tmps, entries(t, dir))
	}
	if st, bak := readState(t, path); st != (state{30, "old"}) || bak {
		t.Fatalf("after the kill: %+v (from the copy %v)", st, bak)
	}
	if err := WriteKeep(path, []byte(`{"limit":60,"name":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := readState(t, path); st != (state{60, "new"}) {
		t.Fatalf("written after the kill: %+v", st)
	}
	if got := strings.Join(entries(t, dir), " "); got != "state.json state.json.bak" {
		t.Fatalf("left after the next write: %s", got)
	}
}

// A write takes away the temporary files of its own file that a killed
// writer left, and nothing else: not another file's, not names that only
// look like one, not a directory, not a temporary file another write of
// this process has open.
func TestWriteSweepsAKilledWritersTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	stale := []string{".state.json.123456.tmp", ".state.json.bak.42.tmp"}
	foreign := []string{".other.json.77.tmp", ".state.json.x1.tmp", ".state.json..tmp", "state.json.5.tmp",
		".state.json.88.tmp.keep", "notes.txt"}
	for _, name := range append(append([]string{}, stale...), foreign...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".state.json.9.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign = append(foreign, ".state.json.9.tmp")
	busy := filepath.Join(dir, ".state.json.31.tmp")
	if err := os.WriteFile(busy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	inflight.Store(busy, true)
	foreign = append(foreign, ".state.json.31.tmp")
	if err := WriteKeep(path, []byte(`{"limit":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inflight.Delete(busy)
	want := append([]string{"state.json", "state.json.bak"}, foreign...)
	slices.Sort(want)
	if got := entries(t, dir); !slices.Equal(got, want) {
		t.Fatalf("left in the directory:\n%v\nwant\n%v", got, want)
	}
}

// SyncTree syncs every file and directory under the one it is given.
func TestSyncTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app.new")
	for _, name := range []string{"manifest.json", "lib/a.js", "lib/deep/b.js", "META-INF/SIG"} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var synced []string
	saved := syncPath
	syncPath = func(path string) error {
		rel, _ := filepath.Rel(dir, path)
		synced = append(synced, filepath.ToSlash(rel))
		return saved(path)
	}
	t.Cleanup(func() { syncPath = saved })
	if err := SyncTree(dir); err != nil {
		t.Fatal(err)
	}
	slices.Sort(synced)
	want := []string{".", "META-INF", "META-INF/SIG", "lib", "lib/a.js", "lib/deep", "lib/deep/b.js", "manifest.json"}
	if !slices.Equal(synced, want) {
		t.Fatalf("synced %v, want %v", synced, want)
	}
	syncPath = func(path string) error {
		if strings.HasSuffix(path, "b.js") {
			return syscall.EIO
		}
		return nil
	}
	if err := SyncTree(dir); !errors.Is(err, syscall.EIO) {
		t.Fatalf("a file that does not sync: %v", err)
	}
}

// A damaged file reads from its copy and is written whole again; without a
// copy, or with both damaged, nothing is decoded and the files stay.
func TestReadJSONFallsBackOnTheCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := WriteKeep(path, []byte(`{"limit":30,"name":"kept"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"limit":30,"na`), 0o640); err != nil {
		t.Fatal(err)
	}
	if st, bak := readState(t, path); st != (state{30, "kept"}) || !bak {
		t.Fatalf("truncated: %+v (from the copy %v)", st, bak)
	}
	if st, bak := readState(t, path); st != (state{30, "kept"}) || bak {
		t.Fatalf("not written again: %+v (from the copy %v)", st, bak)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Fatalf("written again with mode %v", st.Mode())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if st, bak := readState(t, path); st != (state{30, "kept"}) || !bak {
		t.Fatalf("missing beside its copy: %+v (from the copy %v)", st, bak)
	}
	for name, files := range map[string][2]string{
		"both damaged":       {`{"limit":`, `[1,`},
		"no copy":            {``, "-"},
		"missing, copy torn": {"-", `{"li`},
		"wrong type":         {`{"limit":"30"}`, "-"},
	} {
		_ = Remove(path)
		for i, p := range []string{path, path + Bak} {
			if files[i] != "-" {
				if err := os.WriteFile(p, []byte(files[i]), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		st := state{Limit: 15, Name: "default"}
		if bak, err := ReadJSON(path, &st); !errors.Is(err, ErrDamaged) || bak || st != (state{15, "default"}) {
			t.Errorf("%s: %+v, %v, from the copy %v", name, st, err, bak)
		}
		if files[0] != "-" {
			if raw, _ := os.ReadFile(path); string(raw) != files[0] {
				t.Errorf("%s: the damaged file is now %q", name, raw)
			}
		}
	}
	_ = Remove(path)
	if got := entries(t, dir); len(got) != 0 {
		t.Fatalf("left after Remove: %v", got)
	}
	st := state{Limit: 15}
	if _, err := ReadJSON(path, &st); !errors.Is(err, fs.ErrNotExist) || st.Limit != 15 {
		t.Fatalf("none: %+v, %v", st, err)
	}
}
