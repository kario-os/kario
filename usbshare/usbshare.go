// Package usbshare is file sharing with a computer over the USB cable, the
// way an iPhone shares with a Mac once it trusts it: the computer reaches
// the phone over the USB network link (hal/usbgadget) only after the user
// trusts it on the phone, and then gets the user's photos, videos, music
// and files, through a page in any browser or mounted as a WebDAV drive
// (Finder, Explorer, Nautilus). A password the phone shows protects each
// connection, and nothing is reachable any more once the cable is out.
// The library behind it is the system's (the shell's media and file
// stores), so what the computer adds shows up on the phone as if saved
// there.
package usbshare

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/i18n"
)

//go:embed locale
var localeFS embed.FS

var tr = i18n.New(localeFS)

// Item is a file in a folder, or (Dir) a folder in a nested one.
type Item struct {
	Name    string
	Mime    string
	Size    int64
	ModTime time.Time
	Dir     bool
}

// Folder is one of the library's top-level folders.
type Folder struct {
	ID       string // what the Library calls it: photos, videos, music, files
	Name     string // shown
	Writable bool   // the computer may add files
	// Path is the folder's name in URLs, what a mounted drive shows (the
	// device's language); ID when empty.
	Path string
	// Nested folders keep folders inside them (an album folder in music);
	// the Library implements Dirs for them, and file names carry their
	// folders ("Album/01.flac").
	Nested bool
	// MaxBytes bounds one file the computer adds; MaxUpload when 0.
	MaxBytes int64
	// Accepts are the extensions (".jpg") of the files the folder takes,
	// any when empty: what lets MTP refuse a file before it is sent; Save
	// still decides.
	Accepts []string `json:",omitempty"`
	// Capacity and Free are the bytes of the file system the folder is on,
	// 0 when not known.
	Capacity int64 `json:",omitempty"`
	Free     int64 `json:",omitempty"`
}

func (f Folder) seg() string {
	if f.Path != "" {
		return f.Path
	}
	return f.ID
}

func (f Folder) limit() int64 {
	if f.MaxBytes > 0 {
		return f.MaxBytes
	}
	return MaxUpload
}

// Library is what the computer sees.
type Library interface {
	Folders() []Folder
	List(folder string) []Item
	Open(folder, name string) (io.ReadSeekCloser, Item, error)
	// Save adds a file to a writable folder, under a free name; it returns
	// the name it got.
	Save(folder, name string, r io.Reader) (string, error)
}

// Dirs is what a Library adds for its nested folders: dir is a folder's
// path inside the top-level one ("Album", "Artist/Album").
type Dirs interface {
	ListDir(folder, dir string) ([]Item, bool)
	MakeDir(folder, dir string) error
}

// MaxUpload is the largest file a computer may add to a folder that does
// not say otherwise.
const MaxUpload = 1 << 30

// ErrTooLarge ends a copy that is over its folder's limit.
var ErrTooLarge = errors.New("the file is too large for this folder")

// maxFailures wrong passwords end the connection's sharing.
const maxFailures = 10

// Password is a fresh one-time password for a connection: eight digits,
// easy to type from the phone's screen.
func Password() string {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%08d", n)
}

// Server is the sharing of one connection.
type Server struct {
	lib      Library
	password string
	mu       sync.Mutex
	failures int
	// Log reports what the computer did (the shell's log).
	Log func(format string, args ...any)
}

func New(lib Library, password string) *Server {
	return &Server{lib: lib, password: password, Log: func(string, ...any) {}}
}

// Locked: too many wrong passwords; the phone has to share again.
func (s *Server) Locked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures >= maxFailures
}

