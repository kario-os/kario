package mediad

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// A photo of the synthetic scene is what it shows when taken, unless a
// selftest comparing snapshots asks for the same one every time.
func TestFixedStills(t *testing.T) {
	s := scanServer(t, "")
	photo := func() []byte {
		t.Helper()
		res, err := s.do(t, svc.MediaCameraOpen, map[string]string{"app": "com.phone.camera"})
		if err != nil {
			t.Fatal(err)
		}
		res, err = s.do(t, svc.MediaCameraCapture, map[string]string{"cam_id": res.(map[string]string)["cam_id"]})
		if err != nil {
			t.Fatal(err)
		}
		f := res.(wire.Files).Files[0]
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil || len(b) == 0 {
			t.Fatalf("photo: %d bytes, %v", len(b), err)
		}
		return b
	}
	a := photo()
	s.started = s.started.Add(-2500 * time.Millisecond)
	if bytes.Equal(a, photo()) {
		t.Fatal("photos taken seconds apart are the same")
	}
	fixedStills = true
	defer func() { fixedStills = false }()
	a = photo()
	s.started = s.started.Add(-2500 * time.Millisecond)
	if !bytes.Equal(a, photo()) {
		t.Fatal("with --test-stills the photos differ")
	}
}
