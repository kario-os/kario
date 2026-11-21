package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kario-os/kario/ui"
)

// The major versions of phone.ui the Shell speaks (ui/protover.go,
// 22-developer-api.md §2.1): its own, in its own form (ui.Msg), and during
// a transition the one before it, whose messages each way a protoVersion
// turns. A new major is introduced by changing package ui to it (ui.Proto,
// ui.ProtoMajor, the types) and putting the major before it here, second,
// with in and out, for at least one release.

type protoVersion struct {
	major int
	// changes: what this major does differently from the Shell's own, one
	// line each, said in the log when an app speaks it.
	changes []string
	// in turns an app's message into the Shell's form, out the Shell's into
	// this major's, on the message's fields as JSON; nil for the Shell's own
	// major. A message in cannot turn is dropped as a malformed one.
	in  func(m map[string]json.RawMessage) error
	out func(m map[string]json.RawMessage)
}

// protoVersions: the Shell's own major first; a second only during a
// transition.
var protoVersions = []protoVersion{{major: ui.ProtoMajor}}

func (c *Core) versions() []protoVersion {
	if c.protos != nil {
		return c.protos
	}
	return protoVersions
}

// version is the major the Shell speaks with major's apps, if it does.
func (c *Core) version(major int) (protoVersion, bool) {
	for _, v := range c.versions() {
		if v.major == major {
			return v, true
		}
	}
	return protoVersion{}, false
}

// negotiate is the version an app's hello asks for (ui.Negotiate).
func (c *Core) negotiate(m ui.Msg) (protoVersion, bool) {
	var majors []int
	for _, v := range c.versions() {
		majors = append(majors, v.major)
	}
	n, ok := ui.Negotiate(majors, m.Proto, m.Protos)
	if !ok {
		return protoVersion{}, false
	}
	return c.version(n)
}

// spokenNames are the versions the Shell speaks, for messages.
func (c *Core) spokenNames() string {
	var names []string
	for _, v := range c.versions() {
		names = append(names, ui.ProtoName(v.major))
	}
	return strings.Join(names, " and ")
}

// spoken is the version the Shell speaks with a, once it said hello.
func (c *Core) spoken(a *App) string {
	if a.proto != nil {
		return ui.ProtoName(a.proto.major)
	}
	return ui.ProtoName(c.versions()[0].major)
}

// errNotTurned: an older major's message its in could not turn.
var errNotTurned = errors.New("protocol version")

// recvApp reads an app's next message in the Shell's form: v turns one of
// an older major.
func recvApp(conn interface{ Recv(any) error }, v *protoVersion, m *ui.Msg) error {
	if v == nil || v.in == nil {
		return conn.Recv(m)
	}
	var raw json.RawMessage
	if err := conn.Recv(&raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if err := v.in(fields); err != nil {
		return fmt.Errorf("%w: phone.ui/%d: %v", errNotTurned, v.major, err)
	}
	turned, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("%w: %v", errNotTurned, err)
	}
	return json.Unmarshal(turned, m)
}

// form is m as an app of v's major reads it; nil when it reads the
// Shell's own.
func (v *protoVersion) form(m ui.Msg) json.RawMessage {
	if v == nil || v.out == nil {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	v.out(fields)
	turned, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return turned
}
