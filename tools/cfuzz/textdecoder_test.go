package cfuzz

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// FuzzTextDecoder is what a server sends a TypeScript app that reads text:
// kario-jsrt's TextDecoder (jsrt/src/utf8dec.c) in chunks against the
// harness's own reading of the WHATWG decoder (fuzz_utf8.c), and what it
// decoded against Node.js's TextDecoder in the same chunks (skipped
// without Node.js). The first byte picks fatal, ignoreBOM and the chunks.
func FuzzTextDecoder(f *testing.F) {
	f.Add([]byte("\x00caf\xc3\xa9 \xe2\x82\xac \xf0\x9f\x98\x80"))
	f.Add([]byte("\x04\xef\xbb\xbfcaf\xc3\xa9 \xe2\x82\xac \xf0\x9f\x98\x80"))
	f.Add([]byte("\x06\xef\xbb\xbf\xef\xbb\xbfx"))
	f.Add([]byte("\x08\xe2\x82\x41\xf0\x9f\x98\xed\xa0\x80\xc0\xaf\xf4\x90\x80\x80\xff\xfe"))
	f.Add([]byte("\x0d\xe0\x80\x80\xe2\x82"))
	f.Add([]byte("\x0c\xf0\x9f\x98"))
	f.Add([]byte("\x01ok \xc3\xa9"))
	f.Add([]byte("\x05\xef\xbb"))
	f.Fuzz(func(t *testing.T, data []byte) {
		bin := harness(t, "utf8-decode")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin)
		var stdout, stderr bytes.Buffer
		cmd.Env = []string{"ASAN_OPTIONS=detect_leaks=1"}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(data), &stdout, &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		run(t, cmd, &stderr)
		if want, ok := nodeDecode(t, data); ok && stdout.String() != want {
			t.Fatalf("kario-jsrt's TextDecoder gives %q, Node.js's %q", stdout.String(), want)
		}
	})
}

// nodeDecoder decodes as fuzz_utf8.c prints: T and the text, or E when
// refused, base64 a line each way.
const nodeDecoder = `
const rl = require("readline").createInterface({ input: process.stdin });
rl.on("line", (l) => {
  const b = Buffer.from(l, "base64");
  let out = "";
  if (b.length) {
    const k = b[0] >> 2, data = b.subarray(1);
    const d = new TextDecoder("utf-8", { fatal: !!(b[0] & 1), ignoreBOM: !!(b[0] & 2) });
    try {
      let s = "";
      for (let i = 0; k && i < data.length; i += k) s += d.decode(data.subarray(i, i + k), { stream: true });
      out = "T" + s + (k ? d.decode() : d.decode(data));
    } catch {
      out = "E";
    }
  }
  process.stdout.write(Buffer.from(out).toString("base64") + "\n");
});
`

var node struct {
	once sync.Once
	mu   sync.Mutex
	in   *bufio.Writer
	out  *bufio.Reader
}

// nodeDecode is Node.js's decoding of data, false without Node.js.
func nodeDecode(t *testing.T, data []byte) (string, bool) {
	node.once.Do(func() {
		cmd := exec.Command("node", "-e", nodeDecoder)
		in, err := cmd.StdinPipe()
		if err != nil {
			return
		}
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return
		}
		node.in, node.out = bufio.NewWriter(in), bufio.NewReader(out)
	})
	if node.in == nil {
		return "", false
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	node.in.WriteString(base64.StdEncoding.EncodeToString(data) + "\n")
	if err := node.in.Flush(); err != nil {
		t.Fatalf("Node.js: %v", err)
	}
	line, err := node.out.ReadString('\n')
	if err != nil {
		t.Fatalf("Node.js: %v", err)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("Node.js: %v", err)
	}
	return string(b), true
}
