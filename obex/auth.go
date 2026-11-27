package obex

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

// OBEX authentication (IrOBEX 1.2 §3.5, which GOEP and its profiles use):
// the side that challenges sends a nonce, and the other proves it knows the
// password they share with the MD5 of the nonce, a colon and the password.
// A server challenges in an Unauthorized answer, and the client sends the
// request again with the response; a client challenges in its CONNECT, and
// the server's answer carries the response. Phones and car kits that use
// it have their users type the same code on both sides (Android's phone
// book server asks for a "session key" when a car kit challenges it).

// Header IDs: byte sequences of tag-length-value triplets.
const (
	hAuthChallenge = 0x4d
	hAuthResponse  = 0x4e
)

// Triplet tags: a challenge's nonce, options and realm; a response's
// digest, user ID and the nonce it answers.
const (
	tagNonce    = 0x00
	tagOptions  = 0x01
	tagRealm    = 0x02
	tagDigest   = 0x00
	tagUserID   = 0x01
	tagAnswered = 0x02
)

// A realm's first byte is its character set: ASCII, ISO-8859-1 to -9 (1
// to 9) or Unicode, which OBEX writes as UTF-16 in network order.
const (
	realmASCII   = 0x00
	realmUnicode = 0xff
)

var realmISO = [...]*charmap.Charmap{charmap.ISO8859_1, charmap.ISO8859_2, charmap.ISO8859_3, charmap.ISO8859_4,
	charmap.ISO8859_5, charmap.ISO8859_6, charmap.ISO8859_7, charmap.ISO8859_8, charmap.ISO8859_9}

// A challenge's options: the response must name a user; the access is
// read only.
const (
	optUserID   = 0x01
	optReadOnly = 0x02
)

const nonceSize = 16

// Auth is how one side authenticates: the answers it gives and the
// challenge it makes.
type Auth struct {
	// Ask gives the password a peer's challenge is answered with, and a
	// user ID (sent when not empty); false: there is none, and the peer's
	// Unauthorized stands. It may block while the user types it.
	Ask func(Challenge) (user, password string, ok bool)
	// Password, when not empty, is what this side challenges the peer
	// for: a client in its CONNECT, a server in its answer to a CONNECT
	// without the response.
	Password string
}

// Challenge is a peer's Authenticate Challenge: its nonce, whether the
// response has to name a user, whether the access is read only, and its
// realm: the peer's text saying which password it wants, decoded from
// its character set ("" when it has none, or one IrOBEX does not name;
// what does not decode is U+FFFD). The realm is the peer's to the last
// character: whoever shows it must not take it for its own text.
type Challenge struct {
	Nonce            [nonceSize]byte
	UserID, ReadOnly bool
	Realm            string
}

var (
	// ErrBadPassword: the peer did not take the password its challenge
	// was answered with.
	ErrBadPassword = errors.New("obex: the other device did not take the password")
	// ErrNotProven: the peer's answer to this side's challenge does not
	// prove it knows the password.
	ErrNotProven = errors.New("obex: the other device did not prove it knows the password")
)

// triplets reads tag-length-value triplets, the first of each tag; false
// when one runs past the end.
func triplets(b []byte) (map[byte][]byte, bool) {
	t := map[byte][]byte{}
	for len(b) > 0 {
		if len(b) < 2 || 2+int(b[1]) > len(b) {
			return nil, false
		}
		if _, ok := t[b[0]]; !ok {
			t[b[0]] = b[2 : 2+int(b[1])]
		}
		b = b[2+int(b[1]):]
	}
	return t, true
}

func parseChallenge(b []byte) (Challenge, error) {
	var c Challenge
	t, ok := triplets(b)
	if !ok || len(t[tagNonce]) != nonceSize {
		return c, errMalformed
	}
	copy(c.Nonce[:], t[tagNonce])
	if o := t[tagOptions]; len(o) > 0 {
		c.UserID, c.ReadOnly = o[0]&optUserID != 0, o[0]&optReadOnly != 0
	}
	c.Realm = realmText(t[tagRealm])
	return c, nil
}

// realmText decodes a realm triplet's value; NULs ending it (a C string's)
// are not its text.
func realmText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	set, b := b[0], b[1:]
	var s string
	switch {
	case set == realmASCII:
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
			if c > 0x7f {
				rs[i] = '\uFFFD'
			}
		}
		s = string(rs)
	case int(set) <= len(realmISO):
		out, _ := realmISO[set-1].NewDecoder().Bytes(b)
		s = string(out)
	case set == realmUnicode:
		s = decodeText(b)
		if len(b)%2 == 1 {
			s += "\uFFFD"
		}
	default:
		return ""
	}
	return strings.TrimRight(s, "\x00")
}

// encode is the challenge as this side sends it: Kario names no realm.
func (c Challenge) encode() []byte {
	b := append([]byte{tagNonce, nonceSize}, c.Nonce[:]...)
	var o byte
	if c.UserID {
		o |= optUserID
	}
	if c.ReadOnly {
		o |= optReadOnly
	}
	if o != 0 {
		b = append(b, tagOptions, 1, o)
	}
	return b
}

func newChallenge() Challenge {
	var c Challenge
	_, _ = rand.Read(c.Nonce[:])
	return c
}

// digest answers nonce with password: MD5(nonce ":" password).
func digest(nonce []byte, password string) [md5.Size]byte {
	b := make([]byte, 0, len(nonce)+1+len(password))
	return md5.Sum(append(append(append(b, nonce...), ':'), password...))
}

// response is the Authenticate Response to c: the digest, the user ID (as
// much as a triplet holds) and the nonce it answers.
func response(c Challenge, user, password string) header {
	d := digest(c.Nonce[:], password)
	b := append([]byte{tagDigest, md5.Size}, d[:]...)
	if user != "" {
		u := user[:min(len(user), 0xff)]
		b = append(append(b, tagUserID, byte(len(u))), u...)
	}
	b = append(append(b, tagAnswered, nonceSize), c.Nonce[:]...)
	return header{id: hAuthResponse, data: b}
}

// proves reports whether resp, an Authenticate Response's bytes, answers
// nonce with password. A response without the nonce it answers is taken
// for the one challenge out.
func proves(resp []byte, nonce [nonceSize]byte, password string) bool {
	t, ok := triplets(resp)
	if !ok || len(t[tagDigest]) != md5.Size {
		return false
	}
	if n, ok := t[tagAnswered]; ok && !bytes.Equal(n, nonce[:]) {
		return false
	}
	want := digest(nonce[:], password)
	return subtle.ConstantTimeCompare(t[tagDigest], want[:]) == 1
}
