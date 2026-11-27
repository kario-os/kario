// prog is a coverage build's program for covflush's tests: "wait" runs
// reached and then waits to be killed, "confined" the same once it may no
// longer open or make a file (as phone-usbshared, which calls Confine
// first), "exec" runs execed and execs true.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/tools/covflush"
	_ "github.com/kario-os/kario/tools/covflush/testdata/prog/start"
)

func reached() { fmt.Println("ready") }

func execed() { fmt.Println("exec") }

func main() {
	switch os.Args[1] {
	case "wait":
		reached()
		select {}
	case "confined":
		covflush.Confine()
		if err := sandbox.DescriptorsOnly(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		reached()
		select {}
	case "exec":
		execed()
		covflush.Flush()
		t, err := exec.LookPath("true")
		if err == nil {
			err = syscall.Exec(t, []string{"true"}, os.Environ())
		}
		fmt.Println(err)
		os.Exit(1)
	}
}
