//go:build !linux

package displayd

import "errors"

func readKMS(string) (kmsInfo, error) { return kmsInfo{}, errors.New("DRM/KMS is Linux's") }
