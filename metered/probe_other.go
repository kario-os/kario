//go:build !linux

package metered

import (
	"errors"
	"net"
	"time"
)

// Probe needs Linux's packet sockets.
func Probe(string, net.IP, time.Duration) ([]byte, error) {
	return nil, errors.New("metered: only on Linux")
}
