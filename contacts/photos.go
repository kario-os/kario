package contacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"time"
)

// Photos are files beside the contacts' (contact_photos/<key>), a key
// being the start of the picture's SHA-256: a contact's Photo. A picture
// is kept as it came (a card's, byte for byte), so a sync writes back
// what it read and the server's ETag does not move; the same picture is
// one file. Files no contact shows go some time after (gcPhotos): a key
// contacts.photo just made waits for its contact.

// Limits of a photo the store keeps (one from a card or a vCard file);
// a larger one is left where it was.
const (
	MaxPhotoBytes = 1 << 20
	MaxPhotoSide  = 4096
)

var (
	ErrPhoto   = fmt.Errorf("a photo is a JPEG, PNG or GIF picture of at most %d KB and %d pixels a side", MaxPhotoBytes>>10, MaxPhotoSide)
	ErrNoPhoto = errors.New("no such photo")
)

// photoKeep is how long a photo no contact shows stays.
const photoKeep = time.Hour

// PhotoKey is the key a picture is kept under.
func PhotoKey(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func photoKeyOK(k string) bool {
	if len(k) != 16 {
		return false
	}
	_, err := hex.DecodeString(k)
	return err == nil
}

// PhotoOK reports whether the store keeps a picture: a JPEG, PNG or GIF
// within the limits.
func PhotoOK(data []byte) bool {
	if len(data) == 0 || len(data) > MaxPhotoBytes {
		return false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && (format == "jpeg" || format == "png" || format == "gif") &&
		cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= MaxPhotoSide && cfg.Height <= MaxPhotoSide
}

func (s *Store) photosDir() string { return filepath.Join(filepath.Dir(s.path), "contact_photos") }

// PutPhoto keeps a picture and gives its key.
func (s *Store) PutPhoto(data []byte) (string, error) {
	if !PhotoOK(data) {
		return "", ErrPhoto
	}
	key := PhotoKey(data)
	p := filepath.Join(s.photosDir(), key)
	if fileExists(p) {
		now := time.Now()
		_ = os.Chtimes(p, now, now)
		return key, nil
	}
	if err := WriteFile(p, data); err != nil {
		return "", err
	}
	return key, nil
}

// Photo is the picture kept under key.
func (s *Store) Photo(key string) ([]byte, error) {
	if !photoKeyOK(key) {
		return nil, ErrNoPhoto
	}
	raw, err := os.ReadFile(filepath.Join(s.photosDir(), key))
	if os.IsNotExist(err) {
		return nil, ErrNoPhoto
	}
	return raw, err
}

func (s *Store) HasPhoto(key string) bool {
	return photoKeyOK(key) && fileExists(filepath.Join(s.photosDir(), key))
}

// gcPhotos removes the photos no contact shows that are older than
// photoKeep, at most once in that time.
func (s *Store) gcPhotos(now time.Time) {
	if now.Sub(s.gcAt) < photoKeep || s.unreadable != nil || s.Kept != "" {
		return
	}
	s.gcAt = now
	entries, err := os.ReadDir(s.photosDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, c := range s.list {
		used[c.Photo] = true
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || used[e.Name()] || now.Sub(info.ModTime()) < photoKeep {
			continue
		}
		_ = os.Remove(filepath.Join(s.photosDir(), e.Name()))
	}
}
