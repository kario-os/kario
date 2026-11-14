// phone-sandbox confines itself according to a policy and execs the app.
//
//	phone-sandbox <policy-json> <app> [args...]
//
// The shell starts it inside fresh namespaces (when running as root); every
// restriction applied here is inherited by the app across exec.
package sandboxcmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/kario-os/kario/sandbox"
)

// The restrictions are the calling thread's until the exec: main stays on
// the thread it started on. kario-system runs every program's init, so only
// the sandbox's own name locks.
func init() {
	if filepath.Base(os.Args[0]) == "phone-sandbox" {
		runtime.LockOSThread()
	}
}

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: phone-sandbox <policy-json> <app> [args...]")
		os.Exit(2)
	}
	// The shell may still be moving this process into the app's scope:
	// nothing runs before it says so.
	if v := os.Getenv("PHONE_SANDBOX_HOLD"); v != "" {
		if fd, err := strconv.Atoi(v); err == nil {
			hold := os.NewFile(uintptr(fd), "hold")
			_, _ = hold.Read(make([]byte, 1))
			hold.Close()
		}
		os.Unsetenv("PHONE_SANDBOX_HOLD")
	}
	var p sandbox.Policy
	if err := json.Unmarshal([]byte(os.Args[1]), &p); err != nil {
		fail(err)
	}
	applied, err := sandbox.Apply(p)
	if err != nil {
		fail(err)
	}
	env := append(os.Environ(), sandbox.Env+"="+strings.Join(applied, ","), "TMPDIR="+sandbox.TempDir(p))
	fail(syscall.Exec(os.Args[2], os.Args[2:], env))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "phone-sandbox:", err)
	os.Exit(126)
}
