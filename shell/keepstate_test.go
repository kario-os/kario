package shell

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// tree reads a directory as path -> mode and content (a link's target).
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := e.Info()
		if err != nil {
			return err
		}
		v := info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			v += " -> " + target
		case info.Mode().IsRegular():
			raw, _ := os.ReadFile(p)
			v += fmt.Sprintf(" %.20q %d %s", raw, len(raw), info.ModTime().UTC().Format(time.RFC3339Nano))
		}
		out[rel] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// differ lists the paths whose entries differ.
func differ(got, want map[string]string) []string {
	var d []string
	for k := range maps.Keys(got) {
		if got[k] != want[k] {
			d = append(d, k+": "+got[k]+", want "+want[k])
		}
	}
	for k := range maps.Keys(want) {
		if _, ok := got[k]; !ok {
			d = append(d, k+": missing, want "+want[k])
		}
	}
	slices.Sort(d)
	return d
}

func write(t *testing.T, path, text string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// aged sets the modification times under root an hour back: files a case
// did not touch.
func aged(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.Type()&fs.ModeSymlink == 0 {
			_ = os.Chtimes(p, old, old)
		}
		return nil
	})
}

func stateDirs(t *testing.T) (state, persist string, s *StateCopy) {
	root := t.TempDir()
	state, persist = filepath.Join(root, "state"), filepath.Join(root, "persist")
	write(t, filepath.Join(state, "shell", "lock.json"), `{"secure":false}`, 0o600)
	write(t, filepath.Join(state, "apps", "com.phone.notes", "notes.json"), "[]", 0o640)
	write(t, filepath.Join(state, "files", "photo.jpg"), strings.Repeat("x", 1<<16), 0o600)
	if err := os.Symlink("../shell/lock.json", filepath.Join(state, "apps", "lock")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(persist, "device-id"), "PH-1", 0o400)
	aged(t, root)
	return state, persist, &StateCopy{Dirs: []string{state, persist}, Dir: filepath.Join(root, "copy")}
}

// What a failed case did to the state is undone: files it wrote over
// (one with the size and the time it had), added, removed, changed the
// mode of, a link it moved and a directory it made.
func TestStateCopyPutsTheStateBack(t *testing.T) {
	state, persist, s := stateDirs(t)
	before, beforeP := tree(t, state), tree(t, persist)
	if _, err := s.Save(); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(state, "shell", "lock.json")
	info, _ := os.Stat(lock)
	write(t, lock, `{"secure":true!}`, 0o600)
	_ = os.Chtimes(lock, info.ModTime(), info.ModTime())
	write(t, filepath.Join(state, "files", "during.txt"), "y", 0o600)
	_ = os.Remove(filepath.Join(state, "files", "photo.jpg"))
	_ = os.Chmod(filepath.Join(state, "apps", "com.phone.notes", "notes.json"), 0o600)
	_ = os.Remove(filepath.Join(state, "apps", "lock"))
	_ = os.Symlink("elsewhere", filepath.Join(state, "apps", "lock"))
	write(t, filepath.Join(state, "apps", "com.phone.new", "x"), "z", 0o600)
	_ = os.Chmod(filepath.Join(persist, "device-id"), 0o600)
	write(t, filepath.Join(persist, "device-id"), "PH-2", 0o400)

	st, err := s.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if d := differ(tree(t, state), before); len(d) > 0 {
		t.Errorf("state after the restore:\n%s", strings.Join(d, "\n"))
	}
	if d := differ(tree(t, persist), beforeP); len(d) > 0 {
		t.Errorf("persist after the restore:\n%s", strings.Join(d, "\n"))
	}
	if st.Removed != 2 {
		t.Errorf("restore: %s, want 2 removed (during.txt, com.phone.new)", st)
	}
}

// A Save copies only the files that changed since the last one, a file
// rewritten with the same size and time (a coarse clock's tick, or a
// writer that sets the time back) too.
func TestStateCopySavesWhatChanged(t *testing.T) {
	if !alone(t) {
		return
	}
	state, _, s := stateDirs(t)
	// Files that changed just before a Save are copied at the next too.
	time.Sleep(copyRecent + 100*time.Millisecond)
	st, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 4 || st.Copied != 5 || st.Bytes < 1<<16 {
		t.Fatalf("first save: %s, want all 4 files and the link", st)
	}
	if st, _ = s.Save(); st.Copied != 0 || st.Removed != 0 {
		t.Fatalf("second save: %s, want nothing copied", st)
	}
	notes := filepath.Join(state, "apps", "com.phone.notes", "notes.json")
	write(t, notes, `["milk"]`, 0o640)
	if st, _ = s.Save(); st.Copied != 1 || st.Bytes != int64(len(`["milk"]`)) {
		t.Fatalf("after one file changed: %s, want it alone copied", st)
	}
	info, _ := os.Stat(notes)
	write(t, notes, `["mild"]`, 0o640)
	_ = os.Chtimes(notes, info.ModTime(), info.ModTime())
	if st, _ = s.Save(); st.Copied != 1 {
		t.Fatalf("after a rewrite with the same size and time: %s, want it copied again", st)
	}
	if raw, _ := os.ReadFile(filepath.Join(s.Dir, "0", "apps", "com.phone.notes", "notes.json")); string(raw) != `["mild"]` {
		t.Fatalf("the copy holds %s", raw)
	}
}

// A copy that is not whole (a Save that failed, or none yet) is not
// restored; a new StateCopy on the same directory (the process the phone
// starts over in) finds the last whole one.
func TestStateCopyRestoresOnlyAWholeCopy(t *testing.T) {
	state, persist, s := stateDirs(t)
	if _, err := s.Restore(); err == nil {
		t.Fatal("restored without a copy")
	}
	before := tree(t, state)
	if _, err := s.Save(); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(state, "files", "during.txt"), "y", 0o600)
	again := &StateCopy{Dirs: []string{state, persist}, Dir: s.Dir}
	if _, err := again.Restore(); err != nil {
		t.Fatal(err)
	}
	if d := differ(tree(t, state), before); len(d) > 0 {
		t.Errorf("state after the restore:\n%s", strings.Join(d, "\n"))
	}
	_ = os.Remove(filepath.Join(s.Dir, copyMarker))
	if _, err := again.Restore(); err == nil {
		t.Fatal("restored a copy without its marker")
	}
}
