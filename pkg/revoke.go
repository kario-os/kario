package pkg

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Revocations is a store's list of apps it took down, signed by its root.
// Devices refuse to start an installed app on it; Issued only grows, so an
// older list (one from before a takedown) cannot be replayed.
type Revocations struct {
	Format  string    `json:"format"`
	Store   string    `json:"store"`
	Issued  int64     `json:"issued"`
	Revoked []Revoked `json:"revoked"`
	Sig     string    `json:"sig,omitempty"`
}

type Revoked struct {
	App    string `json:"app"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
}

const RevocationsFormat = "phoneapp-revocations/1"

func (r Revocations) signedBytes() []byte {
	r.Sig = ""
	raw, _ := json.Marshal(r)
	return append([]byte(RevocationsFormat+"\n"), raw...)
}

// SignRevocations signs the list with the store's root.
func (s *Store) SignRevocations(r Revocations) Revocations {
	r.Format, r.Store = RevocationsFormat, s.Name
	r.Sig = b64.EncodeToString(ed25519.Sign(s.Key, r.signedBytes()))
	return r
}

// VerifyRevocations checks a list against the store roots a device trusts.
func VerifyRevocations(raw []byte, roots map[string]ed25519.PublicKey) (*Revocations, error) {
	var r Revocations
	if err := json.Unmarshal(raw, &r); err != nil || r.Format != RevocationsFormat {
		return nil, errors.New("not a revocation list")
	}
	root, ok := roots[r.Store]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUntrustedStore, r.Store)
	}
	sig, err := b64.DecodeString(r.Sig)
	if err != nil || !ed25519.Verify(root, r.signedBytes(), sig) {
		return nil, errors.New("revocation list is not signed by the store")
	}
	return &r, nil
}

// ValidResidentID checks a mainland resident ID number (GB 11643): 17
// digits and the ISO 7064 MOD 11-2 check character.
func ValidResidentID(s string) bool {
	if len(s) != 18 {
		return false
	}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		sum += int(s[i]-'0') * w
	}
	return strings.ToUpper(s[17:]) == string("10X98765432"[sum%11])
}

// creditChars are the characters of a unified social credit code (no I, O,
// S, V, Z), in value order.
const creditChars = "0123456789ABCDEFGHJKLMNPQRTUWXY"

// ValidCreditCode checks a unified social credit code (GB 32100), the ID of
// a company: 18 characters, the last a MOD 31 check character.
func ValidCreditCode(s string) bool {
	s = strings.ToUpper(s)
	if len(s) != 18 {
		return false
	}
	weights := []int{1, 3, 9, 27, 19, 26, 16, 17, 20, 29, 25, 13, 8, 24, 10, 30, 28}
	sum := 0
	for i, w := range weights {
		v := strings.IndexByte(creditChars, s[i])
		if v < 0 {
			return false
		}
		sum += v * w
	}
	return s[17] == creditChars[(31-sum%31)%31]
}
