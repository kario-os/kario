package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// btInquiry is the step bt-inquiry yes|no [ms]: btmon records the phone's
// controller for ms (not scaled: the radio inquires at its own pace, a
// round about every 16 s while BlueZ searches) and the HCI Inquiry
// commands in it are counted, at least one wanted or none. It prints
// PHONE-BT-INQUIRY. The simulated radio has no controller: there it is
// whether it searches.
func (r *Runner) btInquiry(want bool, ms int) error {
	var b *bluezRadio
	switch x := r.C.bt.(type) {
	case *simBT:
		st, err := x.state()
		if err == nil && st.Discovering != want {
			err = fmt.Errorf("the simulated radio searches: %v, want %v", st.Discovering, want)
		}
		return err
	case *bluezRadio:
		b = x
	default:
		return fmt.Errorf("no Bluetooth")
	}
	btmon, err := exec.LookPath("btmon")
	if err != nil {
		return fmt.Errorf("no btmon in the image: %w", err)
	}
	o, err := b.load()
	if err != nil {
		return err
	}
	hci := filepath.Base(string(b.adapter(o)))
	snoop := filepath.Join(os.TempDir(), "phone-bt-inquiry.btsnoop")
	defer os.Remove(snoop)
	rec := exec.Command(btmon, "-i", hci, "-w", snoop)
	if err := rec.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- rec.Wait() }()
	for end := time.Now().Add(time.Duration(ms) * time.Millisecond); time.Now().Before(end); {
		select {
		case err := <-done:
			return fmt.Errorf("btmon ended: %v", err)
		default:
			r.pump(50 * time.Millisecond)
		}
	}
	_ = rec.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = rec.Process.Kill()
		<-done
	}
	out, err := exec.Command(btmon, "-r", snoop).Output()
	if err != nil {
		return fmt.Errorf("btmon -r: %v", err)
	}
	n := strings.Count(string(out), "HCI Command: Inquiry (0x01|0x0001)")
	fmt.Fprintf(r.Out, "PHONE-BT-INQUIRY: %s: %d inquiries in %d ms\n", hci, n, ms)
	if (n > 0) != want {
		return fmt.Errorf("%s: %d inquiries in %d ms, want any: %v", hci, n, ms, want)
	}
	return nil
}
