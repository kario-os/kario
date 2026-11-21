package wire

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func dialWait(t *testing.T, path string) *Client {
	t.Helper()
	for i := 0; i < 50; i++ {
		if c, err := Dial(path); err == nil {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no service at %s", path)
	return nil
}

func TestServeFilesTakesRequestFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "files.sock")
	go func() {
		_ = ServeFiles(path, func(method string, _ json.RawMessage, files []*os.File) (any, error) {
			defer closeAll(files)
			if len(files) != 1 {
				return map[string]int{"files": len(files)}, nil
			}
			raw, err := io.ReadAll(files[0])
			return map[string]string{"read": string(raw)}, err
		})
	}()
	plain := filepath.Join(dir, "plain.sock")
	go func() {
		_ = Serve(plain, func(string, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	}()
	src := filepath.Join(dir, "video")
	if err := os.WriteFile(src, []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got struct {
		Read  string `json:"read"`
		Files int    `json:"files"`
	}
	if err := dialWait(t, path).CallWith("read", nil, []*os.File{f}, &got); err != nil || got.Read != "frames" {
		t.Fatalf("ServeFiles read %q, %v", got.Read, err)
	}
	if err := dialWait(t, plain).CallWith("read", nil, []*os.File{f}, nil); err != nil {
		t.Fatalf("a plain service failed a call carrying a file: %v", err)
	}
}
