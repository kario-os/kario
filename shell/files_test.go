package shell

import "testing"

func TestCleanFileName(t *testing.T) {
	for in, want := range map[string]string{
		"\u62a5\u544a.pdf":     "\u62a5\u544a.pdf",
		"../../etc/passwd":     "passwd",
		"a\\b\\c.txt":          "c.txt",
		"  \u7a7a\u683c.txt  ": "\u7a7a\u683c.txt",
	} {
		if got, err := cleanFileName(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", ".", "..", ".hidden", "a\x01b", "/"} {
		if _, err := cleanFileName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "\u957f"
	}
	if got, _ := cleanFileName(long); len([]rune(got)) != maxFileName {
		t.Errorf("long name kept %d characters", len([]rune(got)))
	}
}
