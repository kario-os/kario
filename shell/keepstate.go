package shell

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"
)

// StateCopy keeps the phone's persistent state as the running case found it
// (--keep-going, Runner.Keep): after a failed case the phone starts over
// from the copy, not from what the case left behind. Save brings the copy
// up to date and Restore puts it back, each copying only the files that
// differ: a file is taken to be unchanged when its size, modification
// time, mode and owner are its copy's and the state's file has not changed
// (its inode's change time) since a little before the last Save: the
// kernel stamps files with a coarse clock, and a writer may set the time
// back. Files are copied, not linked: the Shell, the services and the apps
// rewrite files in place.
type StateCopy struct {
	// Dirs are the directories kept (the state and persist directories);
	// Dir holds their copies, one subdirectory each.
	Dirs []string
	Dir  string
	// saved is when the last Save started (its marker file has it too, for
	// the process the phone starts over in).
	saved time.Time
}

// CopyStats is what a Save or a Restore did.
type CopyStats struct {
	Files, Copied, Removed int
	Bytes                  int64
	Took                   time.Duration
}

func (s CopyStats) String() string {
	return fmt.Sprintf("%d files, %d copied (%d bytes), %d removed in %s", s.Files, s.Copied, s.Bytes, s.Removed, s.Took.Round(time.Microsecond))
}

// copyRecent is how long before the last Save a change counts as after it:
// a coarse clock's tick, or a file system's whole second.
const copyRecent = 2 * time.Second

// copyMarker is in Dir while the copy is whole: its content is when the
// Save started.
const copyMarker = "saved"

// Save makes the copy match the directories as they are now.
func (s *StateCopy) Save() (CopyStats, error) {
	t0 := time.Now()
	var st CopyStats
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return st, err
	}
	marker := filepath.Join(s.Dir, copyMarker)
	if s.saved.IsZero() {
		s.saved = s.savedAt()
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return st, err
	}
	since := s.saved
	for i, d := range s.Dirs {
		if err := mirror(d, filepath.Join(s.Dir, strconv.Itoa(i)), func(state, _ fs.FileInfo) bool { return changedSince(state, since) }, &st); err != nil {
			return st, err
		}
	}
	s.saved = t0
	st.Took = time.Since(t0)
	return st, os.WriteFile(marker, []byte(t0.Format(time.RFC3339Nano)), 0o600)
}

// Restore puts the directories back as the last Save found them; nothing
// may write to them meanwhile.
func (s *StateCopy) Restore() (CopyStats, error) {
	t0 := time.Now()
	var st CopyStats
	saved := s.savedAt()
	if saved.IsZero() {
		return st, errors.New("no whole copy of the state")
	}
	for i, d := range s.Dirs {
		if err := mirror(filepath.Join(s.Dir, strconv.Itoa(i)), d, func(_, state fs.FileInfo) bool { return changedSince(state, saved) }, &st); err != nil {
			return st, err
		}
	}
	st.Took = time.Since(t0)
	return st, nil
}

func (s *StateCopy) savedAt() time.Time {
	raw, err := os.ReadFile(filepath.Join(s.Dir, copyMarker))
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, string(raw))
	return t
}

// mirror makes dst a copy of src (missing: empty): directories, regular
// files and symbolic links with their modes, owners and modification
// times. A regular file is copied when it differs from dst's or changed
// says so. Sockets and pipes are left out; whoever serves on them makes
// them again.
func mirror(src, dst string, changed func(src, dst fs.FileInfo) bool, st *CopyStats) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	type dir struct {
		path string
		info fs.FileInfo
	}
	var dirs []dir
	have := map[string]bool{".": true}
	err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			// Gone since its directory was read.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if rel == "." {
			dirs = append(dirs, dir{to, info})
			return nil
		}
		was, _ := os.Lstat(to)
		switch m := info.Mode(); {
		case m.IsDir():
			if was != nil && !was.IsDir() {
				if err := os.RemoveAll(to); err != nil {
					return err
				}
				was = nil
			}
			if was == nil {
				if err := os.Mkdir(to, 0o700); err != nil {
					return err
				}
			} else if was.Mode().Perm()&0o700 != 0o700 {
				// Written into before its own mode is put back.
				if err := os.Chmod(to, 0o700); err != nil {
					return err
				}
			}
			dirs = append(dirs, dir{to, info})
		case m&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if was != nil && was.Mode()&fs.ModeSymlink != 0 {
				if t, _ := os.Readlink(to); t == target {
					break
				}
			}
			if err := os.RemoveAll(to); err != nil {
				return err
			}
			if err := os.Symlink(target, to); err != nil {
				return err
			}
			lchown(to, info)
			st.Copied++
		case m.IsRegular():
			st.Files++
			if was != nil && sameFile(info, was) && !changed(info, was) {
				break
			}
			n, err := copyFile(p, to, info)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			st.Copied++
			st.Bytes += n
		default:
			return nil
		}
		have[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	err = filepath.WalkDir(dst, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dst, p)
		if have[rel] {
			return nil
		}
		st.Removed++
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		if e.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Deepest first: setting a directory's time is the last change to it.
	for _, d := range slices.Backward(dirs) {
		if err := os.Chmod(d.path, d.info.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)); err != nil {
			return err
		}
		lchown(d.path, d.info)
		if err := os.Chtimes(d.path, d.info.ModTime(), d.info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}

// sameFile: b is taken to hold what a holds.
func sameFile(a, b fs.FileInfo) bool {
	if !b.Mode().IsRegular() || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) || a.Mode() != b.Mode() {
		return false
	}
	sa, oka := a.Sys().(*syscall.Stat_t)
	sb, okb := b.Sys().(*syscall.Stat_t)
	return !oka || !okb || sa.Uid == sb.Uid && sa.Gid == sb.Gid
}

// changedSince: the file was modified, or its inode changed, after a
// little before t (zero: no Save to compare with).
func changedSince(info fs.FileInfo, t time.Time) bool {
	if t.IsZero() {
		return false
	}
	from := t.Add(-copyRecent)
	if !info.ModTime().Before(from) {
		return true
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && !time.Unix(s.Ctim.Sec, s.Ctim.Nsec).Before(from)
}

// copyFile copies src over dst with its mode, owner and modification
// time. A file being written meanwhile (its size or time moved) is read
// again.
func copyFile(src, dst string, info fs.FileInfo) (int64, error) {
	for try := 0; ; try++ {
		n, err := copyOnce(src, dst, info)
		if err != nil {
			return n, err
		}
		now, err := os.Lstat(src)
		if err != nil {
			return n, err
		}
		if (now.Size() == n && now.ModTime().Equal(info.ModTime())) || try == 2 {
			return n, nil
		}
		info = now
	}
}

func copyOnce(src, dst string, info fs.FileInfo) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if err := os.RemoveAll(dst); err != nil {
			return 0, err
		}
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	lchown(dst, info)
	if err := os.Chmod(dst, info.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)); err != nil {
		return n, err
	}
	return n, os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// lchown gives path info's owner where this process may (root: the apps'
// own uids under the sandbox).
func lchown(path string, info fs.FileInfo) {
	if s, ok := info.Sys().(*syscall.Stat_t); ok && (int(s.Uid) != os.Getuid() || int(s.Gid) != os.Getgid()) {
		_ = os.Lchown(path, int(s.Uid), int(s.Gid))
	}
}