func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures >= maxFailures {
		http.Error(w, tr.T("Too many wrong passwords: share again from the phone"), http.StatusForbidden)
		return false
	}
	if ok && subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) == 1 {
		return true
	}
	if ok {
		s.failures++
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="Kario", charset="UTF-8"`)
	http.Error(w, tr.T("Enter the password shown on the phone"), http.StatusUnauthorized)
	return false
}

// folder finds a top-level folder by its URL name or its ID.
func (s *Server) folder(seg string) (Folder, bool) {
	for _, f := range s.lib.Folders() {
		if f.ID == seg || f.Path != "" && f.Path == seg {
			return f, true
		}
	}
	return Folder{}, false
}

// split is a request path's folder and the rest ("" for the folder).
func split(p string) (folder, name string) {
	p = strings.Trim(path.Clean("/"+p), "/")
	folder, name, _ = strings.Cut(p, "/")
	return folder, name
}

// dir lists a folder (rest "") or a folder inside a nested one.
func (s *Server) dir(f Folder, rest string) ([]Item, bool) {
	if rest == "" {
		return s.lib.List(f.ID), true
	}
	if d, ok := s.lib.(Dirs); ok && f.Nested {
		return d.ListDir(f.ID, rest)
	}
	return nil, false
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'")
	seg, rest := split(r.URL.Path)
	var f Folder
	if seg != "" {
		var ok bool
		if f, ok = s.folder(seg); !ok || strings.Contains(rest, "/") && !f.Nested {
			http.NotFound(w, r)
			return
		}
	}
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("DAV", "1")
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND, PUT, POST, MKCOL")
		w.Header().Set("MS-Author-Via", "DAV")
	case http.MethodGet, http.MethodHead:
		s.get(w, r, f, rest)
	case "PROPFIND":
		s.propfind(w, r, f, rest)
	case http.MethodPut:
		s.put(w, r, f, rest, r.Body)
	case http.MethodPost:
		s.upload(w, r, f, rest)
	case "MKCOL":
		s.mkcol(w, r, f, rest)
	default:
		http.Error(w, tr.T("Not supported"), http.StatusMethodNotAllowed)
	}
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, f Folder, rest string) {
	if f.ID == "" {
		page(w, pageArgs{title: tr.T("Kario phone"), folders: s.lib.Folders()})
		return
	}
	if items, ok := s.dir(f, rest); ok {
		title := f.Name
		if rest != "" {
			title = f.Name + " / " + rest
		}
		page(w, pageArgs{title: title, base: f.seg(), dir: rest, items: items, writable: f.Writable, nested: f.Nested})
		return
	}
	rc, it, err := s.lib.Open(f.ID, rest)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer rc.Close()
	if it.Mime != "" {
		w.Header().Set("Content-Type", it.Mime)
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(it.Name))
	}
	if r.Method == http.MethodGet {
		s.Log("usb sharing: %s/%s copied to the computer", f.ID, rest)
	}
	http.ServeContent(w, r, it.Name, it.ModTime, rc)
}

// capped stops a copy past its folder's limit with ErrTooLarge (the
// Library sees an error, not a file cut short).
type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			return 0, ErrTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (s *Server) save(w http.ResponseWriter, f Folder, name string, body io.Reader) bool {
	got, err := s.lib.Save(f.ID, name, &capped{r: body, left: f.limit()})
	switch {
	case errors.Is(err, ErrTooLarge):
		http.Error(w, tr.T("The file is too large for this folder"), http.StatusRequestEntityTooLarge)
		return false
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	s.Log("usb sharing: the computer added %s/%s", f.ID, got)
	return true
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, f Folder, rest string, body io.Reader) {
	if f.ID == "" || rest == "" {
		http.NotFound(w, r)
		return
	}
	if !f.Writable {
		http.Error(w, tr.T("This folder is read-only"), http.StatusForbidden)
		return
	}
	if r.ContentLength > f.limit() {
		http.Error(w, tr.T("The file is too large for this folder"), http.StatusRequestEntityTooLarge)
		return
	}
	if s.save(w, f, rest, body) {
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) mkcol(w http.ResponseWriter, r *http.Request, f Folder, rest string) {
	d, ok := s.lib.(Dirs)
	if f.ID == "" || rest == "" || !ok || !f.Nested || !f.Writable {
		http.Error(w, tr.T("This folder is read-only"), http.StatusForbidden)
		return
	}
	if err := d.MakeDir(f.ID, rest); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.Log("usb sharing: the computer made the folder %s/%s", f.ID, rest)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, f Folder, rest string) {
	if f.ID == "" || !f.Writable {
		http.Error(w, tr.T("This folder is read-only"), http.StatusForbidden)
		return
	}
	if _, ok := s.dir(f, rest); !ok {
		http.NotFound(w, r)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if part.FileName() == "" {
			continue
		}
		name := baseName(part.FileName())
		if f.Nested {
			// A folder chosen in the browser comes as "Album/01.flac"
			// (FileName keeps only the last element).
			_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if rel := relName(params["filename"]); rel != "" {
				name = rel
			}
			if rest != "" {
				name = rest + "/" + name
			}
		}
		if !s.save(w, f, name, part) {
			return
		}
	}
	http.Redirect(w, r, esc("/"+f.seg()+"/"+dirSlash(rest)), http.StatusSeeOther)
}

func dirSlash(d string) string {
	if d == "" {
		return ""
	}
	return d + "/"
}

// baseName is an uploaded file's name without the folders some browsers
// send along (a Windows path included).
func baseName(n string) string {
	if i := strings.LastIndexAny(n, `/\`); i >= 0 {
		n = n[i+1:]
	}
	return n
}

// relName is the relative path of a file from a folder upload, "" when
// the name has no folders or tries to leave its folder.
func relName(n string) string {
	n = strings.ReplaceAll(n, `\`, "/")
	if !strings.Contains(n, "/") || strings.HasPrefix(n, "/") || strings.Contains(n, ":") {
		return ""
	}
	for _, p := range strings.Split(n, "/") {
		if p == "" || p == "." || p == ".." {
			return ""
		}
	}
	return n
}

// WebDAV's PROPFIND, enough for a mount and for PUT: the folders, their
// files and folders, and a file itself.
type multistatus struct {
	XMLName  xml.Name   `xml:"D:multistatus"`
	XMLNS    string     `xml:"xmlns:D,attr"`
	Response []response `xml:"D:response"`
}

type response struct {
	Href string `xml:"D:href"`
	Prop prop   `xml:"D:propstat>D:prop"`
	Stat string `xml:"D:propstat>D:status"`
}

type prop struct {
	Name     string   `xml:"D:displayname"`
	Type     *restype `xml:"D:resourcetype"`
	Length   *int64   `xml:"D:getcontentlength,omitempty"`
	Mime     string   `xml:"D:getcontenttype,omitempty"`
	Modified string   `xml:"D:getlastmodified,omitempty"`
}

type restype struct {
	Collection *struct{} `xml:"D:collection"`
}

func dir(href, name string) response {
	return response{Href: href, Prop: prop{Name: name, Type: &restype{Collection: &struct{}{}}}, Stat: "HTTP/1.1 200 OK"}
}

func file(href string, it Item) response {
	if it.Dir {
		return dir(href+"/", it.Name)
	}
	n := it.Size
	return response{Href: href, Prop: prop{Name: it.Name, Type: &restype{}, Length: &n, Mime: it.Mime, Modified: it.ModTime.UTC().Format(http.TimeFormat)}, Stat: "HTTP/1.1 200 OK"}
}

func esc(s string) string { return (&url.URL{Path: s}).EscapedPath() }

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, f Folder, rest string) {
	deep := r.Header.Get("Depth") != "0"
	ms := multistatus{XMLNS: "DAV:"}
	if f.ID == "" {
		ms.Response = append(ms.Response, dir("/", tr.T("Kario phone")))
		if deep {
			for _, f := range s.lib.Folders() {
				ms.Response = append(ms.Response, dir(esc("/"+f.seg()+"/"), f.Name))
			}
		}
	} else if items, ok := s.dir(f, rest); ok {
		base := "/" + f.seg() + "/" + dirSlash(rest)
		name := f.Name
		if rest != "" {
			name = path.Base(rest)
		}
		ms.Response = append(ms.Response, dir(esc(base), name))
		if deep {
			for _, it := range items {
				ms.Response = append(ms.Response, file(esc(base+it.Name), it))
			}
		}
	} else {
		rc, it, err := s.lib.Open(f.ID, rest)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		rc.Close()
		ms.Response = append(ms.Response, file(esc("/"+f.seg()+"/"+rest), it))
	}
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(ms)
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>{{.Title}}</title>
<style>body{font-family:sans-serif;max-width:46em;margin:2em auto;padding:0 1em}
li{margin:.4em 0}small{color:#666}</style></head><body>
<h1>{{.Title}}</h1>
{{if .Up}}<p><a href="{{.Up}}">{{.UpLabel}}</a></p>{{end}}
<ul>
{{range .Folders}}<li><a href="{{.Href}}">{{.Name}}</a></li>
{{end}}{{range .Items}}<li><a href="{{.Href}}">{{.Name}}</a> <small>{{.Size}}</small> <a href="{{.Href}}?download">{{$.Download}}</a></li>
{{end}}</ul>
{{if .Empty}}<p>{{.EmptyLabel}}</p>{{end}}
{{if .Writable}}<form method="post" enctype="multipart/form-data">
<input type="file" name="file" multiple> <button>{{.UploadLabel}}</button></form>{{end}}
{{if .Nested}}<form method="post" enctype="multipart/form-data">
<input type="file" name="dir" webkitdirectory multiple> <button>{{.FolderLabel}}</button></form>{{end}}
<p><small>{{.Hint}}</small></p>
</body></html>`))

