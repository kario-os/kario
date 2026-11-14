package mtp

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
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
	if folder == "music" && ext != ".flac" && ext != ".lrc" {
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
		if err != nil || pd.Code != c || pd.Settable {
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
	i.tid++
	tid := i.tid
	body := bytes.Repeat([]byte("F"), 3*Packet+100)
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
	if err != nil || h.typ != typeResponse || h.code != RespOK {
		t.Fatalf("response %+v, %v", h, err)
	}
	i.rd.payload(h).drain()
	if b, _ := lib.get("music/huge.flac"); !bytes.Equal(b, body) {
		t.Errorf("stored %d bytes, want %d", len(b), len(body))
	}
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
	defer h.Close()
	remote, _, err := usbshare.Connect(ends[1], usbshare.Hello{Sandbox: []string{"test"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := connect(t, remote, 0)
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
