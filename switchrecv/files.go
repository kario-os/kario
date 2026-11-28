package switchrecv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// fileState is a file offered in this session: staged under a name made
// from its id (ids may be "." or ".."), its hash taken as the chunks come,
// in order.
type fileState struct {
	id, kind, name, mime string
	size, takenAt        int64
	sum                  []byte
	path                 string
	got                  int64
	h                    hash.Hash
	stored               bool
}

var mimePattern = regexp.MustCompile(`^[A-Za-z0-9.+-]{1,40}/[A-Za-z0-9.+-]{1,60}$`)

type offerReply struct {
	Offset int64 `json:"offset"`
}

// offer answers POST /v1/files: the bytes of the file already here (0 for
// a new one, its size once it is in).
func (s *Session) offer(plain []byte) (int, any) {
	var o struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		Mime    string `json:"mime"`
		TakenAt int64  `json:"taken_at"`
		SHA256  string `json:"sha256"`
	}
	if err := json.Unmarshal(plain, &o); err != nil {
		return fail(http.StatusBadRequest, "bad_json", "a file offer is the JSON docs/36 §2.3 gives")
	}
	sum, err := hex.DecodeString(o.SHA256)
	name := cleanName(o.Name)
	switch {
	case !idPattern.MatchString(o.ID):
		return fail(http.StatusBadRequest, "bad_offer", "a file id is 1 to 64 of A-Z a-z 0-9 . _ -")
	case o.Kind != FilePhoto && o.Kind != FileVideo:
		return fail(http.StatusBadRequest, "bad_offer", "a file is a photo or a video")
	case err != nil || len(sum) != sha256.Size:
		return fail(http.StatusBadRequest, "bad_offer", "sha256 is 64 hex digits")
	case name == "":
		return fail(http.StatusBadRequest, "bad_offer", "the file needs a name")
	case o.Size <= 0:
		return fail(http.StatusBadRequest, "bad_offer", "the size is the file's bytes, more than 0")
	}
	if !slices.Contains(s.cfg.Accept, pluralKind(o.Kind)) {
		return fail(http.StatusForbidden, "not_accepted", "this phone does not take "+pluralKind(o.Kind))
	}
	mime := strings.ToLower(o.Mime)
	if !mimePattern.MatchString(mime) {
		mime = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.files[o.ID]; f != nil {
		same := f.kind == o.Kind && f.size == o.Size && string(f.sum) == string(sum)
		switch {
		case same && f.stored:
			return http.StatusOK, offerReply{Offset: f.size}
		case same:
			return http.StatusOK, offerReply{Offset: f.got}
		case !f.stored:
			// The file changed on the old phone: start it again.
			s.reserved -= f.size
			_ = os.Remove(f.path)
		}
		delete(s.files, o.ID)
	}
	switch {
	case o.Size > s.cfg.MaxFile:
		s.addFailure(Failure{Kind: o.Kind, ID: o.ID, Reason: "too big"})
		return fail(http.StatusInsufficientStorage, "too_big", fmt.Sprintf("files are at most %d bytes", s.cfg.MaxFile))
	case s.reserved+o.Size > s.capacity:
		s.addFailure(Failure{Kind: o.Kind, ID: o.ID, Reason: "no space"})
		return fail(http.StatusInsufficientStorage, "no_space", "the phone has no room for this file")
	}
	sh := sha256.Sum256([]byte(o.ID))
	f := &fileState{id: o.ID, kind: o.Kind, name: name, mime: mime, size: o.Size, takenAt: max(o.TakenAt, 0), sum: sum,
		path: filepath.Join(s.cfg.Dir, hex.EncodeToString(sh[:16])+".part"), h: sha256.New()}
	h, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		s.r.logf("switch: staging: %v", err)
		return fail(http.StatusInternalServerError, "store_failed", "the phone cannot stage the file")
	}
	h.Close()
	s.files[o.ID] = f
	s.reserved += o.Size
	return http.StatusOK, offerReply{Offset: 0}
}

// chunk answers PUT /v1/files/<id>?offset=N: N must be what is here.
func (s *Session) chunk(id string, q url.Values, plain []byte) (int, any) {
	s.mu.Lock()
	f := s.files[id]
	if f == nil || !idPattern.MatchString(id) {
		s.mu.Unlock()
		return fail(http.StatusNotFound, "not_found", "offer the file first")
	}
	at := f.got
	if f.stored {
		at = f.size
	}
	off, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || off != at || f.stored {
		s.mu.Unlock()
		return http.StatusBadRequest, errorReply{Error: "the offset is not where the file is", Code: "bad_offset", Offset: &at}
	}
	if len(plain) == 0 || len(plain) > Chunk || f.got+int64(len(plain)) > f.size {
		s.mu.Unlock()
		return fail(http.StatusBadRequest, "bad_chunk", fmt.Sprintf("a chunk is 1 to %d bytes, not past the size", Chunk))
	}
	if err := writeAt(f.path, plain, f.got); err != nil {
		s.mu.Unlock()
		s.r.logf("switch: staging: %v", err)
		return fail(http.StatusInternalServerError, "store_failed", "the phone cannot write the file")
	}
	f.h.Write(plain)
	f.got += int64(len(plain))
	s.received += int64(len(plain))
	if f.got < f.size {
		s.mu.Unlock()
		return http.StatusOK, offerReply{Offset: f.got}
	}
	if string(f.h.Sum(nil)) != string(f.sum) {
		_ = os.Remove(f.path)
		delete(s.files, id)
		s.reserved -= f.size
		s.addFailure(Failure{Kind: f.kind, ID: id, Reason: "sha256"})
		s.mu.Unlock()
		s.r.logf("switch: a %s's SHA-256 did not match; dropped", f.kind)
		return fail(http.StatusUnprocessableEntity, "bad_hash", "the file's SHA-256 is not the one offered; it was dropped")
	}
	done := File{Path: f.path, ID: id, Kind: f.kind, Name: f.name, Mime: f.mime, Size: f.size, TakenAt: f.takenAt}
	s.mu.Unlock()
	err = s.cfg.Sink.File(done)
	_ = os.Remove(done.Path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		delete(s.files, id)
		s.reserved -= f.size
		s.addFailure(Failure{Kind: f.kind, ID: id, Reason: "not stored"})
		s.r.logf("switch: a %s was not stored: %v", f.kind, err)
		return fail(http.StatusInternalServerError, "store_failed", "the phone could not keep the file")
	}
	f.stored = true
	s.count[pluralKind(f.kind)].Stored++
	s.clearFailure(f.kind, id)
	return http.StatusOK, offerReply{Offset: f.size}
}

func writeAt(path string, b []byte, off int64) error {
	h, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = h.WriteAt(b, off)
	if cerr := h.Close(); err == nil {
		err = cerr
	}
	return err
}

// cleanName is a file name as the phone keeps it: the last part of a path,
// no control characters, at most 128 characters; "" when nothing is left.
func cleanName(name string) string {
	name = strings.ReplaceAll(strings.ToValidUTF8(name, ""), "\\", "/")
	name = strings.TrimSpace(name[strings.LastIndex(name, "/")+1:])
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if r := []rune(name); len(r) > 128 {
		name = string(r[:128])
	}
	if name == "." || name == ".." || !utf8.ValidString(name) {
		return ""
	}
	return name
}
