package location

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AssistCache fetches the assistance data a modem names servers for (its
// predicted orbits, XTRA) and keeps it in Dir: fetched again once a day,
// given for a week while no server can be reached, and the servers tried
// at most once an hour.
type AssistCache struct {
	Dir    string
	Client *http.Client
	Now    func() time.Time

	mu    sync.Mutex
	tried time.Time
}

const (
	assistRefresh = 24 * time.Hour
	assistKeep    = 7 * 24 * time.Hour
	assistRetry   = time.Hour
	// assistMax bounds a file (XTRA's are some tens of kilobytes).
	assistMax = 1 << 20
)

func (a *AssistCache) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Fetch is the data for these servers: the copy kept, while it is fresh,
// else a new one.
func (a *AssistCache) Fetch(ctx context.Context, servers []string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sum := sha256.Sum256([]byte(strings.Join(servers, "\n")))
	path := filepath.Join(a.Dir, "assist-"+hex.EncodeToString(sum[:6])+".bin")
	now := a.now()
	kept, err := os.ReadFile(path)
	age := assistKeep
	if st, serr := os.Stat(path); err == nil && serr == nil && len(kept) > 0 {
		age = now.Sub(st.ModTime())
	}
	if age < assistRefresh {
		return kept, nil
	}
	if now.Sub(a.tried) < assistRetry {
		if age < assistKeep {
			return kept, nil
		}
		return nil, errors.New("the assistance servers failed less than an hour ago")
	}
	a.tried = now
	var errs []error
	for _, s := range servers {
		data, err := a.get(ctx, s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_ = os.MkdirAll(a.Dir, 0o700)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err == nil {
			_ = os.Rename(tmp, path)
		}
		return data, nil
	}
	if age < assistKeep {
		return kept, nil
	}
	if len(errs) == 0 {
		return nil, errors.New("no assistance server")
	}
	return nil, errors.Join(errs...)
}

func (a *AssistCache) get(ctx context.Context, server string) ([]byte, error) {
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http(s) URL", server)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	cl := a.Client
	if cl == nil {
		cl = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u.Host, res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, assistMax+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s: %w", u.Host, err)
	case len(data) == 0 || len(data) > assistMax:
		return nil, fmt.Errorf("%s: %d bytes", u.Host, len(data))
	}
	return data, nil
}
