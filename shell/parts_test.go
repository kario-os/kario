package shell

import (
	"strings"
	"testing"
)

const partsScript = "sleep 10\nsnap 00-boot\n#part a\n== a1\nrequire-env KARIO_TEST_NEVER_SET\nsleep 10\n" +
	"#part b\n== b1\nsleep 10\n#part a\n== a2\nrequire-previous\nsleep 10\n#part c\n== c1\nrequire-previous\nsleep 10\n"

// The device's parallel run (phone-shell --parts) runs some parts of the
// script in one Shell: the boot, then the parts named, each case at its own
// line; a part left for another keeps its "#part" line, so require-previous
// looks back in the case's own part only, as in the whole script.
func TestSelectParts(t *testing.T) {
	if !alone(t) {
		return
	}
	if got, err := ScriptParts(partsScript); err != nil || strings.Join(got, " ") != "a b c" {
		t.Fatalf("parts %q, %v", got, err)
	}
	got, err := SelectParts(partsScript, []string{"c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	want := "sleep 10\nsnap 00-boot\nat sys.idle 5000\n== a1\nrequire-env KARIO_TEST_NEVER_SET\nsleep 10\n" +
		"\n\n\n\n== a2\nrequire-previous\nsleep 10\n#part c\n== c1\nrequire-previous\nsleep 10\n"
	if got != want {
		t.Fatalf("selected:\n%s\nwant:\n%s", got, want)
	}
	var out strings.Builder
	r := &Runner{C: snapCore(t), Out: &out}
	r.Run(got)
	if line := caseLine(r.Cases); line != "a1 SKIP, a2 SKIP, c1 PASS" {
		t.Errorf("cases %s\n%s", line, out.String())
	}
	if _, err := SelectParts(partsScript, []string{"a", "d"}); err == nil || !strings.Contains(err.Error(), `no part "d"`) {
		t.Errorf("an unknown part: %v", err)
	}
	if _, err := ScriptParts("sleep 1\n== early\n#part a\n== a1\n"); err == nil {
		t.Error("a case before the first part line was taken")
	}
}

// The compatibility cases are a part of their own after the selftest's.
func TestCTSScriptIsAPart(t *testing.T) {
	parts, err := ScriptParts(SelftestScript + "\n" + CTSScript)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range parts {
		if p == "cts" {
			n++
		}
	}
	if parts[len(parts)-1] != "cts" || n != 1 {
		t.Errorf("parts %v", parts)
	}
}
