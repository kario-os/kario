package gm

import (
	"crypto/cipher"

	"github.com/emmansun/gmsm/sm4"
)

// SM4 is the 128-bit block cipher of GB/T 32907 as a cipher.Block, so the
// standard modes apply (cipher.NewGCM gives SM4-GCM).

const SM4BlockSize = sm4.BlockSize

// NewSM4 takes a 16-byte key.
func NewSM4(key []byte) (cipher.Block, error) { return sm4.NewCipher(key) }
