package shell

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// Files: documents and attachments that belong to the user, not to
// the app that brought them, kept by the system as photos are. An app gets
// to a file only through the user: saving one asks first (file.save),
// picking one goes through the system's list and hands over that file
// read-only (file.pick), and sharing one grants it to the receiving app
// (file.open). No permission covers all files.

const (
	maxFileBytes = 16 << 20
	maxFileName  = 64
)

type fileItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	At   int64  `json:"at"`
}

func (f *fileItem) ui() ui.FileItem {
	return ui.FileItem{ID: f.ID, Name: f.Name, Mime: f.Mime, Size: f.Size, At: f.At}
}

type fileStore struct {
	dir    string
	Seq    int                        `json:"seq"`
	Items  []*fileItem                `json:"items"`
	Grants map[string]map[string]bool `json:"grants"` // app -> file IDs it may open
}

func openFiles(dir string, logf func(string, ...any)) *fileStore {
	s := &fileStore{dir: dir, Grants: map[string]map[string]bool{}}
	readState(logf, filepath.Join(dir, "index.json"), s)
	if s.Grants == nil {
		s.Grants = map[string]map[string]bool{}
	}
	return s
}

func (s *fileStore) save() {
	raw, _ := json.MarshalIndent(s, "", "  ")
	_ = statefile.Write(filepath.Join(s.dir, "index.json"), raw, 0o600)
}

func (s *fileStore) path(f *fileItem) string { return filepath.Join(s.dir, f.ID) }

func (s *fileStore) get(id string) *fileItem {
	for _, f := range s.Items {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (s *fileStore) grant(app string, f *fileItem) {
	if s.Grants[app] == nil {
		s.Grants[app] = map[string]bool{}
	}
	s.Grants[app][f.ID] = true
	s.save()
}

// cleanFileName keeps a name a user can read: no directories, no control
// characters, at most 64 characters.
func cleanFileName(name string) (string, error) {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if !utf8.ValidString(name) || name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
		return "", errors.New("the file needs a name")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("the file name has control characters")
		}
	}
	if rs := []rune(name); len(rs) > maxFileName {
		name = string(rs[:maxFileName])
	}
	return name, nil
}

func (s *fileStore) add(name string, raw []byte, at time.Time) (*fileItem, error) {
	if len(raw) > maxFileBytes {
		return nil, fmt.Errorf("files are at most %d MB", maxFileBytes>>20)
	}
	s.Seq++
	m := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if m == "" {
		m = "application/octet-stream"
	}
	f := &fileItem{ID: fmt.Sprintf("f%d", s.Seq), Name: name, Mime: m, Size: int64(len(raw)), At: at.Unix()}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	if err := statefile.Write(s.path(f), raw, 0o600); err != nil {
		return nil, err
	}
	s.Items = append(s.Items, f)
	s.save()
	return f, nil
}

// freeName is name, or "name (2).ext" and so on when Files has it.
func (s *fileStore) freeName(name string) string {
	taken := map[string]bool{}
	for _, f := range s.Items {
		taken[f.Name] = true
	}
	got, ext := name, filepath.Ext(name)
	for i := 2; taken[got]; i++ {
		got = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
	}
	return got
}

