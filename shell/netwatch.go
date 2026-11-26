package shell

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// The network's events, for the contacts accounts' syncs (accounts.go): a
// netlink route socket's link, address and route messages (a Wi-Fi network
// joined or left, mobile data up, an address that changed) and
// telephonyd's data link (telephony.go). A burst of them is one change,
// netDebounce after the last. A developer machine (no SKU) and the tests
// have their own network, which a sync does not wait for.

const netDebounce = 2 * time.Second

func (c *Core) watchNetEvents() {
	a := &c.accounts
	if a.netWatching || a.net != nil || c.cfg.SKU.ID == "" {
		return
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		c.log.Printf("accounts: no network events: %v", err)
		return
	}
	groups := unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR | unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: uint32(groups)}); err != nil {
		unix.Close(fd)
		c.log.Printf("accounts: no network events: %v", err)
		return
	}
	a.netWatching = true
	events := make(chan struct{}, 1)
	go func() {
		defer close(events)
		defer unix.Close(fd)
		buf := make([]byte, 64<<10)
		for !c.stopping.Load() {
			n, _, err := unix.Recvfrom(fd, buf, 0)
			switch {
			case errors.Is(err, unix.EINTR), errors.Is(err, unix.ENOBUFS):
				// ENOBUFS: messages were lost, which is a change too.
				n = 1
			case err != nil:
				c.log.Printf("accounts: network events: %v", err)
				return
			}
			if n > 0 {
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	go func() {
		for range events {
			t := time.NewTimer(netDebounce)
		burst:
			for {
				select {
				case _, ok := <-events:
					if !ok {
						t.Stop()
						return
					}
					t.Reset(netDebounce)
				case <-t.C:
					break burst
				}
			}
			c.Post(c.accountsNetChanged)
		}
	}()
}
