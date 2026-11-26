//go:build !linux

package main

import (
	"errors"
	"os"

	"github.com/kario-os/kario/ota/delta/squashfs"
)

type bundleStream struct {
	*squashfs.Stream
	path        string
	onDev, size int64
	compressed  int
}

func (b *bundleStream) Close() error { return nil }

func bundlePayload(*os.File, int) (*bundleStream, error) {
	return nil, errors.New("only on Linux")
}
