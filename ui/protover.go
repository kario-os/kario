package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Major versions of phone.ui (22-developer-api.md §2.1). Within a major the
// protocol only grows; a new major may rename, remove or retype what the
// one before carried. The Shell's own form, Msg and the types of this
// package, is always the newest major's (ProtoMajor); during a transition
// the Shell also speaks the major before it, for at least one release,
// translating (shell/protover.go). The handshake is the same in every
// major: hello's proto and protos, start's proto.

// ProtoMajor is Proto's major version, the Shell's own.
const ProtoMajor = 0

const protoBase = "phone.ui/"

// ProtoName is a major's name in the handshake ("phone.ui/0").
func ProtoName(major int) string { return protoBase + strconv.Itoa(major) }

// ParseProto reads "phone.ui/<major>[.<minor>]".
func ParseProto(p string) (int, bool) {
	v, ok := strings.CutPrefix(p, protoBase)
	if !ok {
		return 0, false
	}
	major, minor, dotted := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil || n < 0 || major != strconv.Itoa(n) {
		return 0, false
	}
	if dotted {
		if m, err := strconv.Atoi(minor); err != nil || m < 0 || minor != strconv.Itoa(m) {
			return 0, false
		}
	}
	return n, true
}

// Negotiate picks, of the majors a system speaks (its own first), the
// newest the app's hello offers: proto (what a system that does not
// negotiate reads, so the oldest the app speaks) and protos (every one it
// speaks). Hello without either is taken for the system's own major.
func Negotiate(majors []int, proto string, protos []string) (int, bool) {
	if proto == "" && len(protos) == 0 {
		return majors[0], true
	}
	offered := map[int]bool{}
	for _, p := range append([]string{proto}, protos...) {
		if n, ok := ParseProto(p); ok {
			offered[n] = true
		}
	}
	best, found := 0, false
	for _, m := range majors {
		if offered[m] && (!found || m > best) {
			best, found = m, true
		}
	}
	return best, found
}

// ProtoForm is how one major's messages differ from this package's form,
// as renames, so that an SDK speaking it besides its own turns each
// message (ToMajor before sending, FromMajor on receipt) and a Shell
// speaking it in a transition does the same the other way. A rule with T
// renames the message type T to As (keeps it when As is empty) and, in
// messages of that type, the fields of Fields (this form's name to the
// major's); a rule without T renames those fields in every message. The
// SDKs (Go, C, both TypeScript runtimes) read forms of other majors from
// EnvTestForms as JSON: the system never sets it, tests do.
type ProtoForm struct {
	Major int         `json:"major"`
	Rules []ProtoRule `json:"rules,omitempty"`
}

type ProtoRule struct {
	T      string            `json:"t,omitempty"`
	As     string            `json:"as,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

func msgType(m map[string]json.RawMessage) string {
	var t string
	_ = json.Unmarshal(m["t"], &t)
	return t
}

func rename(m map[string]json.RawMessage, from, to string) {
	if v, ok := m[from]; ok {
		delete(m, from)
		m[to] = v
	}
}

// ToMajor turns m, a message as its fields, from this package's form into
// the major's.
func (f ProtoForm) ToMajor(m map[string]json.RawMessage) {
	t := msgType(m)
	for _, r := range f.Rules {
		if r.T != "" && r.T != t {
			continue
		}
		for own, theirs := range r.Fields {
			rename(m, own, theirs)
		}
		if r.T != "" && r.As != "" {
			m["t"], _ = json.Marshal(r.As)
		}
	}
}

// FromMajor turns m from the major's form into this package's; a type the
// major does not have (one a rule renames, which no rule renames to) is an
// error.
func (f ProtoForm) FromMajor(m map[string]json.RawMessage) error {
	t := msgType(m)
	theirs := func(r ProtoRule) string {
		if r.As != "" {
			return r.As
		}
		return r.T
	}
	has := false
	for _, r := range f.Rules {
		has = has || r.T != "" && theirs(r) == t
	}
	for _, r := range f.Rules {
		if !has && r.T == t && r.As != "" {
			return fmt.Errorf("%s has no %s (it is %s)", ProtoName(f.Major), r.T, r.As)
		}
	}
	for _, r := range f.Rules {
		if r.T != "" && theirs(r) != t {
			continue
		}
		for own, their := range r.Fields {
			rename(m, their, own)
		}
		if r.T != "" {
			m["t"], _ = json.Marshal(r.T)
		}
	}
	return nil
}
