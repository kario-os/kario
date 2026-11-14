// phone-pkg is the developer's side of the store: a signing key, registering
// it with the store (with an identity number the store staff verify; run
// register again to fetch the certificate once they have), building signed
// packages (mini-program TypeScript is compiled to JavaScript here, so
// devices only ever run JavaScript), submitting them for review and reading
// the apps' crash reports (requests signed with the key).
//
//	phone-pkg keygen -key dev.key
//	phone-pkg register -store URL -key dev.key -name "Example Developer" -kind company -number CREDIT-CODE -cert dev.cert
//	phone-pkg build -key dev.key -cert dev.cert -o app.phoneapp DIR
//	phone-pkg submit -store URL [-rollout PERCENT] app.phoneapp
//	phone-pkg rollout -store URL -key dev.key -name "Example Developer" APP-ID VERSION PERCENT
//	phone-pkg crashes -store URL -key dev.key -name "Example Developer" APP-ID
//
// An update submitted with -rollout goes to that percentage of devices (0:
// none yet); rollout moves it, 100 gives it to everyone. The store halts a
// staged version that crashes on many of the devices that took it.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

var b64 = base64.StdEncoding

func die(err error) {
	fmt.Fprintln(os.Stderr, "phone-pkg:", err)
	os.Exit(1)
}

func readKey(path string) ed25519.PrivateKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		die(err)
	}
	seed, err := b64.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		die(fmt.Errorf("%s is not a phone-pkg key", path))
	}
	return ed25519.NewKeyFromSeed(seed)
}

func post(url, ctype string, body []byte, out any) error {
	cl := &http.Client{Timeout: 30 * time.Second}
	res, err := cl.Post(url, ctype, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("store: %s", e.Error)
	}
	return json.Unmarshal(raw, out)
}

// compileMiniProgram copies a mini-program with app.ts / app.tsx turned
// into app.js (types stripped, JSX into h() calls, as the host does).
func compileMiniProgram(dir string) (string, error) {
	tmp, err := os.MkdirTemp("", "phone-pkg-")
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(tmp, rel)
		if base := filepath.Base(rel); base == "app.ts" || base == "app.tsx" {
			loader := api.LoaderTS
			if base == "app.tsx" {
				loader = api.LoaderTSX
			}
			res := api.Transform(string(raw), api.TransformOptions{Loader: loader, Target: api.ES2017, Sourcefile: rel, JSXFactory: "h", JSXFragment: "Fragment"})
			if len(res.Errors) > 0 {
				return fmt.Errorf("%s: %s", rel, res.Errors[0].Text)
			}
			raw, dst = res.Code, filepath.Join(filepath.Dir(dst), "app.js")
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644)
	})
	return tmp, err
}

