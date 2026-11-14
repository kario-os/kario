package nfc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/hal"
)

// WakeSource makes the controller behind a raw NCI device node a wake-up
// source, so that its interrupt (a reader's field, a command for the
// host) resumes a suspended phone: the kernel's power/wakeup attribute of
// the device the node belongs to (the I2C client), else of the node's own
// device. The driver must offer it (device_init_wakeup, or "wakeup-source"
// in the device tree).
func WakeSource(dev string) error {
	var st unix.Stat_t
	if err := unix.Stat(dev, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return fmt.Errorf("%s is not a character device", dev)
	}
	rdev := uint64(st.Rdev)
	node := filepath.Join(hal.SysRoot, "dev", "char", fmt.Sprintf("%d:%d", unix.Major(rdev), unix.Minor(rdev)))
	for _, attr := range []string{filepath.Join(node, "device", "power", "wakeup"), filepath.Join(node, "power", "wakeup")} {
		raw, err := os.ReadFile(attr)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(raw)) == "enabled" {
			return nil
		}
		f, err := os.OpenFile(attr, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		_, err = f.WriteString("enabled")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return errors.New("the driver offers no wake-up for " + dev)
}
