// phone-chatsrv stands in for the Hongyan vendor's cloud: the app logs in with
// its push token and sends messages here; replies and calls come back only
// through the push gateway, so the app need not stay resident (it is frozen
// in the background like every app).
//
//	phone-chatsrv [--listen 127.0.0.1:8791] [--addr-file PATH] (--gateway URL | --gateway-file PATH)
//
// HTTP, JSON bodies:
//
//	POST /v1/login  {user, push_token, push_key}
//	POST /v1/send   {from, to, kind, text, secs}   (see apps/chat/bot)
//
// A message reply is pushed with a notification (title: the peer, body: the
// text, thread: the conversation) plus the data the app files away, sealed
// to the app's push key; a call is a VoIP push.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/kario-os/kario/apps/chat/bot"
	"github.com/kario-os/kario/push"
)

// testClip is the browser test's video download (2 s of VP8, 96×72).
//
//go:embed testdata/clip.webm
var testClip []byte

// testManual is the browser test's text download.
//
//go:embed testdata/manual.txt
var testManual []byte

type server struct {
	gwFlag, gwFile string
	mu             sync.Mutex
	tokens         map[string]string // user -> push token
	keys           map[string]string // user -> the app's push key (seals data)
	http           *http.Client
}

func (s *server) push(user string, p push.Send) {
	s.mu.Lock()
	var key string
	p.Token, key = s.tokens[user], s.keys[user]
	s.mu.Unlock()
	if key != "" && p.Data != "" {
		sealed, err := push.Seal(key, []byte(p.Data))
		if err != nil {
			log.Printf("seal for %s: %v", user, err)
			return
		}
		p.Data = sealed
	}
	if p.Token == "" {
		log.Printf("%s is not logged in; reply dropped", user)
		return
	}
	// Any instance of the gateway cluster takes the push; an unreachable one
	// is skipped, a refusal (bad token, too large) is final.
	gws, err := push.Gateways(s.gwFlag, s.gwFile)
	raw, _ := json.Marshal(p)
	for _, gw := range gws {
		var resp *http.Response
		if resp, err = s.http.Post(gw+"/v1/push", "application/json", bytes.NewReader(raw)); err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = errors.New(resp.Status)
		}
		break
	}
	if err != nil {
		log.Printf("push to %s: %v", user, err)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var q struct {
		User      string `json:"user"`
		PushToken string `json:"push_token"`
		PushKey   string `json:"push_key"`
	}
	if !decode(w, r, &q) || q.User == "" || q.PushToken == "" {
		return
	}
	s.mu.Lock()
	s.tokens[q.User] = q.PushToken
	s.keys[q.User] = q.PushKey
	s.mu.Unlock()
	log.Printf("%s logged in", q.User)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *server) send(w http.ResponseWriter, r *http.Request) {
	var m bot.Message
	if !decode(w, r, &m) || m.From == "" || m.To == "" {
		return
	}
	name := bot.Peers[m.To]
	for _, rep := range bot.Replies(m) {
		rep := rep
		time.AfterFunc(rep.After, func() {
			if rep.Call {
				data, _ := json.Marshal(map[string]string{"type": "call", "peer": m.To})
				s.push(m.From, push.Send{Kind: "voip", Data: string(data)})
				return
			}
			data, _ := json.Marshal(map[string]string{"type": "msg", "peer": m.To, "text": rep.Text})
			s.push(m.From, push.Send{Title: name, Body: rep.Text, Data: string(data), Thread: m.To})
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8791", "address the app connects to")
	addrFile := flag.String("addr-file", "", "write the server URL here once listening")
	gw := flag.String("gateway", "", "push gateway URL")
	gwFile := flag.String("gateway-file", "", "read the push gateway URLs from these files, comma-separated")
	gwCert := flag.String("gateway-cert", "", "pin the push gateway's TLS certificate (PEM)")
	flag.Parse()
	if (*gw == "") == (*gwFile == "") {
		log.Fatal("give exactly one of --gateway and --gateway-file")
	}
	client, err := push.Client(*gwCert, 10*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{gwFlag: *gw, gwFile: *gwFile, tokens: map[string]string{}, keys: map[string]string{}, http: client}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/login", s.login)
	mux.HandleFunc("POST /v1/send", s.send)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	bot.Base = url
	for path, page := range map[string]string{"/article": bot.Article, "/arch": bot.Arch, "/search": bot.Search, "/tools": bot.Tools} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		})
	}
	// The browser test's ad (the content blocker drops it) and download.
	mux.HandleFunc("GET /phone-test-ad.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte("window.phoneAd = true;\n"))
	})
	mux.HandleFunc("GET /clip.webm", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/webm")
		w.Header().Set("Content-Disposition", `attachment; filename="clip.webm"`)
		_, _ = w.Write(testClip)
	})
	mux.HandleFunc("GET /manual.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="manual.txt"`)
		_, _ = w.Write(testManual)
	})
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile+".tmp", []byte(url+"\n"), 0o644); err != nil {
			log.Fatal(err)
		}
		if err := os.Rename(*addrFile+".tmp", *addrFile); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("phone-chatsrv (simulated Hongyan server): %s", url)
	log.Fatal(http.Serve(ln, mux))
}
