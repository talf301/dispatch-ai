package id

import (
	"crypto/rand"
	"encoding/hex"
)

// Generate returns a 4-character lowercase hex string (2 random bytes).
func Generate() string {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
