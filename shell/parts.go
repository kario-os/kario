package shell

import (
	"fmt"
	"slices"
	"strings"
)

// ScriptParts are the names of a script's parts (its "#part <name>"
// lines), in the order they first appear; a case above the first such line
// is refused.
func ScriptParts(script string) ([]string, error) {
	var names []string
	seen := false
	for i, l := range strings.Split(script, "\n") {
		t := strings.TrimSpace(l)
		if name, ok := strings.CutPrefix(t, partMark); ok {
			seen = true
			if name = strings.TrimSpace(name); !slices.Contains(names, name) {
				names = append(names, name)
			}
			continue
		}
		if strings.HasPrefix(t, "== ") && !seen {
			return nil, fmt.Errorf("line %d: a case before the first %q line", i+1, strings.TrimSpace(partMark))
		}
	}
	return names, nil
}

// SelectParts is the script with the cases of the named parts only, each
// at its line number: the lines before the first part (the boot) and those
// of the named parts stay, the others are blank. The first named part
// begins at the idle screen the boot leaves; where the script goes from one
// named part to another its "#part" line stays, so a case goes on from the
// cases above it in its own part only (require-previous), as in the whole
// script.
func SelectParts(script string, names []string) (string, error) {
	all, err := ScriptParts(script)
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if !slices.Contains(all, n) {
			return "", fmt.Errorf("the script has no part %q (it has %s)", n, strings.Join(all, ", "))
		}
	}
	lines := strings.Split(script, "\n")
	out := make([]string, len(lines))
	cur, last, begun := "", "", false
	for i, l := range lines {
		name, mark := strings.CutPrefix(strings.TrimSpace(l), partMark)
		if !mark {
			if !begun || slices.Contains(names, cur) {
				out[i] = l
			}
			continue
		}
		begun, cur = true, strings.TrimSpace(name)
		if !slices.Contains(names, cur) {
			continue
		}
		switch last {
		case "":
			out[i] = "at sys.idle 5000"
		case cur:
		default:
			out[i] = l
		}
		last = cur
	}
	return strings.Join(out, "\n"), nil
}
