package keystore

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"
)

// AuthToken is an authenticator's word that the user just proved who they
// are, as Android's HardwareAuthToken: the PIN check and the fingerprint
// matcher make one, the operations that need the user (enrolling a finger,
// signing with a biometric-bound key) take it. It carries a MAC under a
// key made at each start of the keystore, so only the keystore can make or
// check one and none outlives a restart; the shell only passes them on.
type AuthToken struct {
	Challenge uint64
	// User is the secure user ID, made when the PIN is first set.
	User uint64
	// AuthID names the authenticator: 0 for the PIN, a finger's ID.
	AuthID uint64
	Type   uint32
	// Time is milliseconds since the keystore started.
	Time uint64
}

// Authenticator types, numbered as Android's HardwareAuthenticatorType.
const (
	AuthPIN         uint32 = 1
	AuthFingerprint uint32 = 2
)

const tokenLen = 1 + 8 + 8 + 8 + 4 + 8

var ErrBadToken = errors.New("bad_token")

func (t AuthToken) bytes() []byte {
	b := make([]byte, 0, tokenLen+sha256.Size)
	b = append(b, 0)
	b = binary.BigEndian.AppendUint64(b, t.Challenge)
	b = binary.BigEndian.AppendUint64(b, t.User)
	b = binary.BigEndian.AppendUint64(b, t.AuthID)
	b = binary.BigEndian.AppendUint32(b, t.Type)
	return binary.BigEndian.AppendUint64(b, t.Time)
}

// mintToken makes a token stamped now; call with lk.mu held.
func (s *Store) mintToken(t AuthToken) string {
	t.Time = uint64(s.lk.since().Milliseconds())
	b := t.bytes()
	m := hmac.New(sha256.New, s.lk.tokenKey)
	m.Write(b)
	return b64.EncodeToString(m.Sum(b))
}

// checkToken reads a token this keystore made, and how old it is; call
// with lk.mu held.
func (s *Store) checkToken(raw string) (AuthToken, time.Duration, error) {
	b, err := b64.DecodeString(raw)
	if err != nil || len(b) != tokenLen+sha256.Size || b[0] != 0 {
		return AuthToken{}, 0, ErrBadToken
	}
	m := hmac.New(sha256.New, s.lk.tokenKey)
	m.Write(b[:tokenLen])
	if !hmac.Equal(m.Sum(nil), b[tokenLen:]) {
		return AuthToken{}, 0, ErrBadToken
	}
	t := AuthToken{
		Challenge: binary.BigEndian.Uint64(b[1:]),
		User:      binary.BigEndian.Uint64(b[9:]),
		AuthID:    binary.BigEndian.Uint64(b[17:]),
		Type:      binary.BigEndian.Uint32(b[25:]),
		Time:      binary.BigEndian.Uint64(b[29:]),
	}
	age := s.lk.since() - time.Duration(t.Time)*time.Millisecond
	if age < 0 {
		return AuthToken{}, 0, ErrBadToken
	}
	return t, age, nil
}

// random64 is a random challenge or ID, never 0 (which means none).
func random64() uint64 {
	for {
		var b [8]byte
		_, _ = rand.Read(b[:])
		if v := binary.BigEndian.Uint64(b[:]); v != 0 {
			return v
		}
	}
}
