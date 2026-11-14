package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type publishOptions struct {
	store   string
	rollout int
}

type submission struct {
	Status string `json:"status"`
	Entry  struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"entry"`
}

// publish hands a signed package to the store's review (POST
// /v1/submissions, as phone-pkg submit): new apps and new permissions wait
// for the staff, other updates are published at once.
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
	url := strings.TrimSuffix(o.store, "/") + "/v1/submissions"
	if o.rollout < 100 {
		url += fmt.Sprintf("?rollout=%d", o.rollout)
	}
	res, err := (&http.Client{Timeout: 60 * time.Second}).Post(url, "application/octet-stream", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("store: %s", e.Error)
	}
	var s submission
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &s, nil
}