// addPath moves a file that arrived whole on disk (from an old phone) into
// Files; it may be larger than maxFileBytes, as it is never read into
// memory here. mime "" goes by the name.
func (s *fileStore) addPath(name, path, mime string, at time.Time) (*fileItem, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if mime == "" {
		mime = firstNonEmpty(mimeByExt(name), "application/octet-stream")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	s.Seq++
	f := &fileItem{ID: fmt.Sprintf("f%d", s.Seq), Name: name, Mime: mime, Size: st.Size(), At: at.Unix()}
	// The receiver stages under the store's directory: one file system.
	if err := os.Rename(path, s.path(f)); err != nil {
		return nil, err
	}
	_ = os.Chmod(s.path(f), 0o600)
	s.Items = append(s.Items, f)
	s.save()
	return f, nil
}

func mimeByExt(name string) string {
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
}

// replace writes a file of the store anew (one that grows as it comes in).
func (s *fileStore) replace(f *fileItem, raw []byte) error {
	if len(raw) > maxFileBytes {
		return fmt.Errorf("files are at most %d MB", maxFileBytes>>20)
	}
	if err := statefile.Write(s.path(f), raw, 0o600); err != nil {
		return err
	}
	f.Size = int64(len(raw))
	s.save()
	return nil
}

// remove deletes a file and the grants to it; false when there is none.
func (s *fileStore) remove(id string) bool {
	i := slices.IndexFunc(s.Items, func(f *fileItem) bool { return f.ID == id })
	if i < 0 {
		return false
	}
	_ = os.Remove(s.path(s.Items[i]))
	s.Items = slices.Delete(s.Items, i, i+1)
	for _, g := range s.Grants {
		delete(g, id)
	}
	s.save()
	return true
}

// inMemory: a file small enough to be read whole (Bluetooth sends it from
// memory, a page's file field gets a copy); an old phone's videos may not
// be.
func (f *fileItem) inMemory() bool { return f.Size <= maxFileBytes }

func (c *Core) fileOrigin(a *App, origin string) string {
	if origin != "" {
		return a.M.Name + " · " + origin
	}
	return a.M.Name
}

// saveFile asks the user before an app's file goes into Files.
func (c *Core) saveFile(a *App, origin string, args ui.FileArgs, reply func(any, error)) {
	name, err := cleanFileName(args.Name)
	var raw []byte
	if err == nil {
		if len(args.Data) > maxFileBytes/3*4+8 {
			err = fmt.Errorf("files are at most %d MB", maxFileBytes>>20)
		} else {
			raw, err = base64.StdEncoding.DecodeString(args.Data)
		}
	}
	if err != nil {
		reply(nil, err)
		return
	}
	c.pushOverlay(&confirmOverlay{text: tr.T("Save “%s” to Files?\nFrom: %s\n%s", name, c.fileOrigin(a, origin), sizeLabel(int64(len(raw)))),
		onYes: func() {
			f, err := c.files.add(name, raw, c.now())
			if err != nil {
				reply(nil, err)
				return
			}
			c.files.grant(permKey(a, origin), f)
			c.log.Printf("%s: saved file %s (%d bytes)", a.M.ID, f.ID, f.Size)
			c.showToast(tr.T("Saved to Files"))
			reply(f.ui(), nil)
		},
		onNo: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// playableVideo: a file the media library can play (mediad decodes WebM;
// MP4's codecs are the device's hardware decoders', not implemented).
func playableVideo(name string) bool { return strings.EqualFold(filepath.Ext(name), ".webm") }

// offerVideo asks whether a video that came into Files goes into Pictures as
// well, where the apps with the album permission see it.
func (c *Core) offerVideo(f *fileItem) {
	c.pushOverlay(&confirmOverlay{text: tr.T("“%s” is a video. Add it to “Pictures” too?\nThen apps with the “Photos” permission can see it as well", f.Name),
		yes: tr.T("Add"), no: tr.T("No, thanks"),
		onYes: func() {
			it, err := c.media.addVideo(c.files.path(f), strings.TrimSuffix(f.Name, filepath.Ext(f.Name)), strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Name), ".")), c.now())
			if err != nil {
				c.showToast(tr.T("Could not add it to Pictures: %v", err))
				return
			}
			c.log.Printf("files: video %s into the album as %s", f.ID, it.ID)
			c.probeVideos()
			c.showToast(tr.T("Added to Pictures"))
		},
		onNo: func() {}})
}

func sizeLabel(n int64) string {
	if n < 1024 {
		return tr.T("%d bytes", n)
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

func matchesTypes(f *fileItem, types []string) bool {
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		t = strings.ToLower(strings.TrimPrefix(t, "."))
		if strings.EqualFold(strings.TrimPrefix(filepath.Ext(f.Name), "."), t) || strings.HasPrefix(f.Mime, t) {
			return true
		}
	}
	return false
}

// pickFile lists the user's files in a system menu; the one picked goes to
// the app read-only, with nothing else of the store.
func (c *Core) pickFile(a *App, origin string, args ui.FileArgs, reply func(any, error)) {
	var es []menuEntry
	for i := len(c.files.Items) - 1; i >= 0; i-- {
		f := c.files.Items[i]
		if !matchesTypes(f, args.Types) {
			continue
		}
		es = append(es, menuEntry{group: tr.T("Files"), label: f.Name + " · " + sizeLabel(f.Size), find: f.Name, run: func() {
			c.files.grant(permKey(a, origin), f)
			c.log.Printf("%s: picked file %s", a.M.ID, f.ID)
			c.handFile(f, args.Read, reply)
		}})
	}
	if len(es) == 0 {
		reply(nil, errors.New("no files"))
		return
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Choose a file · for “%s”", c.fileOrigin(a, origin)), entries: es,
		onCancel: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// openFile hands over a file the app was given (shared with it, picked or
// saved by it).
func (c *Core) openFile(a *App, origin string, args ui.FileArgs, reply func(any, error)) {
	f := c.files.get(args.ID)
	if f == nil || !c.files.Grants[permKey(a, origin)][args.ID] {
		reply(nil, errors.New("no such file"))
		return
	}
	c.handFile(f, args.Read, reply)
}

// handFile answers with the file read-only, or with read its bytes.
func (c *Core) handFile(f *fileItem, read bool, reply func(any, error)) {
	if read {
		if f.Size > ui.MaxFileRead {
			reply(nil, fmt.Errorf("“%s” is larger than %d KB to read whole", f.Name, ui.MaxFileRead>>10))
			return
		}
		raw, err := os.ReadFile(c.files.path(f))
		if err != nil {
			reply(nil, err)
			return
		}
		it := f.ui()
		it.Data = base64.StdEncoding.EncodeToString(raw)
		reply(it, nil)
		return
	}
	h, err := os.Open(c.files.path(f))
	if err != nil {
		reply(nil, err)
		return
	}
	reply(withFiles{data: f.ui(), files: []*os.File{h}}, nil)
}
