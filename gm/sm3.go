// Package gm is the Chinese commercial cryptography (GM/T) the payment and
// transport paths need: the SM3 hash (GB/T 32905), SM2 signatures
// (GB/T 32918) on the recommended curve and the SM4 block cipher
// (GB/T 32907).
//
// It is a thin layer over github.com/emmansun/gmsm, which has
// constant-time curve arithmetic and cipher (assembly on amd64 and arm64)
// and hedged signature nonces; the tests here pin it to the standards'
// vectors. In production the device keys live in the TEE or a secure
// element and sign there.
package gm

import (
	"hash"

	"github.com/emmansun/gmsm/sm3"
)

const (
	Size      = sm3.Size
	BlockSize = sm3.BlockSize
)

// NewSM3 returns an SM3 hash.
func NewSM3() hash.Hash { return sm3.New() }

// SM3 returns the SM3 digest of data.
func SM3(data []byte) [Size]byte { return sm3.Sum(data) }
