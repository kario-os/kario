package mtp

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/usbshare"
)

// memLib is the Shell's library for the tests, with its rules: Photos
// takes JPEG and PNG up to 4 KB, Music keeps folders, Files takes
// anything up to 1 MB; a name taken becomes "name (2)"; Open hands out
// a read-only file, as the Shell does.
type memLib struct {
	dir   string
	mu    sync.Mutex
	files map[string][]byte // "folder/path"
	dirs  map[string]bool   // "music/Album"
}

func newLib(t *testing.T) *memLib {
	return &memLib{dir: t.TempDir(), files: map[string][]byte{
		"photos/beach.jpg":    bytes.Repeat([]byte("J"), 1000),
		"photos/exact.jpg":    bytes.Repeat([]byte("E"), Packet-headerLen),
		"music/Album/01.flac": []byte("flac"),
		"files/notes.txt":     []byte("0123456789abcdef"),
		"files/big.bin":       bytes.Repeat([]byte("0123456789"), 30000),
	}, dirs: map[string]bool{"music/Album": true}}
}

func (m *memLib) Folders() []usbshare.Folder {
	return []usbshare.Folder{
		{ID: "photos", Name: "Photos", Path: "Photos", Writable: true, MaxBytes: 4096, Accepts: []string{".jpg", ".jpeg", ".png"}, Capacity: 1 << 30, Free: 1 << 29},
		{ID: "videos", Name: "Videos", Path: "Videos", Writable: true, MaxBytes: 4 << 30, Accepts: []string{".webm"}, Capacity: 1 << 30, Free: 1 << 29},
		{ID: "music", Name: "Music", Path: "Music", Writable: true, Nested: true, MaxBytes: 9 << 30, Capacity: 1 << 30, Free: 1 << 29},
		{ID: "files", Name: "Files", Path: "Files", Writable: true, MaxBytes: 1 << 20, Capacity: 1 << 30, Free: 1 << 29},
	}
}