func main() {
	if len(os.Args) < 2 {
		die(errors.New("usage: phone-pkg keygen|register|build|submit ..."))
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	key := fs.String("key", "dev.key", "developer signing key")
	cert := fs.String("cert", "dev.cert", "developer certificate from the store")
	store := fs.String("store", os.Getenv(ui.EnvStoreServer), "store backend URL")
	name := fs.String("name", "", "developer name (register, crashes)")
	kind := fs.String("kind", "company", "register: company (unified social credit code) or person (resident ID number)")
	number := fs.String("number", "", "register: the identity number")
	out := fs.String("o", "app.phoneapp", "package to write (build)")
	rollout := fs.Int("rollout", 100, "submit: the percentage of devices an update goes to first")
	_ = fs.Parse(os.Args[2:])
	switch os.Args[1] {
	case "keygen":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			die(err)
		}
		if err := os.WriteFile(*key, []byte(b64.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
			die(err)
		}
	case "register":
		k := readKey(*key)
		body, _ := json.Marshal(map[string]string{"name": *name, "pub": b64.EncodeToString(k.Public().(ed25519.PublicKey)), "kind": *kind, "number": *number})
		var d struct {
			Status string       `json:"status"`
			Reason string       `json:"reason"`
			Cert   *pkg.DevCert `json:"cert"`
		}
		if err := post(strings.TrimSuffix(*store, "/")+"/v1/developers", "application/json", body, &d); err != nil {
			die(err)
		}
		if d.Cert == nil {
			fmt.Printf("registered %s: %s %s (run register again once the store has verified you)\n", *name, d.Status, d.Reason)
			return
		}
		raw, _ := json.MarshalIndent(d.Cert, "", "  ")
		if err := os.WriteFile(*cert, raw, 0o644); err != nil {
			die(err)
		}
		fmt.Printf("verified; certificate from store %s in %s\n", d.Cert.Store, *cert)
	case "build":
		dir := fs.Arg(0)
		var c pkg.DevCert
		raw, err := os.ReadFile(*cert)
		if err == nil {
			err = json.Unmarshal(raw, &c)
		}
		if err != nil {
			die(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "app.json")); err == nil {
			if dir, err = compileMiniProgram(dir); err != nil {
				die(err)
			}
			defer os.RemoveAll(dir)
		}
		pk, err := pkg.Build(dir, pkg.Developer{Key: readKey(*key), Cert: c})
		if err != nil {
			die(err)
		}
		if err := os.WriteFile(*out, pk, 0o644); err != nil {
			die(err)
		}
		fmt.Printf("%s: %d bytes\n", *out, len(pk))
	case "submit":
		raw, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			die(err)
		}
		var res struct {
			Status string         `json:"status"`
			Entry  map[string]any `json:"entry"`
		}
		url := strings.TrimSuffix(*store, "/") + "/v1/submissions"
		if *rollout < 100 {
			url += fmt.Sprintf("?rollout=%d", *rollout)
		}
		if err := post(url, "application/octet-stream", raw, &res); err != nil {
			die(err)
		}
		fmt.Printf("%v %v: %s\n", res.Entry["id"], res.Entry["version"], res.Status)
	case "rollout":
		app, version := fs.Arg(0), fs.Arg(1)
		var percent int
		if _, err := fmt.Sscan(fs.Arg(2), &percent); err != nil || app == "" || version == "" {
			die(errors.New("usage: phone-pkg rollout -store URL -key dev.key -name NAME APP-ID VERSION PERCENT"))
		}
		k, t := readKey(*key), time.Now().Unix()
		body, _ := json.Marshal(map[string]any{"app": app, "version": version, "percent": percent})
		req, _ := http.NewRequest("POST", strings.TrimSuffix(*store, "/")+"/v1/developer/rollout", bytes.NewReader(body))
		req.Header.Set("X-Developer", *name)
		req.Header.Set("X-Time", fmt.Sprint(t))
		req.Header.Set("X-Signature", b64.EncodeToString(ed25519.Sign(k, []byte(pkg.RolloutMsg(*name, t, app, version, percent)))))
		res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			die(err)
		}
		defer res.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if res.StatusCode != 200 {
			die(fmt.Errorf("store: %s", strings.TrimSpace(string(msg))))
		}
		fmt.Printf("%s %s: rolled out to %d%%\n", app, version, percent)
	case "crashes":
		app, k, t := fs.Arg(0), readKey(*key), time.Now().Unix()
		req, _ := http.NewRequest("GET", strings.TrimSuffix(*store, "/")+"/v1/crashes?app="+app, nil)
		req.Header.Set("X-Developer", *name)
		req.Header.Set("X-Time", fmt.Sprint(t))
		req.Header.Set("X-Signature", b64.EncodeToString(ed25519.Sign(k, []byte(fmt.Sprintf("phone-crashes/1|%s|%d", app, t)))))
		res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			die(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		if res.StatusCode != 200 {
			die(fmt.Errorf("store: %s", strings.TrimSpace(string(body))))
		}
		os.Stdout.Write(body)
	default:
		die(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}
