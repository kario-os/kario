package main

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

type publishOptions struct {
	store   string
	rollout int
	// symbols is the package's symbol file ("" the one kario package wrote
	// beside it, if any; "-" none), uploaded once the store has the
	// package, the request signed with key (the developer's, as the
	// package).
	symbols, key string
}

type submission struct {
	Status string `json:"status"`
	Entry  struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"entry"`
	// Warning is what the store took this time and will not later (an
	// update without a privacy label in the store's grace).
	Warning string `json:"warning"`
	// Symbols says what became of the symbol file ("" none sent).
	Symbols string `json:"-"`
}

// publish hands a signed package to the store's review (POST
// /v1/submissions, as phone-pkg submit): new apps and new permissions wait
// for the staff, other updates are published at once. Its symbol file
// follows (POST /v1/symbols/<app>/<version>): the store keeps it for the
// developer and the reviewers only and symbolizes the app's crash reports
// with it.
func publish(file string, o publishOptions) (*submission, error) {
	if o.store == "" {
		return nil, errors.New("-store: the store backend's URL (or $KARIO_STORE)")
	}
	if o.rollout < 0 || o.rollout > 100 {
		return nil, errors.New("-rollout is a percentage, 0 to 100")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var symbols []byte
	switch o.symbols {
	case "-":
	case "":
		symbols, err = os.ReadFile(symbolsPath(file))
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	default:
		symbols, err = os.ReadFile(o.symbols)
	}
	if err != nil {
		return nil, err
	}
	var sym *jsrt.SymbolFile
	if symbols != nil {
		if sym, err = jsrt.ReadSymbolFile(symbols); err != nil {
			return nil, err
		}
		if sym.PackageSHA256 != jsrt.SHA256(raw) {
			return nil, fmt.Errorf("the symbol file is another package's (kario package writes both)")
		}
	}
	base := strings.TrimSuffix(o.store, "/")
	u := base + "/v1/submissions"
	if o.rollout < 100 {
		u += fmt.Sprintf("?rollout=%d", o.rollout)
	}
	var s submission
	if err := storeCall(&http.Request{Method: "POST", Header: http.Header{"Content-Type": {"application/octet-stream"}}}, u, raw, &s); err != nil {
		return nil, err
	}
	if sym == nil {
		return &s, nil
	}
	if s.Symbols, err = uploadSymbols(base, raw, sym, symbols, o.key); err != nil {
		return &s, fmt.Errorf("%s %s was submitted (%s), its symbol file was not: %w", s.Entry.ID, s.Entry.Version, s.Status, err)
	}
	return &s, nil
}

// uploadSymbols sends a symbol file signed as the package's developer: what
// the store said, or why it has none (a store without symbol files).
func uploadSymbols(base string, raw []byte, sym *jsrt.SymbolFile, symbols []byte, keyFile string) (string, error) {
	name, err := packageDeveloper(raw)
	if err != nil {
		return "", err
	}
	if keyFile == "" {
		keyFile = "dev.key"
	}
	key, err := readKey(keyFile)
	if err != nil {
		return "", fmt.Errorf("the symbol file is signed with the developer's key: %w", err)
	}
	now := time.Now().Unix()
	sig := ed25519.Sign(key, []byte(pkg.SymbolsMsg(name, now, sym.App, sym.Version, jsrt.SHA256(symbols))))
	req := &http.Request{Method: "POST", Header: http.Header{
		"Content-Type": {"application/zip"},
		"X-Developer":  {url.PathEscape(name)},
		"X-Time":       {strconv.FormatInt(now, 10)},
		"X-Signature":  {base64.StdEncoding.EncodeToString(sig)},
	}}
	var res struct {
		Status string `json:"status"`
	}
	err = storeCall(req, base+"/v1/symbols/"+url.PathEscape(sym.App)+"/"+url.PathEscape(sym.Version), symbols, &res)
	var nf errNotFound
	if errors.As(err, &nf) {
		return "the store takes no symbol files (" + nf.Error() + ")", nil
	}
	if err != nil {
		return "", err
	}
	return res.Status, nil
}

type errNotFound struct{ msg string }

func (e errNotFound) Error() string { return "store: " + e.msg }

// storeCall sends body with req's method and headers to u and decodes the
// JSON answer into out; the store's error is the error.
func storeCall(req *http.Request, u string, body []byte, out any) error {
	r, err := http.NewRequest(req.Method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range req.Header {
		r.Header[k] = v
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(answer, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(answer))
		}
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
			return errNotFound{e.Error}
		}
		return fmt.Errorf("store: %s", e.Error)
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// packageDeveloper is the name a package is signed in (its signature
// file, unverified: the store verifies it).
func packageDeveloper(raw []byte) (string, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err != nil {
			return "", errors.New("not a package (no signature)")
		}
		if h.Name != pkg.SigPath {
			continue
		}
		var sig pkg.Signature
		if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&sig); err != nil {
			return "", err
		}
		return sig.Developer.Name, nil
	}
}

// readKey reads a phone-pkg key file (the Ed25519 seed in base64).
func readKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a phone-pkg key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
