package jsrt

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A store app's bytecode sits outside its package (the Shell's cache) and
// is named with --bytecode: it runs instead of the module beside it;
// bytecode of another engine falls back to the source; --no-bytecode
// wins. --compile IN - writes what --compile IN OUT writes, to stdout.
func TestBytecodeFile(t *testing.T) {
	bin := binary(t)
	dir := appCopy(t, "app")
	main := filepath.Join(dir, "main.js")
	var bc, stderr bytes.Buffer
	cmd := exec.Command(bin, "--compile", main, "-")
	cmd.Stdout, cmd.Stderr = &bc, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compile: %v %s", err, stderr.String())
	}
	if out, err := exec.Command(bin, "--compile", main, main+"c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v %s", err, out)
	}
	beside, _ := os.ReadFile(main + "c")
	if !bytes.Equal(bc.Bytes(), beside) || !bytes.HasPrefix(beside, []byte("KJSC")) {
		t.Fatalf("stdout %d bytes, file %d bytes", bc.Len(), len(beside))
	}
	_ = os.Remove(main + "c")
	cache := filepath.Join(t.TempDir(), "main.jsc")
	if err := os.WriteFile(cache, bc.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	source, _ := os.ReadFile(main)
	if err := os.WriteFile(main, []byte("this is not JavaScript ("), 0o644); err != nil {
		t.Fatal(err)
	}
	s := launch(t, t.TempDir(), "zh-Hans", bin, "--bytecode", cache, main)
	s.hello("", nil)
	s.screenText("Jishu 0")
	s.send(action("timer"))
	s.screenText("timer fired")

	s = launch(t, t.TempDir(), "zh-Hans", bin, "--no-bytecode", "--bytecode", cache, main)
	if code := s.exit(); code != 1 || !strings.Contains(s.log(), "SyntaxError") {
		t.Fatalf("--no-bytecode ran the bytecode: exit %d; log:\n%s", code, s.log())
	}

	if err := os.WriteFile(main, source, 0o644); err != nil {
		t.Fatal(err)
	}
	other := bytes.Clone(bc.Bytes())
	other[5] ^= 1
	_ = os.WriteFile(cache, other, 0o644)
	s = launch(t, t.TempDir(), "zh-Hans", bin, "--bytecode", cache, main)
	s.hello("", nil)
	s.screenText("Jishu 0")
	if !s.logHas("running the source") {
		t.Fatalf("log:\n%s", s.log())
	}
	s = launch(t, t.TempDir(), "zh-Hans", bin, "--bytecode", filepath.Join(t.TempDir(), "none.jsc"), main)
	s.hello("", nil)
	s.screenText("Jishu 0")
}
