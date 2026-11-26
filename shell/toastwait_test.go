package shell

import (
	"io"
	"testing"
)

// A toast that came and went before the runner looked (the bar-t9 pixel
// run's wait for "Call ended" after hanging up, on a loaded machine) still counts
// for wait and wait-re until the script's next input, and never for gone.
func TestWaitSeesAToastThatWentAlready(t *testing.T) {
	c := snapCore(t)
	r := &Runner{C: c, Out: io.Discard}
	if err := r.step("sleep", "1"); err != nil {
		t.Fatal(err)
	}
	c.showToast("Call ended 00:03")
	c.toast = nil
	if err := r.step("wait", "Call ended 50"); err != nil {
		t.Error(err)
	}
	if err := r.step("wait-re", `ended \d\d:\d\d 50`); err != nil {
		t.Error(err)
	}
	if err := r.step("gone", "Call ended 50"); err != nil {
		t.Error(err)
	}
	if err := r.step("type", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.step("wait", "Call ended 50"); err == nil {
		t.Error("a toast from before the last input counted")
	}
}
