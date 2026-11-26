package shell

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// exampleDeveloper is the sample developer's registered name ("example
// developer" in Chinese): it is signed into its certificate and requests,
// and the selftest expects it.
const exampleDeveloper = "\u793a\u4f8b\u5f00\u53d1\u8005"

// storeStaff makes a request as the store staff (tests): the token the
// backend wrote to RunDir/storesrv-admin.token.
func (c *Core) storeStaff(path string, body any) error {
	url := strings.TrimSuffix(c.appEnvValue(ui.EnvStoreServer), "/")
	tok, err := os.ReadFile(filepath.Join(c.cfg.RunDir, "storesrv-admin.token"))
	if err != nil || url == "" {
		return errors.New("no store backend staff token")
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("%s: %s", path, strings.TrimSpace(string(msg)))
	}
	return nil
}

// RolloutExample plays the developer PublishExample is moving a staged
// version to percent of devices.
func (c *Core) RolloutExample(id, version string, percent int) error {
	url := strings.TrimSuffix(c.appEnvValue(ui.EnvStoreServer), "/")
	raw, err := os.ReadFile(filepath.Join(c.cfg.StateDir, "developer", "store.key"))
	if err != nil || url == "" {
		return errors.New("no developer has published yet")
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("bad developer key")
	}
	now := time.Now().Unix()
	body, _ := json.Marshal(map[string]any{"app": id, "version": version, "percent": percent})
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/developer/rollout", bytes.NewReader(body))
	req.Header.Set("X-Developer", exampleDeveloper)
	req.Header.Set("X-Time", fmt.Sprint(now))
	req.Header.Set("X-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed),
		[]byte(pkg.RolloutMsg(exampleDeveloper, now, id, version, percent)))))
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("rollout: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

// TakedownExample plays the store staff taking an app down.
func (c *Core) TakedownExample(id, reason string) error {
	return c.storeStaff("/v1/admin/takedowns/"+id, map[string]string{"reason": reason})
}

// PublishExample plays a store developer (tests): register a key kept in
// StateDir/developer and a company's credit code with the store backend
// (the staff verify it), build the sample app at the given version and
// submit it; a submission waiting for a human review is approved.
func (c *Core) PublishExample(name, version string, rollout int) error {
	url := strings.TrimSuffix(c.appEnvValue(ui.EnvStoreServer), "/")
	if url == "" {
		return errors.New("no store backend (PHONE_STORE_SERVER)")
	}
	developer := exampleDeveloper
	keyPath := filepath.Join(c.cfg.StateDir, "developer", "store.key")
	var key ed25519.PrivateKey
	if raw, err := os.ReadFile(keyPath); err == nil {
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("bad developer key")
		}
		key = ed25519.NewKeyFromSeed(seed)
	} else {
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		key = k
		_ = os.MkdirAll(filepath.Dir(keyPath), 0o700)
		if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(k.Seed())), 0o600); err != nil {
			return err
		}
	}
	cl := &http.Client{Timeout: 20 * time.Second}
	call := func(path, ctype string, body []byte, out any) error {
		res, err := cl.Post(url+path, ctype, bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode != 200 {
			return fmt.Errorf("%s: %s", path, strings.TrimSpace(string(raw)))
		}
		return json.Unmarshal(raw, out)
	}
	reg, _ := json.Marshal(map[string]string{"name": developer, "pub": base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)),
		"kind": "company", "number": "91110108MA01ABCD2H"})
	var dev struct {
		Status string       `json:"status"`
		Cert   *pkg.DevCert `json:"cert"`
	}
	if err := call("/v1/developers", "application/json", reg, &dev); err != nil {
		return err
	}
	if dev.Cert == nil {
		if err := c.storeStaff("/v1/admin/developers/"+developer, nil); err != nil {
			return err
		}
		if err := call("/v1/developers", "application/json", reg, &dev); err != nil || dev.Cert == nil {
			return fmt.Errorf("developer not certified after verification: %v", err)
		}
	}
	cert := *dev.Cert
	src := filepath.Join(filepath.Dir(c.cfg.AppsDir), "examples", name)
	tmp, err := os.MkdirTemp("", "phone-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info, err := e.Info(); err == nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if e.Name() == "manifest.json" {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			m["version"] = version
			raw, _ = json.MarshalIndent(m, "", "  ")
		}
		if err := os.WriteFile(filepath.Join(tmp, e.Name()), raw, mode); err != nil {
			return err
		}
	}
	pk, err := pkg.Build(tmp, pkg.Developer{Key: key, Cert: cert})
	if err != nil {
		return err
	}
	var sub struct {
		Status string `json:"status"`
		Entry  struct {
			ID string `json:"id"`
		} `json:"entry"`
	}
	path := "/v1/submissions"
	if rollout < 100 {
		path += fmt.Sprintf("?rollout=%d", rollout)
	}
	if err := call(path, "application/octet-stream", pk, &sub); err != nil {
		return err
	}
	if sub.Status == "pending_review" {
		return c.storeStaff("/v1/admin/reviews/"+sub.Entry.ID+"/"+version, map[string]string{})
	}
	return nil
}
