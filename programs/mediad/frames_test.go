package mediad

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// A subscription line that never ends is refused once it is longer than
// any request, rather than read on into memory.
func TestFramesRequestBounded(t *testing.T) {
	s := scanServer(t, "")
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.stream(server)
	}()
	go func() {
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for i := 0; i < 64; i++ {
			if _, err := client.Write(chunk); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("4 MB without a newline: the frames socket still reads")
	}
}
