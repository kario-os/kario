// phone-imedict compiles the input method's word lists into the read-only
// dictionary that phone-imed maps into memory (share/phone/ime.dict).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kario-os/kario/ime"
)

func main() {
	out := flag.String("o", "ime.dict", "output file")
	flag.Parse()
	if err := ime.WriteDict(*out); err != nil {
		fmt.Fprintln(os.Stderr, "phone-imedict:", err)
		os.Exit(1)
	}
	st, _ := os.Stat(*out)
	fmt.Printf("%s: %.1f MB\n", *out, float64(st.Size())/1e6)
}
