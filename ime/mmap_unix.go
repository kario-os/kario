//go:build unix

package ime

import (
	"os"
	"syscall"
)

// mapFile maps a dictionary read-only; the mapping is never undone, the
// strings handed out point into it.
func mapFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() < headerSize {
		return nil, os.ErrInvalid
	}
	return syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
}
