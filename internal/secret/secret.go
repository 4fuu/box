// Package secret builds one-time passwords and node pairing codes.
// The raw value is returned to the caller to print. It is not logged here.
package secret

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
)

const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// ClientPassword is four-character groups the operator can type.
func ClientPassword() (string, error) {
	raw, err := draw(20)
	if err != nil {
		return "", err
	}
	return group(raw), nil
}

// NodeCode is a short single-use pairing code.
func NodeCode() (string, error) {
	return draw(8)
}

// Hash is what the server stores. Pairing rows keep this, not the secret.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// NormalizePassword accepts the printed form with or without hyphens.
func NormalizePassword(s string) string {
	s = strings.TrimSpace(s)
	flat := strings.ReplaceAll(s, "-", "")
	flat = strings.ReplaceAll(flat, " ", "")
	if len(flat) == 20 && isAlphabet(flat) {
		return group(flat)
	}
	return s
}

func draw(n int) (string, error) {
	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}

func group(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(alphabet, rune(s[i])) {
			return false
		}
	}
	return true
}
