package shell

import (
	"fmt"
	"hash/adler32"
	"io"
	"math/rand/v2"
	"time"
)

// askAgain: a request the harness has not answered yet (no press, no
// finger) is said again this long after it was last said (scaled).
var askAgain = 3 * time.Second

// ask says a request to whoever runs the selftest (yocto/qemu-buttons.py
// on QEMU) on the output as line n of a console file of the Runner's own
// for that kind of request, in one write, as the image's console-file
// writes a line:
//
//	PHONE-FILE <kind>-<id>.ask <n> <Adler-32 of line> <line>
//
// The kernel prints on the console past the tty and its line can land
// inside this one; the host takes it out where the sum then holds and
// answers each name and n once (a Shell that took the script over counts
// from 1 again, under an id of its own). again says the line once more
// when askAgain has passed since it was last said: a line can be lost
// (the journal's rate limit) or damaged past mending.
func (r *Runner) ask(kind string, n int, line string) (again func()) {
	if r.askID == "" {
		r.askID = fmt.Sprintf("%08x", rand.Uint32())
	}
	said := fmt.Sprintf("PHONE-FILE %s-%s.ask %d %08x %s\n", kind, r.askID, n, adler32.Checksum([]byte(line)), line)
	io.WriteString(r.Out, said)
	last := time.Now()
	return func() {
		if time.Since(last) >= r.scaled(askAgain) {
			io.WriteString(r.Out, said)
			last = time.Now()
		}
	}
}
