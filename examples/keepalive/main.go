// Keepalive Sample, a sample third-party app with a connection of its own
// (net.keepalive, 21-push.md §8), as a messenger that does not go through
// the system push would have. The system dials the server for it and hands
// it the socket; the app reads its messages in a goroutine of its own and
// sends a heartbeat only when the system says so (the keepalive event,
// beat), never on a timer: frozen in the background it is thawed for those
// heartbeats, which ride wakes that come anyway, and for data the server
// sends. The protocol is lines: HELLO on connecting, PING as the
// heartbeat, any other line from the server a message. A skeleton.
package main

import (
	"bufio"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

func main() {
	// The selftest finds the lines in the app's log by their start.
	log.SetFlags(0)
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	var conn net.Conn
	var res ui.KeepaliveRes
	var addr, last string
	beats := 0
	screen := func() ui.Screen {
		s := ui.Screen{ID: "main", Title: tr.T("Keepalive Sample"), Body: []ui.Node{
			{Kind: ui.KindInput, ID: "addr", Placeholder: tr.T("Server (host:port)"), Value: addr, Submit: "connect"},
		}}
		if conn == nil {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Not connected")))
			return s
		}
		s.Actions = []ui.Action{{ID: "close", Label: tr.T("Disconnect"), Key: "x"}}
		s.Body = append(s.Body, sdk.Styled(ui.StyleAccent, tr.T("Connected to %s · heartbeat every %d s (push heartbeat %d s)", res.Addr, res.EverySec, res.PushSec)),
			ui.Node{Kind: ui.KindText, Text: tr.T("Heartbeats sent: %d", beats)})
		if last != "" {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Text: tr.T("Last message: %s", last)})
		}
		return s
	}
	ended := func(c net.Conn, why string) {
		if conn != c {
			return
		}
		conn.Close()
		conn = nil
		log.Printf("keepalive closed: %s", why)
		app.Update(screen())
	}
	read := func(c net.Conn) {
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("keepalive got: %s", line)
			app.Post(func() {
				last = line
				app.Update(screen())
			})
		}
		app.Post(func() { ended(c, "the server closed it") })
	}
	connect := func(target string) {
		host, port, err := net.SplitHostPort(target)
		n, _ := strconv.Atoi(port)
		if err != nil || n == 0 {
			app.Toast(tr.T("Enter the server as host:port"))
			return
		}
		go func() {
			var r ui.KeepaliveRes
			var c net.Conn
			_, err := app.CallFile(ui.CapNetKeepalive, ui.KeepaliveArgs{Host: host, Port: n, EverySec: 600}, &r, func(f *os.File) (int64, error) {
				var err error
				if c, err = net.FileConn(f); err == nil {
					// Go turns TCP keep-alive on; the heartbeats are the system's.
					err = c.(*net.TCPConn).SetKeepAlive(false)
				}
				return 0, err
			})
			app.Post(func() {
				if err != nil {
					app.Toast(tr.T("Not connected: %v", err))
					return
				}
				if conn != nil {
					conn.Close()
				}
				conn, res, beats, last = c, r, 0, ""
				_, _ = conn.Write([]byte("HELLO\n"))
				log.Printf("keepalive connected: %s every %d s", r.Addr, r.EverySec)
				go read(c)
				app.Update(screen())
			})
		}()
	}
	app.OnStart = func(string) { app.Reset(screen()) }
	app.OnEvent = func(name, value string) {
		if name != ui.EventKeepalive || conn == nil {
			return
		}
		switch value {
		case ui.KeepaliveBeat:
			if _, err := conn.Write([]byte("PING\n")); err != nil {
				ended(conn, err.Error())
				return
			}
			beats++
			log.Printf("keepalive beat %d", beats)
			app.Update(screen())
		case ui.KeepaliveClosed:
			ended(conn, "the system says the server closed it")
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		switch e.Action {
		case "connect":
			addr = strings.TrimSpace(e.Fields["addr"])
			connect(addr)
		case "close":
			if conn != nil {
				_ = app.Call(ui.CapNetKeepalive, ui.KeepaliveArgs{Close: true}, nil)
				ended(conn, "disconnected")
			}
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
