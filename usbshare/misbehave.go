package usbshare

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"

	"github.com/kario-os/kario/wire"
)

// Misbehave plays a helper that tries what the Shell's end must refuse,
// against that end on conn: requests before hello, folders that do not
// exist, names leaving their folder, copies over the limit or not what
// the helper claims, descriptors sent to the Shell. folder is a writable
// folder that takes any file (the Shell's "files"). It returns what got
// through, nil when everything was refused. For tests: this package's and
// the selftest's (usb-helper refuses).
func Misbehave(conn *net.UnixConn, folder string) error {
	p := newPeer(conn, func(string, json.RawMessage) (any, []*os.File, error) { return nil, nil, errors.New("no") })
	go p.run()
	defer p.c.Close()
	ask := func(method string, a, out any) error {
		files, err := p.call(method, a, out, callWait)
		closeFiles(files)
		return err
	}
	if ask(msgList, args{Folder: folder}, nil) == nil {
		return errors.New("the Shell answered a request before hello")
	}
	var w welcome
	if err := ask(msgHello, Hello{}, &w); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	if ask(msgHello, Hello{}, &w) == nil {
		return errors.New("the Shell took a second hello")
	}
	var fl folderList
	if err := ask(msgFolders, nil, &fl); err != nil {
		return fmt.Errorf("folders: %w", err)
	}
	var flat, nested Folder
	for _, f := range fl.Folders {
		if f.ID == folder {
			flat = f
		}
		if f.Nested && f.Writable && nested.ID == "" {
			nested = f
		}
	}
	if flat.ID == "" || !flat.Writable || flat.Nested {
		return fmt.Errorf("no writable flat folder %q", folder)
	}
	refused := []struct {
		what, method string
		a            args
	}{
		{"a folder that does not exist", msgList, args{Folder: "secrets"}},
		{"a file outside its folder", msgOpen, args{Folder: flat.ID, Name: "../../shell/settings.json"}},
		{"a folder inside a flat folder", msgOpen, args{Folder: flat.ID, Name: "a/b"}},
		{"a name with a control character", msgSave, args{Folder: flat.ID, Name: "a\x01b.txt"}},
		{"a name with ..", msgSave, args{Folder: flat.ID, Name: ".."}},
		{"a path with ..", msgSave, args{Folder: flat.ID, Name: "../evil.txt"}},
		{"a Windows path leaving its folder", msgSave, args{Folder: flat.ID, Name: `..\evil.txt`}},
		{"an empty name", msgSave, args{Folder: flat.ID}},
		{"a copy into a folder that does not exist", msgSave, args{Folder: "nowhere", Name: "x.txt"}},
		{"a folder made in a flat folder", msgMakeDir, args{Folder: flat.ID, Name: "dir"}},
		{"the end of a copy that never started", msgEnd, args{ID: 1 << 30, Size: 1, Complete: true}},
	}
	if nested.ID != "" {
		refused = append(refused, []struct {
			what, method string
			a            args
		}{
			{"a folder made outside the nested folder", msgMakeDir, args{Folder: nested.ID, Name: "a/../../b"}},
			{"a file outside the nested folder", msgOpen, args{Folder: nested.ID, Name: "../../etc/passwd"}},
			{"a copy outside the nested folder", msgSave, args{Folder: nested.ID, Name: "a/../../evil.lrc"}},
		}...)
	}
	for _, r := range refused {
		if ask(r.method, r.a, nil) == nil {
			return fmt.Errorf("the Shell took %s (%s %+v)", r.what, r.method, r.a)
		}
	}
	for _, d := range []args{{Folder: nested.ID, Name: ".."}, {Folder: flat.ID, Name: "x"}} {
		var dl dirList
		if d.Folder != "" && ask(msgListDir, d, &dl) == nil && dl.OK {
			return fmt.Errorf("the Shell listed %+v", d)
		}
	}
	copies := []struct {
		what, name string
		send, size int64
		complete   bool
	}{
		{"a copy over the folder's limit", "over-the-limit.bin", flat.limit() + 1, flat.limit() + 1, true},
		{"a copy shorter than the helper says", "shorter.txt", 5, 10, true},
		{"a copy the helper says is incomplete", "incomplete.txt", 5, 5, false},
	}
	for _, c := range copies {
		var st args
		files, err := p.call(msgSave, args{Folder: flat.ID, Name: c.name}, &st, callWait)
		if err != nil || len(files) != 1 {
			closeFiles(files)
			return fmt.Errorf("the Shell did not start a copy of %s: %v", c.name, err)
		}
		pw := files[0]
		copied := make(chan struct{})
		go func() {
			defer close(copied)
			_, _ = io.Copy(pw, io.LimitReader(zeros{}, c.send))
			pw.Close()
		}()
		<-copied
		if _, err := p.call(msgEnd, args{ID: st.ID, Size: c.size, Complete: c.complete}, nil, 0); err == nil {
			return fmt.Errorf("the Shell kept %s", c.what)
		}
	}
	var items []Item
	if err := ask(msgList, args{Folder: flat.ID}, &items); err != nil {
		return fmt.Errorf("list: %w", err)
	}
	for _, it := range items {
		for _, c := range copies {
			if strings.HasPrefix(it.Name, strings.TrimSuffix(c.name, path.Ext(c.name))) {
				return fmt.Errorf("%s is in the folder after %s", it.Name, c.what)
			}
		}
	}
	return refusesFiles(p, ask)
}

// refusesFiles sends the Shell the read end of a pipe with a message that
// does not count it: refused, it is closed as it arrives (not queued for
// a later message), and the write end breaks.
func refusesFiles(p *peer, ask func(string, any, any) error) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer w.Close()
	raw, _ := json.Marshal(args{Line: "usb sharing: a descriptor for the Shell"})
	err = p.c.SendFiles(wire.RPC{Method: msgLog, Params: raw}, r)
	r.Close()
	if err != nil {
		return err
	}
	// Answered in order: the descriptor was read (and closed) before.
	if err := ask(msgFolders, nil, nil); err != nil {
		return err
	}
	if _, err := w.Write([]byte("x")); err == nil {
		return errors.New("the Shell kept a descriptor the helper sent")
	}
	return nil
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
