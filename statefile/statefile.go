// Package statefile writes the phone's state files whole and reads them
// back, for the Shell and the system services: a file is replaced in one
// step, so a crash, a power cut or a full disk in mid-write leaves the old
// file or the new one, never a part of either. State whose loss would
// weaken the phone's protections (a parent's limits, the store's takedowns)
// is written twice, the second time to path.bak, and read back from there
// when path is damaged.
package statefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// Bak is the suffix of the copy WriteKeep keeps.
const Bak = ".bak"

// ErrDamaged: the file is there but does not read or decode whole, and no
// copy kept of it does.
var ErrDamaged = errors.New("damaged")

type tempFile interface {
	io.Writer
	Name() string
	Chmod(fs.FileMode) error
	Sync() error
	Close() error
}

// createTemp makes the temporary file a write goes to (tests put a writer
// that fails or stops in mid-write in its place).
var createTemp = func(dir, pattern string) (tempFile, error) { return os.CreateTemp(dir, pattern) }

// Write replaces path with data: a temporary file beside it is written,
// given perm, synced and renamed over path, then the directory is synced.
// The directory is made (0700) if missing. Unlike os.WriteFile the mode is
// perm exactly, not reduced by the umask, and a symbolic link at path is
// replaced rather than followed. Temporary files of path that a killed
// writer left go with the next write.
func Write(path string, data []byte, perm fs.FileMode) error {
	if err := replace(path, data, perm); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// WriteKeep is Write, then the same again to path.bak: a copy of the last
// state written whole, apart from path's blocks, for ReadJSON to fall back
// on.
func WriteKeep(path string, data []byte, perm fs.FileMode) error {
	if err := replace(path, data, perm); err != nil {
		return err
	}
	err := replace(path+Bak, data, perm)
	syncDir(filepath.Dir(path))
	return err
}

// Remove removes path and the copy WriteKeep kept, the copy first: a file
// missing beside its copy counts as damaged (ReadJSON).
func Remove(path string) error {
	if err := os.Remove(path + Bak); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// inflight holds the temporary files this process is writing now, which
// sweep leaves alone.
var inflight sync.Map

func replace(path string, data []byte, perm fs.FileMode) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := createTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	mine := filepath.Join(dir, filepath.Base(tmp))
	inflight.Store(mine, true)
	defer inflight.Delete(mine)
	err = f.Chmod(perm)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	sweep(dir, base)
	return nil
}

// sweep removes the temporary files of base in dir that a writer killed in
// mid-write left (a power cut, SIGKILL): regular files named exactly as
// replace names them, other than this process's own in flight.
func sweep(dir, base string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if _, busy := inflight.Load(p); !busy && e.Type().IsRegular() && isTemp(e.Name(), base) {
			_ = os.Remove(p)
		}
	}
}

// isTemp: name is one of os.CreateTemp's names for "."+base+".*.tmp", the
// random part being decimal digits.
func isTemp(name, base string) bool {
	rest, ok := strings.CutPrefix(name, "."+base+".")
	if !ok {
		return false
	}
	digits, ok := strings.CutSuffix(rest, ".tmp")
	return ok && digits != "" && strings.Trim(digits, "0123456789") == ""
}

// SyncTree syncs every file and directory under dir, dir too: a directory
// written file by file (an app's code unpacked) is whole on the disk before
// it is renamed into place.
func SyncTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			syncDir(path)
		case e.Type().IsRegular():
			return syncPath(path)
		}
		return nil
	})
}

// SyncDir makes the renames in dir durable, as Write does after its own.
func SyncDir(dir string) { syncDir(dir) }

// syncPath syncs the file or directory at path (tests watch it).
var syncPath = func(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncDir makes the renames in dir durable; a file system that cannot sync
// a directory has them durable with its next commit.
func syncDir(dir string) { _ = syncPath(dir) }

// ReadJSON decodes the state file at path into v, which only a file that
// decodes whole changes. When path is damaged (it does not read or decode,
// or it is missing beside its copy) the copy WriteKeep kept is decoded
// instead, bak says so, and path is written again from it. Neither file
// there: an error wrapping fs.ErrNotExist. Neither whole: one wrapping
// ErrDamaged, with both files left as they are.
func ReadJSON(path string, v any) (bak bool, err error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		if err = decode(raw, v); err == nil {
			return false, nil
		}
	}
	missing := errors.Is(err, fs.ErrNotExist)
	kept, kerr := os.ReadFile(path + Bak)
	switch {
	case kerr == nil:
		if kerr = decode(kept, v); kerr == nil {
			mode := fs.FileMode(0o600)
			if st, err := os.Stat(path + Bak); err == nil {
				mode = st.Mode().Perm()
			}
			_ = Write(path, kept, mode)
			return true, nil
		}
	case missing && errors.Is(kerr, fs.ErrNotExist):
		return false, err
	}
	if missing {
		return false, fmt.Errorf("%w: %s missing, its copy: %v", ErrDamaged, path, kerr)
	}
	if errors.Is(kerr, fs.ErrNotExist) {
		return false, fmt.Errorf("%w: %v", ErrDamaged, err)
	}
	return false, fmt.Errorf("%w: %v; its copy: %v", ErrDamaged, err, kerr)
}

func decode(raw []byte, v any) error {
	if err := json.Unmarshal(raw, reflect.New(reflect.TypeOf(v).Elem()).Interface()); err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
