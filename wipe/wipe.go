// Package wipe is a factory reset's erasing of the data area
// (docs/31-lock-and-biometrics.md §11). The Shell leaves a request in the
// persistent area, which the wipe does not reach (on a phone the state
// partition, /var/lib/slots, outside the data partition), and restarts the
// phone; the wipe happens on the next boot before the phone's services
// start. On a phone the early boot does it with yocto's data-wipe: the
// initramfs makes the data partition anew, and phone-wipe.service empties
// the data directories where there is no such partition. On a developer
// machine phone-shell does it to its state dir with Boot before it starts
// its services. Both take the same steps and keep the same paths (Keep).
package wipe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/keystore"
)

// Keep are the paths under the state dir that a wipe keeps: the device keys,
// which the factory's certificate names, and the factory's files (the
// device certificates, the roots, the store roots the factory provisioned).
// yocto's data-wipe keeps the same (WIPE_KEEP, under /var/lib/phone).
var Keep = []string{"factory", "keystore/device.key", "keystore/device-sm2.key"}

// What a wipe leaves in the persistent area until it is done: the request,
// the kept paths and the mark that they are all there.
const (
	dirName  = "wipe"
	request  = "request"
	keepName = "keep"
	keptMark = "kept"
)

// Why a reset was asked for (Request).
const (
	FromSettings   = "settings"   // Settings, with the lock-screen or the parental PIN
	FromLockScreen = "lockscreen" // the lock screen, without the PIN (forgotten)
)

// Request asks for a wipe on the next boot.
func Request(persist, why string) error {
	if persist == "" {
		return errors.New("wipe: no persistent area")
	}
	dir := filepath.Join(persist, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, request+".tmp")
	line := fmt.Sprintf("kario-wipe/1 %s %d\n", why, time.Now().Unix())
	if err := writeSync(tmp, []byte(line), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, request)); err != nil {
		return err
	}
	return syncDir(dir)
}

// Requested: a wipe waits for the next boot.
func Requested(persist string) bool {
	_, err := os.Stat(filepath.Join(persist, dirName, request))
	return persist != "" && err == nil
}

// Boot does a requested wipe of the state dir and reports whether there was
// one: the kept paths copied into the persistent area (once: a wipe cut
// short starts over from the copies), the state dir emptied, the kept paths
// put back with a new data salt (keystore.NewDataSalt), the request removed
// last.
func Boot(state, persist string) (bool, error) {
	if !Requested(persist) {
		return false, nil
	}
	dir := filepath.Join(persist, dirName)
	keep := filepath.Join(dir, keepName)
	if _, err := os.Stat(filepath.Join(dir, keptMark)); err != nil {
		if err := os.RemoveAll(keep); err != nil {
			return true, err
		}
		for _, rel := range Keep {
			if err := copyTree(filepath.Join(state, rel), filepath.Join(keep, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return true, fmt.Errorf("wipe: keeping %s: %w", rel, err)
			}
		}
		if err := writeSync(filepath.Join(dir, keptMark), nil, 0o600); err != nil {
			return true, err
		}
	}
	ents, err := os.ReadDir(state)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return true, err
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(state, e.Name())); err != nil {
			return true, err
		}
	}
	for _, rel := range Keep {
		if err := copyTree(filepath.Join(keep, rel), filepath.Join(state, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return true, fmt.Errorf("wipe: putting back %s: %w", rel, err)
		}
	}
	if err := keystore.NewDataSalt(state); err != nil {
		return true, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return true, err
	}
	return true, syncDir(persist)
}

// Why is the reason the pending request gives, "" for none.
func Why(persist string) string {
	raw, err := os.ReadFile(filepath.Join(persist, dirName, request))
	if err != nil {
		return ""
	}
	f := strings.Fields(string(raw))
	if len(f) < 2 || f[0] != "kario-wipe/1" {
		return ""
	}
	return f[1]
}

// copyTree copies a file or a directory with what is in it, modes kept.
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	switch {
	case st.IsDir():
		if err := os.MkdirAll(dst, st.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chmod(dst, st.Mode().Perm()); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	case st.Mode().IsRegular():
		raw, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := writeSync(dst, raw, st.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(dst, st.Mode().Perm())
	}
	return fmt.Errorf("%s is neither a file nor a directory", src)
}

func writeSync(path string, raw []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