type pageItem struct {
	Name, Href, Size string
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

type pageArgs struct {
	title    string
	base     string // the folder's URL name
	dir      string // inside a nested folder
	folders  []Folder
	items    []Item
	writable bool
	nested   bool
}

func page(w http.ResponseWriter, a pageArgs) {
	sort.SliceStable(a.items, func(i, j int) bool {
		if a.items[i].Dir != a.items[j].Dir {
			return a.items[i].Dir
		}
		if a.items[i].Dir {
			return a.items[i].Name < a.items[j].Name
		}
		return a.items[i].ModTime.After(a.items[j].ModTime)
	})
	var folders, list []pageItem
	for _, f := range a.folders {
		folders = append(folders, pageItem{Name: f.Name, Href: esc("/" + f.seg() + "/")})
	}
	prefix := "/" + a.base + "/" + dirSlash(a.dir)
	for _, it := range a.items {
		if it.Dir {
			folders = append(folders, pageItem{Name: it.Name + "/", Href: esc(prefix + it.Name + "/")})
			continue
		}
		list = append(list, pageItem{Name: it.Name, Href: esc(prefix + it.Name), Size: size(it.Size)})
	}
	up := ""
	if a.base != "" {
		up = "/"
		if a.dir != "" {
			up = esc("/" + a.base + "/" + dirSlash(path.Dir(a.dir)))
			if path.Dir(a.dir) == "." {
				up = esc("/" + a.base + "/")
			}
		}
	}
	upLabel := tr.T("All folders")
	if a.dir != "" {
		upLabel = tr.T("Up one folder")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTmpl.Execute(w, map[string]any{
		"Title": a.title, "Up": up, "UpLabel": upLabel,
		"Folders": folders, "Items": list, "Empty": a.base != "" && len(a.items) == 0,
		"EmptyLabel": tr.T("Nothing here yet"), "Writable": a.writable, "UploadLabel": tr.T("Copy to the phone"),
		"Nested": a.nested && a.writable, "FolderLabel": tr.T("Copy a folder to the phone"),
		"Download": tr.T("Download"),
		"Hint":     tr.T("You can also connect to this address as a network drive (WebDAV). Unplugging the cable ends sharing."),
	})
}
