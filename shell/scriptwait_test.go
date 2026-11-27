package shell

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// A wait whose text ends in a number takes the number for its timeout:
// one too small to wait for anything is the text's own (an emergency
// number, a sum), and the wait looks for the words before it for a moment
// only, passing on any of them ("=" is on the calculator's keys) and failing
// on a machine that answers later. The quickest wait meant is 400 ms.
func TestScriptWaitTimeouts(t *testing.T) {
	const least = 300
	scripts := map[string]string{"selftest.script": SelftestScript, "cts.script": CTSScript}
	for _, name := range []string{soakScript, "fdstore.script"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		scripts[name] = string(raw)
	}
	for name, script := range scripts {
		sc := bufio.NewScanner(strings.NewReader(script))
		for n := 1; sc.Scan(); n++ {
			line := strings.TrimSpace(sc.Text())
			cmd, arg, _ := strings.Cut(line, " ")
			if cmd != "wait" && cmd != "wait-re" {
				continue
			}
			if text, ms := lastNum(arg, least); ms < least {
				t.Errorf("%s:%d: %q waits %d ms for %q: give the timeout after the text", name, n, line, ms, text)
			}
		}
	}
}