func (m *memLib) list(folder, dir string) []usbshare.Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := folder + "/"
	if dir != "" {
		prefix += dir + "/"
	}
	var out []usbshare.Item
	seen := map[string]bool{}
	for k, b := range m.files {
		if rest, ok := strings.CutPrefix(k, prefix); ok && !strings.Contains(rest, "/") {
			out = append(out, usbshare.Item{Name: rest, Size: int64(len(b)), ModTime: time.Unix(1700000000, 0)})
		}
	}
	for k := range m.dirs {
		if rest, ok := strings.CutPrefix(k, prefix); ok && !strings.Contains(rest, "/") && !seen[rest] {
			seen[rest] = true
			out = append(out, usbshare.Item{Name: rest, Dir: true, ModTime: time.Unix(1700000000, 0)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *memLib) List(folder string) []usbshare.Item { return m.list(folder, "") }

func (m *memLib) ListDir(folder, dir string) ([]usbshare.Item, bool) {
	m.mu.Lock()
	ok := folder == "music" && m.dirs[folder+"/"+dir]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return m.list(folder, dir), true
}

func (m *memLib) MakeDir(folder, dir string) error {
	if folder != "music" {
		return errors.New("read-only")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[folder+"/"+dir] = true
	return nil
}

func (m *memLib) Open(folder, name string) (io.ReadSeekCloser, usbshare.Item, error) {
	m.mu.Lock()
	b, ok := m.files[folder+"/"+name]
	m.mu.Unlock()
	if !ok {
		return nil, usbshare.Item{}, os.ErrNotExist
	}
	f, err := os.CreateTemp(m.dir, "open-*")
	if err != nil {
		return nil, usbshare.Item{}, err
	}
	f.Write(b)
	f.Close()
	ro, err := os.Open(f.Name())
	return ro, usbshare.Item{Name: path.Base(name), Size: int64(len(b))}, err
}

func (m *memLib) Save(folder, name string, r io.Reader) (string, error) {
	var limit int64
	for _, f := range m.Folders() {
		if f.ID == folder {
			limit = f.MaxBytes
		}
	}
	ext := strings.ToLower(path.Ext(name))
	if folder == "photos" && ext != ".jpg" && ext != ".png" {
		return "", errors.New("Photos takes JPEG and PNG pictures")
	}
	if folder == "music" && ext != ".flac" && ext != ".lrc" && ext != ".m3u8" {
		return "", errors.New("Music takes music files")
	}
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", usbshare.ErrTooLarge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := path.Dir(name); d != "." && !m.dirs[folder+"/"+d] {
		m.dirs[folder+"/"+d] = true
	}
	got := name
	stem := strings.TrimSuffix(name, path.Ext(name))
	for i := 2; m.files[folder+"/"+got] != nil; i++ {
		got = fmt.Sprintf("%s (%d)%s", stem, i, path.Ext(name))
	}
	m.files[folder+"/"+got] = b
	return got, nil
}

func (m *memLib) get(k string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[k]
	return b, ok
}

func (m *memLib) put(k string, b []byte) {
	m.mu.Lock()
	m.files[k] = b
	m.mu.Unlock()
}

func (m *memLib) remove(k string) {
	m.mu.Lock()
	delete(m.files, k)
	m.mu.Unlock()
}

// connect serves lib on a simulated cable and opens a session from the
// computer's side.
func connect(t *testing.T, lib usbshare.Library, poll time.Duration) *Initiator {
	t.Helper()
	dev, host, err := MemSim(Packet)
	if err != nil {
		t.Fatal(err)
	}
	r := NewResponder(Config{Lib: lib, Info: DeviceInfo{Manufacturer: "Kario", Model: "Kario phone", Version: "0.6", Serial: "KARIO0000001"},
		Poll: poll, Log: t.Logf})
	done := make(chan error, 1)
	go func() { done <- r.Serve(dev) }()
	i := NewInitiator(host)
	t.Cleanup(func() {
		i.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the responder outlived the cable")
		}
	})
	if err := i.OpenSession(); err != nil {
		t.Fatal(err)
	}
	return i
}

func mustFind(t *testing.T, i *Initiator, p string) Found {
	t.Helper()
	f, err := i.Find(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func wantCode(t *testing.T, what string, err error, code uint16) {
	t.Helper()
	if Code(err) != code {
		t.Errorf("%s: %v, want %s", what, err, RespName(code))
	}
}

func TestBeforeTheSession(t *testing.T) {
	dev, host, err := MemSim(Packet)
	if err != nil {
		t.Fatal(err)
	}
	go NewResponder(Config{Lib: newLib(t), Info: DeviceInfo{Model: "Kario phone"}}).Serve(dev)
	i := NewInitiator(host)
	defer i.Close()
	di, err := i.DeviceInfo()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(di.VendorExtension, "microsoft.com: 1.0") || di.Model != "Kario phone" {
		t.Errorf("device info: %+v", di)
	}
	_, err = i.StorageIDs()
	wantCode(t, "storages before OpenSession", err, RespSessionNotOpen)
	if err := i.OpenSession(); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "a second OpenSession", i.OpenSession(), RespSessionAlreadyOpen)
}

// Windows reads the device the way Explorer shows it: the operations and
// properties it may use, each storage and its space, then each folder's
// contents with every property in one GetObjectPropList (depth 1).
func TestWindowsPropertyListFlow(t *testing.T) {
	i := connect(t, newLib(t), 0)
	di, err := i.DeviceInfo()
	if err != nil {
		t.Fatal(err)
	}
	has := map[uint16]bool{}
	for _, op := range di.Operations {
		has[op] = true
	}
	for _, op := range []uint16{OpGetObjectPropList, OpGetObjectPropsSupported, OpGetObjectPropDesc, OpGetPartialObject64, OpSendObjectInfo, OpDeleteObject} {
		if !has[op] {
			t.Errorf("DeviceInfo lacks 0x%04x", op)
		}
	}
	if name, err := i.FriendlyName(); err != nil || name != "Kario phone" {
		t.Errorf("friendly name %q, %v", name, err)
	}
	ids, err := i.StorageIDs()
	if err != nil || len(ids) != 4 {
		t.Fatalf("storages %v, %v", ids, err)
	}
	var names []string
	for _, id := range ids {
		si, err := i.StorageInfo(id)
		if err != nil {
			t.Fatal(err)
		}
		if si.Access != 0 || si.Free != 1<<29 || si.Capacity != 1<<30 {
			t.Errorf("storage %s: %+v", si.Description, si)
		}
		names = append(names, si.Description)
	}
	if strings.Join(names, ",") != "Photos,Videos,Music,Files" {
		t.Errorf("storages %v", names)
	}
	codes, err := i.PropsSupported(FormatUndefined)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range codes {
		pd, err := i.PropDesc(c, FormatUndefined)
		if err != nil || pd.Code != c || pd.Settable != (c == PropObjectFileName) {
			t.Errorf("property 0x%04x: %+v, %v", c, pd, err)
		}
	}
	music := mustFind(t, i, "/Music")
	root, err := i.Handles(music.Storage, 0, rootParent)
	if err != nil {
		t.Fatal(err)
	}
	byHandle := map[uint32]map[uint16]any{}
	for _, h := range root {
		list, err := i.PropList(h, 0, 0xFFFFFFFF, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range list {
			if byHandle[e.Handle] == nil {
				byHandle[e.Handle] = map[uint16]any{}
			}
			byHandle[e.Handle][e.Code] = e.Value
		}
	}
	if roots, err := i.PropList(0, 0, PropObjectFileName, 0, 1); err != nil || len(roots) != 5 {
		t.Errorf("the names in every storage's root: %v, %v", roots, err)
	}
	if all, err := i.PropList(allObjects, 0, PropObjectFileName, 0, 0); err != nil || len(all) != 6 {
		t.Errorf("the names of everything: %v, %v", all, err)
	}
	if len(byHandle) != 1 {
		t.Fatalf("Music's root: %v", byHandle)
	}
	for h, p := range byHandle {
		if p[PropObjectFileName] != "Album" || p[PropObjectFormat] != uint64(FormatAssociation) || p[PropParentObject] != uint64(0) ||
			p[PropStorageID] != uint64(music.Storage) || len(p[PropPersistentUID].([]byte)) != 16 || p[PropName] != "Album" {
			t.Errorf("Album %d: %v", h, p)
		}
		inner, err := i.PropList(h, 0, 0xFFFFFFFF, 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		got := map[uint16]any{}
		self := false
		for _, e := range inner {
			if e.Handle == h {
				self = true
				continue
			}
			got[e.Code] = e.Value
		}
		if !self {
			t.Error("depth 1 left the folder itself out")
		}
		if got[PropObjectFileName] != "01.flac" || got[PropObjectSize] != uint64(4) || got[PropParentObject] != uint64(h) ||
			got[PropObjectFormat] != uint64(FormatFLAC) || got[PropProtectionStatus] != uint64(1) {
			t.Errorf("Album's contents: %v", got)
		}
	}
	photos := mustFind(t, i, "/Photos/beach.jpg")
	one, err := i.PropList(photos.Handle, 0, PropObjectSize, 0, 0)
	if err != nil || len(one) != 1 || one[0].Value != uint64(1000) {
		t.Errorf("one property of one object: %v, %v", one, err)
	}
	if photos.Info.Format != FormatJPEG || photos.Info.CompressedSize != 1000 || photos.Info.Protection != 1 {
		t.Errorf("object info: %+v", photos.Info)
	}
	_, err = i.PropList(photos.Handle, 0, 0, 1, 0)
	wantCode(t, "a property group", err, RespGroupUnsupported)
	_, err = i.PropList(photos.Handle, 0, 0, 0, 0)
	wantCode(t, "no property", err, RespParameterNotSupported)
	if all, err := i.PropList(0, 0, PropObjectSize, 0, 0xFFFFFFFF); err != nil || len(all) != 6 {
		t.Errorf("everything at any depth: %v, %v", all, err)
	}
	album := mustFind(t, i, "/Music/Album")
	_, err = i.PropList(album.Handle, 0, 0xFFFFFFFF, 0, 2)
	wantCode(t, "depth 2", err, RespDepthUnsupported)
	all, err := i.Handles(music.Storage, 0, 0)
	if err != nil || len(all) != 2 {
		t.Errorf("everything in Music: %v, %v", all, err)
	}
	dirs, err := i.Handles(allStores, FormatAssociation, 0)
	if err != nil || len(dirs) != 1 {
		t.Errorf("the folders of every storage: %v, %v", dirs, err)
	}
}

func TestDownloads(t *testing.T) {
	lib := newLib(t)
	i := connect(t, lib, 0)
	for _, p := range []string{"files/big.bin", "photos/exact.jpg", "files/notes.txt"} {
		folder, name, _ := strings.Cut(p, "/")
		want, _ := lib.get(p)
		f := mustFind(t, i, "/"+strings.ToUpper(folder[:1])+folder[1:]+"/"+name)
		var got bytes.Buffer
		if err := i.Get(f.Handle, &got); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: %d bytes, want %d", p, got.Len(), len(want))
		}
	}
	notes := mustFind(t, i, "/Files/notes.txt")
	for _, c := range []struct {
		off  int64
		n    uint32
		wide bool
		want string
	}{{4, 4, false, "4567"}, {10, 100, true, "abcdef"}, {0, 3, true, "012"}, {16, 4, false, ""}} {
		b, err := i.Partial(notes.Handle, c.off, c.n, c.wide)
		if err != nil || string(b) != c.want {
			t.Errorf("%d bytes at %d (64: %v): %q, %v", c.n, c.off, c.wide, b, err)
		}
	}
	_, err := i.Partial(notes.Handle, 17, 4, true)
	wantCode(t, "past the end", err, RespInvalidParameter)
	big := mustFind(t, i, "/Files/big.bin")
	b, err := i.Partial(big.Handle, 200000, 70000, true)
	want, _ := lib.get("files/big.bin")
	if err != nil || !bytes.Equal(b, want[200000:270000]) {
		t.Errorf("a piece of a big file: %d bytes, %v", len(b), err)
	}
}

func TestUploads(t *testing.T) {
	lib := newLib(t)
	i := connect(t, lib, 0)
	files := mustFind(t, i, "/Files")
	h, err := i.Put(files.Storage, rootParent, "from-pc.txt", []byte("hello over MTP"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("files/from-pc.txt"); string(b) != "hello over MTP" {
		t.Errorf("stored %q", b)
	}
	if ev, err := i.WaitEvent(EventStorageInfoChanged, 2*time.Second); err != nil || ev.Params[0] != files.Storage {
		t.Errorf("storage event: %+v, %v", ev, err)
	}
	// A name taken: the phone keeps both, and says so.
	h2, err := i.Put(files.Storage, rootParent, "from-pc.txt", []byte("again"))
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := i.WaitEvent(EventObjectInfoChanged, 2*time.Second); err != nil || ev.Params[0] != h2 {
		t.Errorf("rename event: %+v, %v", ev, err)
	}
	oi, err := i.ObjectInfo(h2)
	if err != nil || oi.Filename != "from-pc (2).txt" || oi.Protection != 1 {
		t.Errorf("the second copy: %+v, %v", oi, err)
	}
	if b, _ := lib.get("files/from-pc.txt"); string(b) != "hello over MTP" || h == h2 {
		t.Errorf("the first copy was overwritten: %q", b)
	}
	// Sizes the packets end on exactly, from the computer.
	for _, n := range []int{Packet - headerLen, 3*chunk - headerLen, 200000} {
		data := bytes.Repeat([]byte{byte(n)}, n)
		name := fmt.Sprintf("size-%d.bin", n)
		if _, err := i.Put(files.Storage, rootParent, name, data); err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if b, _ := lib.get("files/" + name); !bytes.Equal(b, data) {
			t.Errorf("%d bytes stored as %d", n, len(b))
		}
	}
	names, _ := i.Names(files)
	if !strings.Contains(strings.Join(names, "|"), "from-pc (2).txt") {
		t.Errorf("Files lists %v", names)
	}
	// Folders, only in Music.
	music := mustFind(t, i, "/Music")
	album, err := i.MakeDir(music.Storage, rootParent, "From the PC")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Put(music.Storage, album, "lyrics.lrc", []byte("[00:00.00]la")); err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("music/From the PC/lyrics.lrc"); string(b) != "[00:00.00]la" {
		t.Errorf("music stored %q", b)
	}
	if f := mustFind(t, i, "/Music/From the PC/lyrics.lrc"); f.Info.Parent != album {
		t.Errorf("parent %d, want %d", f.Info.Parent, album)
	}
	_, err = i.MakeDir(files.Storage, rootParent, "A folder")
	wantCode(t, "a folder in Files", err, RespAccessDenied)
	// A file the computer announced and then did not send is only
	// forgotten; it may be named again before it is sent.
	p, err := i.SendObjectInfo(files.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 3, Filename: "draft.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := i.SetName(p, "final.txt"); err != nil {
		t.Errorf("naming a file not sent yet: %v", err)
	}
	if err := i.SendObject(strings.NewReader("fin"), 3); err != nil {
		t.Fatal(err)
	}
	if b, ok := lib.get("files/final.txt"); !ok || string(b) != "fin" {
		t.Errorf("final.txt: %q", b)
	}
	p, _ = i.SendObjectInfo(files.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 3, Filename: "never.txt"})
	if err := i.Delete(p); err != nil {
		t.Errorf("deleting a file not sent: %v", err)
	}
	wantCode(t, "SendObject after its info was deleted", i.SendObject(strings.NewReader("abc"), 3), RespNoValidObjectInfo)
	// A copy on the phone, into a folder that takes it.
	photo := mustFind(t, i, "/Photos/beach.jpg")
	c, err := i.Copy(photo.Handle, files.Storage, rootParent)
	if err != nil {
		t.Fatal(err)
	}
	if oi, _ := i.ObjectInfo(c); oi.Filename != "beach.jpg" || oi.StorageID != files.Storage {
		t.Errorf("the copy: %+v", oi)
	}
	if b, _ := lib.get("files/beach.jpg"); len(b) != 1000 {
		t.Errorf("copied %d bytes", len(b))
	}
}

// What the WebDAV sharing refuses, MTP refuses: nothing on the phone is
// deleted, renamed, moved or overwritten; each folder takes only its
// kinds of file up to its size; and nothing cut short is kept.
func TestRefusals(t *testing.T) {
	lib := newLib(t)
	i := connect(t, lib, 0)
	notes := mustFind(t, i, "/Files/notes.txt")
	photos := mustFind(t, i, "/Photos")
	music := mustFind(t, i, "/Music")
	wantCode(t, "delete", i.Delete(notes.Handle), RespObjectWriteProtected)
	wantCode(t, "delete everything", i.Delete(allObjects), RespObjectWriteProtected)
	wantCode(t, "rename", i.SetName(notes.Handle, "renamed.txt"), RespObjectWriteProtected)
	wantCode(t, "move", i.Move(notes.Handle, photos.Storage, rootParent), RespObjectWriteProtected)
	album := mustFind(t, i, "/Music/Album")
	wantCode(t, "delete a folder", i.Delete(album.Handle), RespObjectWriteProtected)
	if b, ok := lib.get("files/notes.txt"); !ok || string(b) != "0123456789abcdef" {
		t.Errorf("notes.txt is now %q", b)
	}
	if _, ok := lib.get("files/renamed.txt"); ok {
		t.Error("renamed")
	}
	_, err := i.Put(photos.Storage, rootParent, "notes.txt", []byte("text"))
	wantCode(t, "a text file into Photos", err, RespInvalidObjectFormatCode)
	_, err = i.Put(photos.Storage, rootParent, "huge.jpg", make([]byte, 5000))
	wantCode(t, "a photo over the limit", err, RespObjectTooLarge)
	_, err = i.Copy(notes.Handle, photos.Storage, rootParent)
	wantCode(t, "a text file copied into Photos", err, RespInvalidObjectFormatCode)
	// Announced small, sent large: the library's own limit stops it.
	if _, err := i.SendObjectInfo(photos.Storage, rootParent, ObjectInfo{Format: FormatJPEG, CompressedSize: 10, Filename: "liar.jpg"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "more than announced", i.SendObject(bytes.NewReader(make([]byte, 5000)), 5000), RespIncompleteTransfer)
	if _, ok := lib.get("photos/liar.jpg"); ok {
		t.Error("a photo larger than announced was kept")
	}
	// The library refuses the kind after the bytes came (Music checks it
	// itself); the stream stays in step for the next operation.
	_, err = i.Put(music.Storage, rootParent, "virus.exe", []byte("MZ"))
	wantCode(t, "a program into Music", err, RespGeneralError)
	if _, ok := lib.get("music/virus.exe"); ok {
		t.Error("Music kept a program")
	}
	_, err = i.Put(photos.Storage, album.Handle, "x.jpg", []byte("J"))
	wantCode(t, "a parent in another storage", err, RespInvalidParentObject)
	_, err = i.Put(photos.Storage, rootParent, "../escape.jpg", []byte("J"))
	wantCode(t, "a name with a path", err, RespInvalidDataset)
	_, err = i.Put(0x00090001, rootParent, "x.txt", []byte("x"))
	wantCode(t, "no such storage", err, RespInvalidStorageID)
	if got, err := i.StorageIDs(); err != nil || len(got) != 4 {
		t.Errorf("still in step: %v, %v", got, err)
	}
	if err := i.CloseSession(); err != nil {
		t.Fatal(err)
	}
	_, err = i.Handles(allStores, 0, rootParent)
	wantCode(t, "after CloseSession", err, RespSessionNotOpen)
}

// roLib offers Music read-only, as the WebDAV page shows a folder that
// is not Writable without its upload form.
type roLib struct{ *memLib }

func (l roLib) Folders() []usbshare.Folder {
	fs := l.memLib.Folders()
	fs[2].Writable = false
	return fs
}

// A read-only folder is a read-only storage: the computer reads it and
// puts nothing into it, neither a file, a folder nor a copy.
func TestReadOnlyFolder(t *testing.T) {
	lib := roLib{newLib(t)}
	i := connect(t, lib, 0)
	music, files := mustFind(t, i, "/Music"), mustFind(t, i, "/Files")
	if si, err := i.StorageInfo(music.Storage); err != nil || si.Access != 1 {
		t.Errorf("Music: %+v, %v", si, err)
	}
	if si, err := i.StorageInfo(files.Storage); err != nil || si.Access != 0 {
		t.Errorf("Files: %+v, %v", si, err)
	}
	flac := mustFind(t, i, "/Music/Album/01.flac")
	var got bytes.Buffer
	if err := i.Get(flac.Handle, &got); err != nil || got.String() != "flac" {
		t.Errorf("reading Music: %q, %v", got.String(), err)
	}
	album := mustFind(t, i, "/Music/Album")
	_, err := i.Put(music.Storage, rootParent, "new.flac", []byte("flac"))
	wantCode(t, "a file", err, RespStoreReadOnly)
	_, err = i.Put(music.Storage, album.Handle, "lyrics.lrc", []byte("[00:00.00]"))
	wantCode(t, "a file in a folder", err, RespStoreReadOnly)
	_, err = i.MakeDir(music.Storage, rootParent, "New album")
	wantCode(t, "a folder", err, RespStoreReadOnly)
	notes := mustFind(t, i, "/Files/notes.txt")
	_, err = i.Copy(notes.Handle, music.Storage, rootParent)
	wantCode(t, "a copy", err, RespStoreReadOnly)
	wantCode(t, "delete", i.Delete(flac.Handle), RespObjectWriteProtected)
	if names, _ := i.Names(music); strings.Join(names, ",") != "Album" {
		t.Errorf("Music is now %v", names)
	}
	if _, err := i.Put(files.Storage, rootParent, "still.txt", []byte("ok")); err != nil {
		t.Errorf("Files, writable: %v", err)
	}
}

// A file the computer sends shorter than it announced is not kept.
func TestShortUpload(t *testing.T) {
	lib := newLib(t)
	i := connect(t, lib, 0)
	files := mustFind(t, i, "/Files")
	if _, err := i.SendObjectInfo(files.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 100, Filename: "short.txt"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "less than announced", i.SendObject(strings.NewReader("only this"), 9), RespIncompleteTransfer)
	if _, ok := lib.get("files/short.txt"); ok {
		t.Error("kept a file cut short")
	}
}

// A file of 4 GiB or more is announced with 0xFFFFFFFF and its data
// container ends with the transfer, not at a length.
func TestUnknownLength(t *testing.T) {
	lib := newLib(t)
	i := connect(t, lib, 0)
	music := mustFind(t, i, "/Music")
	if _, err := i.SendObjectInfo(music.Storage, rootParent, ObjectInfo{Format: FormatFLAC, CompressedSize: unknownLength, Filename: "huge.flac"}); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("F"), 3*Packet+100)
	if code := sendUnsized(t, i, body); code != RespOK {
		t.Fatalf("response %s", RespName(code))
	}
	if b, _ := lib.get("music/huge.flac"); !bytes.Equal(b, body) {
		t.Errorf("stored %d bytes, want %d", len(b), len(body))
	}
}

// sendUnsized is SendObject with a data container whose length is
// 0xFFFFFFFF, as for a file of 4 GiB or more; it returns the response.
func sendUnsized(t *testing.T, i *Initiator, body []byte) uint16 {
	t.Helper()
	i.tid++
	tid := i.tid
	if err := i.small(container{typ: typeCommand, code: OpSendObject, tid: tid}); err != nil {
		t.Fatal(err)
	}
	hdr := make([]byte, headerLen)
	binary.LittleEndian.PutUint32(hdr, unknownLength)
	binary.LittleEndian.PutUint16(hdr[4:], typeData)
	binary.LittleEndian.PutUint16(hdr[6:], OpSendObject)
	binary.LittleEndian.PutUint32(hdr[8:], tid)
	w := &packetWriter{w: i.t.Out, packet: Packet}
	w.Write(append(hdr, body...))
	w.Close()
	h, err := i.rd.header()
	if err != nil || h.typ != typeResponse {
		t.Fatalf("response %+v, %v", h, err)
	}
	i.rd.payload(h).drain()
	return h.code
}

// What changes on the phone while the computer looks reaches it as
// events: a photo taken, a file deleted.
func TestEvents(t *testing.T) {
	lib := newLib(t)
	i := connect(t, lib, 20*time.Millisecond)
	photos := mustFind(t, i, "/Photos")
	before, _ := i.Names(photos)
	lib.put("photos/new.jpg", []byte("JPEG"))
	ev, err := i.WaitEvent(EventObjectAdded, 3*time.Second)
	if err != nil {
		t.Fatalf("no ObjectAdded (listed %v): %v", before, err)
	}
	if oi, err := i.ObjectInfo(ev.Params[0]); err != nil || oi.Filename != "new.jpg" {
		t.Errorf("the added object: %+v, %v", oi, err)
	}
	beach := mustFind(t, i, "/Photos/beach.jpg")
	lib.remove("photos/beach.jpg")
	ev, err = i.WaitEvent(EventObjectRemoved, 3*time.Second)
	if err != nil || ev.Params[0] != beach.Handle {
		t.Errorf("ObjectRemoved: %+v, %v (beach is %d)", ev, err, beach.Handle)
	}
	_, err = i.ObjectInfo(beach.Handle)
	wantCode(t, "a removed object", err, RespInvalidObjectHandle)
}

func TestClassRequests(t *testing.T) {
	i := connect(t, newLib(t), 0)
	if st, err := i.DeviceStatus(); err != nil || st != RespOK {
		t.Errorf("device status %s, %v", RespName(st), err)
	}
	if err := i.Cancel(); err != nil {
		t.Errorf("cancel: %v", err)
	}
	if _, err := i.t.Ctl.Control(Setup{RequestType: 0xA1, Request: reqExtendedEvent, Length: 16}, nil); !errors.Is(err, ErrStall) {
		t.Errorf("an extended event request: %v, want a stall", err)
	}
	if _, err := i.StorageIDs(); err != nil {
		t.Errorf("after the requests: %v", err)
	}
}

// The responder as phone-usbshared runs it: over usbshare.Remote, the
// library across a socket pair to the Shell's end, whose checks apply.
func TestOverTheHelperPair(t *testing.T) {
	lib := newLib(t)
	i := connect(t, overPair(t, lib), 0)
	notes := mustFind(t, i, "/Files/notes.txt")
	var got bytes.Buffer
	if err := i.Get(notes.Handle, &got); err != nil || got.String() != "0123456789abcdef" {
		t.Errorf("download through the pair: %q, %v", got.String(), err)
	}
	files := mustFind(t, i, "/Files")
	if _, err := i.Put(files.Storage, rootParent, "via-pair.txt", []byte("across")); err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("files/via-pair.txt"); string(b) != "across" {
		t.Errorf("upload through the pair: %q", b)
	}
	photos := mustFind(t, i, "/Photos")
	if _, err := i.SendObjectInfo(photos.Storage, rootParent, ObjectInfo{Format: FormatJPEG, CompressedSize: 10, Filename: "liar.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := i.SendObject(bytes.NewReader(make([]byte, 5000)), 5000); Code(err) != RespIncompleteTransfer && Code(err) != RespObjectTooLarge {
		t.Errorf("over the Shell's limit: %v", err)
	}
	if _, ok := lib.get("photos/liar.jpg"); ok {
		t.Error("the Shell kept a photo over its limit")
	}
	if _, err := i.SendObjectInfo(files.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 100, Filename: "cut.txt"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "cut short", i.SendObject(strings.NewReader("only"), 4), RespIncompleteTransfer)
	if _, ok := lib.get("files/cut.txt"); ok {
		t.Error("the Shell kept a file cut short")
	}
}

// overPair is lib as phone-usbshared reaches the Shell's: a
// usbshare.Remote across a socket pair to usbshare.Helper.
func overPair(t *testing.T, lib usbshare.Library) *usbshare.Remote {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ends [2]*net.UnixConn
	for k, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		nc, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		ends[k] = nc.(*net.UnixConn)
	}
	h := usbshare.ServeHelper(ends[0], usbshare.HelperConfig{Lib: lib})
	t.Cleanup(func() { h.Close() })
	remote, _, err := usbshare.Connect(ends[1], usbshare.Hello{Sandbox: []string{"test"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

func TestFraming(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fw := &framedWriter{w: w, packet: 8}
	fr := &framedReader{r: bufio.NewReader(r), packet: 8}
	go func() {
		fw.Write([]byte("0123456789")) // a full packet and a short one
		fw.Write([]byte("abcdefgh"))   // exactly one packet...
		fw.Write(nil)                  // ...ended by a zero-length one
		fw.Write([]byte("xyz"))
		w.Close()
	}()
	var reads []string
	for {
		b := make([]byte, 5)
		n, err := fr.Read(b)
		if err != nil {
			break
		}
		reads = append(reads, string(b[:n]))
	}
	if got := strings.Join(reads, "|"); got != "01234|56789|abcde|fgh|xyz" {
		t.Errorf("reads %q", got)
	}
}

func (m *memLib) mkdir(k string) {
	m.mu.Lock()
	m.dirs[k] = true
	m.mu.Unlock()
}

// galleryLib is memLib with the gallery's thumbnails: one of every photo,
// one of a video once its poster frame is made.
type galleryLib struct {
	*memLib
	tmu    sync.Mutex
	asked  int
	stills map[string]bool
	bad    bool // hand over what is not a JPEG
}

func newGallery(t *testing.T) *galleryLib {
	l := &galleryLib{memLib: newLib(t), stills: map[string]bool{}}
	l.put("videos/clip.webm", []byte("webm"))
	return l
}

func (l *galleryLib) Folders() []usbshare.Folder {
	fs := l.memLib.Folders()
	fs[0].Thumbs, fs[1].Thumbs = true, true
	return fs
}

// thumbJPEG is a grey thumbnail of w x h pixels.
func thumbJPEG(w, h int, shade byte) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for k := range img.Pix {
		img.Pix[k] = shade
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

// Thumb's shade is the file's size, so a thumbnail tells which file it is
// of and whether the file changed.
func (l *galleryLib) Thumb(folder, name string) (usbshare.Thumb, error) {
	l.tmu.Lock()
	defer l.tmu.Unlock()
	l.asked++
	b, ok := l.get(folder + "/" + name)
	switch {
	case !ok || folder != "photos" && folder != "videos":
		return usbshare.Thumb{}, os.ErrNotExist
	case folder == "videos" && !l.stills[name]:
		return usbshare.Thumb{}, errors.New("no poster frame yet")
	case l.bad:
		return usbshare.Thumb{JPEG: []byte("<html>"), W: 32, H: 24}, nil
	}
	return usbshare.Thumb{JPEG: thumbJPEG(32, 24, byte(len(b))), W: 32, H: 24}, nil
}

func (l *galleryLib) asks() int {
	l.tmu.Lock()
	defer l.tmu.Unlock()
	return l.asked
}

func (l *galleryLib) set(f func()) {
	l.tmu.Lock()
	f()
	l.tmu.Unlock()
}

// Photos and videos have thumbnails the phone made: their ObjectInfo
// names a JPEG and its size, GetThumb sends it, and a video's comes once
// its poster frame is made. Nothing else has one, and a phone whose
// library keeps none does not offer GetThumb.
func TestThumbnails(t *testing.T) {
	lib := newGallery(t)
	i := connect(t, lib, 0)
	di, err := i.DeviceInfo()
	if err != nil || !slices.Contains(di.Operations, OpGetThumb) {
		t.Fatalf("DeviceInfo lacks GetThumb: %v, %v", di.Operations, err)
	}
	beach := mustFind(t, i, "/Photos/beach.jpg")
	before := lib.asks()
	oi, err := i.ObjectInfo(beach.Handle)
	want := thumbJPEG(32, 24, byte(1000%256))
	if err != nil || oi.ThumbFormat != FormatJPEG || oi.ThumbCompressedSize != uint32(len(want)) || oi.ThumbPixWidth != 32 || oi.ThumbPixHeight != 24 {
		t.Errorf("a photo's thumbnail fields: %+v, %v", oi, err)
	}
	for range 2 {
		got, err := i.Thumb(beach.Handle)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("the photo's thumbnail: %d bytes, %v", len(got), err)
		}
	}
	if img, err := jpeg.Decode(bytes.NewReader(want)); err != nil || img.Bounds().Dx() != 32 || img.Bounds().Dy() != 24 {
		t.Errorf("the thumbnail is not a 32x24 JPEG: %v", err)
	}
	if n := lib.asks() - before; n > 1 {
		t.Errorf("the library was asked %d times for one thumbnail", n)
	}
	clip := mustFind(t, i, "/Videos/clip.webm")
	if clip.Info.ThumbFormat != 0 || clip.Info.ThumbCompressedSize != 0 {
		t.Errorf("a video without its poster frame: %+v", clip.Info)
	}
	_, err = i.Thumb(clip.Handle)
	wantCode(t, "a video without its poster frame", err, RespNoThumbnailPresent)
	lib.set(func() { lib.stills["clip.webm"] = true })
	if got, err := i.Thumb(clip.Handle); err != nil || !bytes.Equal(got, thumbJPEG(32, 24, 4)) {
		t.Errorf("the poster frame once made: %d bytes, %v", len(got), err)
	}
	if oi, err := i.ObjectInfo(clip.Handle); err != nil || oi.ThumbFormat != FormatJPEG {
		t.Errorf("the video's thumbnail fields: %+v, %v", oi, err)
	}
	asked := lib.asks()
	for _, p := range []string{"/Files/notes.txt", "/Music/Album", "/Music/Album/01.flac"} {
		f := mustFind(t, i, p)
		if f.Info.ThumbFormat != 0 || f.Info.ThumbCompressedSize != 0 {
			t.Errorf("%s has thumbnail fields: %+v", p, f.Info)
		}
		_, err := i.Thumb(f.Handle)
		wantCode(t, p, err, RespNoThumbnailPresent)
	}
	if lib.asks() != asked {
		t.Error("the library was asked for thumbnails of a folder without them")
	}
	_, err = i.Thumb(0x7777)
	wantCode(t, "no such object", err, RespInvalidObjectHandle)
	photos := mustFind(t, i, "/Photos")
	up, err := i.SendObjectInfo(photos.Storage, rootParent, ObjectInfo{Format: FormatJPEG, CompressedSize: 4, Filename: "up.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = i.Thumb(up)
	wantCode(t, "a photo not sent yet", err, RespNoThumbnailPresent)
	if err := i.SendObject(strings.NewReader("JPEG"), 4); err != nil {
		t.Fatal(err)
	}
	// The photo changed on the phone: the next listing finds it, and its
	// thumbnail is asked for again.
	lib.put("photos/beach.jpg", bytes.Repeat([]byte("K"), 2000))
	if _, err := i.Handles(photos.Storage, 0, rootParent); err != nil {
		t.Fatal(err)
	}
	if got, err := i.Thumb(beach.Handle); err != nil || !bytes.Equal(got, thumbJPEG(32, 24, byte(2000%256))) {
		t.Errorf("the changed photo's thumbnail: %d bytes, %v", len(got), err)
	}
	// What is not a JPEG is not passed on.
	lib.set(func() { lib.bad = true })
	lib.put("photos/other.jpg", []byte("JPEG"))
	other := mustFind(t, i, "/Photos/other.jpg")
	if other.Info.ThumbFormat != 0 {
		t.Errorf("a thumbnail that is not a JPEG: %+v", other.Info)
	}
	_, err = i.Thumb(other.Handle)
	wantCode(t, "a thumbnail that is not a JPEG", err, RespNoThumbnailPresent)
	lib.set(func() { lib.bad = false })
	// As phone-usbshared has them: from the Shell, across the pair.
	j := connect(t, overPair(t, lib), 0)
	pb := mustFind(t, j, "/Photos/beach.jpg")
	if got, err := j.Thumb(pb.Handle); err != nil || !bytes.Equal(got, thumbJPEG(32, 24, byte(2000%256))) || pb.Info.ThumbPixWidth != 32 {
		t.Errorf("a thumbnail across the pair: %d bytes, %+v, %v", len(got), pb.Info, err)
	}
	plain := connect(t, newLib(t), 0)
	if di, err := plain.DeviceInfo(); err != nil || slices.Contains(di.Operations, OpGetThumb) {
		t.Errorf("GetThumb offered without thumbnails: %v, %v", di.Operations, err)
	}
	pp := mustFind(t, plain, "/Photos/beach.jpg")
	_, err = plain.Thumb(pp.Handle)
	wantCode(t, "GetThumb without thumbnails", err, RespOperationNotSupported)
	if pp.Info.ThumbFormat != 0 {
		t.Errorf("thumbnail fields without thumbnails: %+v", pp.Info)
	}
}

func named(name string) []PropEntry {
	return []PropEntry{{Code: PropObjectFileName, Type: typeString, Value: name}}
}

// Windows and libmtp announce a file with SendObjectPropList when the
// phone offers it: the format and the exact size as parameters, the name
// as a property. It is SendObjectInfo otherwise: the same file stored, the
// same refusals with the same codes.
func TestSendObjectPropList(t *testing.T) {
	lib := newLib(t)
	i := connect(t, lib, 0)
	files, photos, videos, music := mustFind(t, i, "/Files"), mustFind(t, i, "/Photos"), mustFind(t, i, "/Videos"), mustFind(t, i, "/Music")
	h, err := i.SendObjectPropList(files.Storage, rootParent, FormatText, 5, append(named("list.txt"),
		PropEntry{Code: PropProtectionStatus, Type: typeUint16, Value: uint64(0)},
		PropEntry{Code: 0xDC46, Type: typeString, Value: "an artist"},
		PropEntry{Code: 0xDC89, Type: typeUint32, Value: uint64(1000)},
		PropEntry{Code: 0xDCFF, Type: typeArray | typeUint16, Value: []uint64{1, 2}},
		PropEntry{Code: PropName, Type: typeString, Value: "not the file name"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := i.SendObject(strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("files/list.txt"); string(b) != "hello" {
		t.Errorf("stored %q", b)
	}
	if oi, err := i.ObjectInfo(h); err != nil || oi.Filename != "list.txt" || oi.CompressedSize != 5 {
		t.Errorf("the file sent: %+v, %v", oi, err)
	}
	if _, err := i.SendObjectPropList(music.Storage, rootParent, FormatFLAC, 4,
		[]PropEntry{{Code: PropName, Type: typeString, Value: "named.flac"}}); err != nil {
		t.Fatal(err)
	}
	if err := i.SendObject(strings.NewReader("flac"), 4); err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("music/named.flac"); string(b) != "flac" {
		t.Errorf("a file named by Name alone: %q", b)
	}
	album, err := i.SendObjectPropList(music.Storage, rootParent, FormatAssociation, 0, named("Props album"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.SendObjectPropList(music.Storage, album, FormatText, 2, named("a.lrc")); err != nil {
		t.Fatal(err)
	}
	if err := i.SendObject(strings.NewReader("la"), 2); err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("music/Props album/a.lrc"); string(b) != "la" {
		t.Errorf("a file in a folder made so: %q", b)
	}
	// The size is exact, wider than ObjectInfo's: a container of unknown
	// length brings it.
	body := bytes.Repeat([]byte("F"), 3*Packet+100)
	if _, err := i.SendObjectPropList(music.Storage, rootParent, FormatFLAC, uint64(len(body)), named("sized.flac")); err != nil {
		t.Fatal(err)
	}
	if code := sendUnsized(t, i, body); code != RespOK {
		t.Errorf("an exact size in a container of unknown length: %s", RespName(code))
	}
	if b, _ := lib.get("music/sized.flac"); !bytes.Equal(b, body) {
		t.Errorf("stored %d bytes, want %d", len(b), len(body))
	}
	_, err = i.SendObjectPropList(videos.Storage, rootParent, FormatUndefinedVideo, 5<<30, named("long.webm"))
	wantCode(t, "5 GiB into Videos' 4", err, RespObjectTooLarge)
	if _, err := i.SendObjectPropList(files.Storage, rootParent, FormatText, 100, named("short.txt")); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "less than announced", i.SendObject(strings.NewReader("only this"), 9), RespIncompleteTransfer)
	if _, ok := lib.get("files/short.txt"); ok {
		t.Error("kept a file cut short")
	}
	album1 := mustFind(t, i, "/Music/Album")
	for _, c := range []struct {
		what            string
		storage, parent uint32
		oi              ObjectInfo
		code            uint16
	}{
		{"a text file into Photos", photos.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 4, Filename: "notes.txt"}, RespInvalidObjectFormatCode},
		{"a photo over the limit", photos.Storage, rootParent, ObjectInfo{Format: FormatJPEG, CompressedSize: 5000, Filename: "huge.jpg"}, RespObjectTooLarge},
		{"over Files' limit", files.Storage, rootParent, ObjectInfo{Format: FormatUndefined, CompressedSize: 2 << 20, Filename: "huge.bin"}, RespObjectTooLarge},
		{"more than the free space", music.Storage, rootParent, ObjectInfo{Format: FormatFLAC, CompressedSize: 1<<29 + 1, Filename: "big.flac"}, RespStoreFull},
		{"a name with a path", photos.Storage, rootParent, ObjectInfo{Format: FormatJPEG, CompressedSize: 1, Filename: "../escape.jpg"}, RespInvalidDataset},
		{"a name with a backslash", files.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 1, Filename: `a\b.txt`}, RespInvalidDataset},
		{"no name", files.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 1}, RespInvalidDataset},
		{"a folder in Files", files.Storage, rootParent, ObjectInfo{Format: FormatAssociation, Filename: "A folder"}, RespAccessDenied},
		{"no such storage", 0x00090001, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 1, Filename: "x.txt"}, RespInvalidStorageID},
		{"no such parent", files.Storage, 0x7777, ObjectInfo{Format: FormatText, CompressedSize: 1, Filename: "x.txt"}, RespInvalidParentObject},
		{"a parent in another storage", photos.Storage, album1.Handle, ObjectInfo{Format: FormatJPEG, CompressedSize: 1, Filename: "x.jpg"}, RespInvalidParentObject},
		{"a bad name in no such storage", 0x00090001, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 1, Filename: ".."}, RespInvalidStorageID},
	} {
		_, err := i.SendObjectInfo(c.storage, c.parent, c.oi)
		wantCode(t, c.what+" (SendObjectInfo)", err, c.code)
		_, err = i.SendObjectPropList(c.storage, c.parent, c.oi.Format, uint64(c.oi.CompressedSize), named(c.oi.Filename))
		wantCode(t, c.what+" (SendObjectPropList)", err, c.code)
	}
	ro := connect(t, roLib{lib}, 0)
	rom := mustFind(t, ro, "/Music")
	_, err = ro.SendObjectInfo(rom.Storage, rootParent, ObjectInfo{Format: FormatFLAC, CompressedSize: 4, Filename: "x.flac"})
	wantCode(t, "a read-only folder (SendObjectInfo)", err, RespStoreReadOnly)
	_, err = ro.SendObjectPropList(rom.Storage, rootParent, FormatFLAC, 4, named("x.flac"))
	wantCode(t, "a read-only folder (SendObjectPropList)", err, RespStoreReadOnly)
	// What only a property list can get wrong.
	idx, err := i.SendObjectPropList(files.Storage, rootParent, FormatText, 1,
		[]PropEntry{{Code: 0xDC46, Type: typeString, Value: "x"}, {Code: PropObjectFileName, Type: typeUint16, Value: uint64(7)}})
	if wantCode(t, "a name that is a number", err, RespInvalidObjectPropFormat); idx != 1 {
		t.Errorf("the property refused is %d, want 1", idx)
	}
	_, err = i.SendObjectPropList(files.Storage, rootParent, FormatText, 1, nil)
	wantCode(t, "no properties", err, RespInvalidDataset)
	garbage := []byte{5, 0, 0, 0, 0, 0, 0, 0, 0x07, 0xDC, 0x34, 0x12}
	_, err = i.transact(OpSendObjectPropList, []uint32{files.Storage, rootParent, FormatText, 0, 1}, bytes.NewReader(garbage), int64(len(garbage)), nil)
	wantCode(t, "not a property list", err, RespInvalidDataset)
	if got, err := i.StorageIDs(); err != nil || len(got) != 4 {
		t.Errorf("still in step: %v, %v", got, err)
	}
	if names, _ := i.Names(files); slices.Contains(names, "x.txt") || slices.Contains(names, "huge.bin") {
		t.Errorf("Files kept what was refused: %v", names)
	}
}

// A playlist the computer makes (Windows Media Player's, libmtp's: an
// abstract playlist and its references) becomes an M3U8 file beside the
// tracks, which the music library takes as one copied over; a playlist
// file on the phone gives its tracks. Neither needs the session it was
// made in.
func TestObjectReferences(t *testing.T) {
	lib := newLib(t)
	lib.put("music/Album/02.flac", []byte("flac2"))
	lib.put("music/top.flac", []byte("top"))
	lib.put("music/Live/b.flac", []byte("live"))
	lib.mkdir("music/Live")
	i := connect(t, lib, 0)
	music, files, photos := mustFind(t, i, "/Music"), mustFind(t, i, "/Files"), mustFind(t, i, "/Photos")
	one, two, top := mustFind(t, i, "/Music/Album/01.flac"), mustFind(t, i, "/Music/Album/02.flac"), mustFind(t, i, "/Music/top.flac")
	tracks := []uint32{two.Handle, one.Handle, top.Handle, two.Handle}
	h, err := i.Playlist(music.Storage, rootParent, "Road trip", tracks)
	if err != nil {
		t.Fatal(err)
	}
	want := "#EXTM3U\nAlbum/02.flac\nAlbum/01.flac\ntop.flac\nAlbum/02.flac\n"
	if b, _ := lib.get("music/Road trip.m3u8"); string(b) != want {
		t.Errorf("the playlist stored %q, want %q", b, want)
	}
	if ev, err := i.WaitEvent(EventObjectInfoChanged, 2*time.Second); err != nil || ev.Params[0] != h {
		t.Errorf("the playlist's ObjectInfoChanged: %+v, %v", ev, err)
	}
	oi, err := i.ObjectInfo(h)
	if err != nil || oi.Filename != "Road trip.m3u8" || oi.Format != FormatM3U || oi.CompressedSize != uint32(len(want)) || oi.Protection != 1 {
		t.Errorf("the playlist: %+v, %v", oi, err)
	}
	if refs, err := i.References(h); err != nil || !slices.Equal(refs, tracks) {
		t.Errorf("the playlist's references %v, %v, want %v", refs, err, tracks)
	}
	album := mustFind(t, i, "/Music/Album")
	if _, err := i.Playlist(music.Storage, album.Handle, "Best of", []uint32{two.Handle, top.Handle}); err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("music/Album/Best of.m3u8"); string(b) != "#EXTM3U\n02.flac\n../top.flac\n" {
		t.Errorf("a playlist in a folder: %q", b)
	}
	if h2, err := i.Playlist(music.Storage, rootParent, "Road trip", []uint32{top.Handle}); err != nil {
		t.Fatal(err)
	} else if oi, _ := i.ObjectInfo(h2); oi.Filename != "Road trip (2).m3u8" {
		t.Errorf("a playlist whose name is taken: %+v", oi)
	}
	// libmtp without SendObjectPropList: SendObjectInfo and one byte. A file
	// announced meanwhile leaves the playlist waiting; it may be renamed
	// until its tracks come (LIBMTP_Update_Playlist).
	d, err := i.SendObjectInfo(music.Storage, rootParent, ObjectInfo{Format: FormatAbstractAVPlaylist, CompressedSize: 1, Filename: "Draft.pla"})
	if err != nil {
		t.Fatal(err)
	}
	if err := i.SendObject(bytes.NewReader([]byte{0}), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Put(music.Storage, rootParent, "between.lrc", []byte("[00:00.00]")); err != nil {
		t.Fatal(err)
	}
	if oi, err := i.ObjectInfo(d); err != nil || oi.Format != FormatAbstractAVPlaylist || oi.Filename != "Draft.pla" {
		t.Errorf("a playlist waiting for its tracks: %+v, %v", oi, err)
	}
	if err := i.SetName(d, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := i.SetReferences(d, []uint32{one.Handle}); err != nil {
		t.Fatal(err)
	}
	if b, _ := lib.get("music/Renamed.m3u8"); string(b) != "#EXTM3U\nAlbum/01.flac\n" {
		t.Errorf("the renamed playlist: %q", b)
	}
	if fp, err := i.Playlist(files.Storage, rootParent, "In files", nil); err != nil {
		t.Errorf("a playlist in Files: %v", err)
	} else if err := i.SetReferences(fp, nil); err != nil {
		t.Errorf("an empty playlist: %v", err)
	}
	if b, _ := lib.get("files/In files.m3u8"); string(b) != "#EXTM3U\n" {
		t.Errorf("an empty playlist in Files: %q", b)
	}
	// Refusals: nothing on the phone changes.
	_, err = i.Playlist(photos.Storage, rootParent, "Snaps", nil)
	wantCode(t, "a playlist in Photos", err, RespInvalidObjectFormatCode)
	wantCode(t, "tracks of a track", i.SetReferences(one.Handle, nil), RespObjectWriteProtected)
	wantCode(t, "new tracks of a playlist on the phone", i.SetReferences(h, []uint32{one.Handle}), RespObjectWriteProtected)
	wantCode(t, "tracks of no object", i.SetReferences(0x7777, nil), RespInvalidObjectHandle)
	p, err := i.SendObjectInfo(files.Storage, rootParent, ObjectInfo{Format: FormatText, CompressedSize: 1, Filename: "x.txt"})
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, "tracks of a file being sent", i.SetReferences(p, nil), RespAccessDenied)
	i.Delete(p)
	e, err := i.Playlist(music.Storage, rootParent, "Bad", nil)
	if err != nil {
		t.Fatal(err)
	}
	photo := mustFind(t, i, "/Photos/beach.jpg")
	for _, c := range []struct {
		what string
		refs []uint32
	}{{"no such track", []uint32{one.Handle, 0x7777}}, {"a folder", []uint32{album.Handle}}, {"a photo", []uint32{photo.Handle}}, {"itself", []uint32{e}}} {
		wantCode(t, "a playlist of "+c.what, i.SetReferences(e, c.refs), RespInvalidObjectReference)
	}
	short := []byte{3, 0, 0, 0, 1, 0, 0, 0}
	_, err = i.transact(OpSetObjectReferences, []uint32{e}, bytes.NewReader(short), int64(len(short)), nil)
	wantCode(t, "references cut short", err, RespInvalidDataset)
	if b, ok := lib.get("music/Road trip.m3u8"); !ok || string(b) != want {
		t.Errorf("the first playlist is now %q", b)
	}
	_, err = i.References(0x7777)
	wantCode(t, "the references of no object", err, RespInvalidObjectHandle)
	for what, h := range map[string]uint32{"a track": one.Handle, "a folder": album.Handle, "a playlist being made": e} {
		if refs, err := i.References(h); err != nil || len(refs) != 0 {
			t.Errorf("the references of %s: %v, %v", what, refs, err)
		}
	}
	// A playlist given no tracks before the session ends is not kept.
	if err := i.CloseSession(); err != nil {
		t.Fatal(err)
	}
	if err := i.OpenSession(); err != nil {
		t.Fatal(err)
	}
	if _, ok := lib.get("music/Bad.m3u8"); ok {
		t.Error("a playlist without tracks was kept")
	}
	_, err = i.ObjectInfo(e)
	wantCode(t, "a playlist without tracks after the session", err, RespInvalidObjectHandle)
	// Playlists on the phone, as people copy them: PLS, M3U from Windows
	// (Latin-1, backslashes), entries that are not files in Music.
	lib.put("music/mix.pls", []byte("[playlist]\r\nFile1=Album\\01.flac\r\nTitle1=One\r\nFile2=http://radio.example/stream\r\n"+
		"File3=../../shell/settings.json\r\nfile4 = Live/b.flac\r\nFile5=/etc/passwd\r\nNumberOfEntries=5\r\n"))
	lib.put("music/Album/old.m3u", []byte("\xef\xbb\xbf#EXTM3U\n#EXTINF:1,One\n01.flac\n\n../top.flac\nmissing.flac\nC:\\Music\\x.flac\n../Live/b.flac\n"))
	lib.put("music/latin1.m3u", []byte("Caf\xe9.flac\r\n"))
	lib.put("music/Café.flac", []byte("cafe"))
	// Another responder, as after a restart or the cable: what it says
	// comes from the files.
	j := connect(t, lib, 0)
	for p, want := range map[string][]string{
		"/Music/Road trip.m3u8":     {"02.flac", "01.flac", "top.flac", "02.flac"},
		"/Music/Album/Best of.m3u8": {"02.flac", "top.flac"},
		"/Music/mix.pls":            {"01.flac", "b.flac"},
		"/Music/Album/old.m3u":      {"01.flac", "top.flac", "b.flac"},
		"/Music/latin1.m3u":         {"Café.flac"},
	} {
		f := mustFind(t, j, p)
		refs, err := j.References(f.Handle)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		var names []string
		for _, r := range refs {
			oi, err := j.ObjectInfo(r)
			if err != nil {
				t.Errorf("%s: a reference %d: %v", p, r, err)
			}
			names = append(names, oi.Filename)
		}
		if !slices.Equal(names, want) {
			t.Errorf("%s holds %v, want %v", p, names, want)
		}
	}
}

// The operations the phone used to answer with OperationNotSupported,
// and left out of DeviceInfo.
func TestMissingOperations(t *testing.T) {
	i := connect(t, newGallery(t), 0)
	di, err := i.DeviceInfo()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(di.Formats, FormatAbstractAVPlaylist) {
		t.Errorf("DeviceInfo's formats lack abstract playlists (libmtp makes none without): %v", di.Formats)
	}
	for _, op := range []uint16{OpGetThumb, OpSendObjectPropList, OpGetObjectReferences, OpSetObjectReferences} {
		if !slices.Contains(di.Operations, op) {
			t.Errorf("DeviceInfo lacks 0x%04x", op)
		}
	}
	beach, files := mustFind(t, i, "/Photos/beach.jpg"), mustFind(t, i, "/Files")
	_, err = i.Thumb(beach.Handle)
	_, err2 := i.SendObjectPropList(files.Storage, rootParent, FormatText, 0, named("new.txt"))
	if err2 == nil {
		err2 = i.SendObject(strings.NewReader(""), 0)
	}
	_, err3 := i.References(beach.Handle)
	err4 := i.SetReferences(beach.Handle, nil)
	for k, err := range []error{err, err2, err3, err4} {
		if Code(err) == RespOperationNotSupported {
			t.Errorf("operation %d: %v", k, err)
		}
	}
	if err != nil || err2 != nil || err3 != nil || Code(err4) != RespObjectWriteProtected {
		t.Errorf("GetThumb %v, SendObjectPropList %v, GetObjectReferences %v, SetObjectReferences %v", err, err2, err3, err4)
	}
}
