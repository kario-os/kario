//go:build !linux

package pushd

import "time"

var started = time.Now()

func sinceBoot() time.Duration { return time.Since(started) }
