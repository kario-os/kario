package wire

import (
	"bytes"
	"io"
	"testing"
)

type stream struct{ io.Reader }

func (stream) Write(p []byte) (int, error) { return len(p), nil }
func (stream) Close() error                { return nil }

// FuzzRecv is what a peer writes on a connection (an app to the Shell,
// the Shell or an app to a service): lines of JSON, read one at a time.
func FuzzRecv(f *testing.F) {
	f.Add([]byte(`{"id":1,"method":"list","params":{"folder":"files"},"fds":2}` + "\n" + `{"id":2,"ok":true,"result":[1,2]}` + "\n"))
	f.Add([]byte(`{"t":"push","screen":{"id":"home","title":"Hi","body":[{"kind":"text","text":"x"}]}}` + "\n{\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c := New(stream{bytes.NewReader(data)})
		for i := 0; ; i++ {
			var m RPC
			err := c.Recv(&m)
			if err == io.EOF || i > len(data) {
				break
			}
			if err == nil && m.FDs > 0 && len(c.TakeFiles(m.FDs)) != 0 {
				t.Fatal("files taken from a stream that carries none")
			}
		}
	})
}
