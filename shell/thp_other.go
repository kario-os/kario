//go:build !linux

package shell

func DisableHugePages() error { return nil }
